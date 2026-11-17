package panel

// Пересылка между серверами сети (ТЗ 24, п. 14; meshd 7). Устройство,
// подключённое только к Германии, достаёт до устройства London и до самого
// London через связку серверов:
//
//	устройство → meshd Германии (адреса у него нет — вызов панели,
//	/v1/relay-wait) → панель Германии → SSH meshlink → порт обмена London
//	(подписанный POST /srv/v1/relay, дальше сырые байты) → панель London →
//	meshd London (/v1/relay-in: правила проверяются ещё раз, только свои
//	устройства и сам сервер — дальше не пересылается) → устройство.
//
// Отдельного порта нет: тот же 127.0.0.1:47847 и тот же permitopen, что у
// обмена данными. Кто есть на других серверах, meshd узнаёт от панели
// (/v1/remote, из живых картин): так у устройств появляются их имена.
//
// meshd 6 и старые панели работают как раньше: у meshd 6 нет этих входов
// (ErrUnsupported), старая панель на /srv/v1/relay отвечает 404 — устройство
// получает понятный отказ.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"time"

	"sshtunnel/internal/meshsvc"
)

// relayAdmin — вход пересылки у meshd (meshsvc.Admin; в тестах — подделка).
type relayAdmin interface {
	SetRemote(ctx context.Context, nets map[string][]meshsvc.RemotePeer) error
	RelayWait(ctx context.Context, on func(meshsvc.RelayAsk)) error
	RelayOut(ctx context.Context, call string, ok bool, errText string) (net.Conn, error)
	RelayIn(ctx context.Context, q meshsvc.RelayAsk) (net.Conn, error)
}

// relayLoop держит ожидание вызовов у своего meshd, пока сервер в сети.
func (n *ServerNet) relayLoop(ctx context.Context) {
	for ctx.Err() == nil {
		ra, ok := n.mesh.admin.(relayAdmin)
		if !ok {
			return
		}
		wait := 3 * time.Second
		if n.InNet() {
			err := ra.RelayWait(ctx, func(q meshsvc.RelayAsk) { go n.relayOut(ctx, ra, q) })
			if errors.Is(err, meshsvc.ErrUnsupported) {
				wait = 5 * time.Minute // meshd 6: пересылки нет, обновят — заработает
			}
		} else {
			wait = 10 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// relayOut — вызов от своего meshd: дотянуться до сервера q.Srv, передать
// вызов и соединить.
func (n *ServerNet) relayOut(ctx context.Context, ra relayAdmin, q meshsvc.RelayAsk) {
	answer := func(text string) {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ra.RelayOut(cctx, q.Call, false, text)
		cancel()
	}
	far, err := n.relayDial(ctx, q)
	if err != nil {
		answer(err.Error())
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	near, err := ra.RelayOut(cctx, q.Call, true, "")
	cancel()
	if err != nil {
		far.Close()
		return
	}
	pipe(near, far)
}

// relayDial — подписанный /srv/v1/relay к серверу q.Srv; соединение — уже
// с устройством (или службой сервера) там.
func (n *ServerNet) relayDial(ctx context.Context, q meshsvc.RelayAsk) (net.Conn, error) {
	n.mu.Lock()
	rec, ok := n.serversLocked()[q.Srv]
	netID, self := n.f.Net, n.f.Self
	signer, kerr := n.keyLocked()
	n.mu.Unlock()
	if !ok || q.Srv == self || netID == "" {
		return nil, errors.New("нет такого сервера в сети")
	}
	if kerr != nil {
		return nil, kerr
	}
	q.Call = ""
	body, _ := json.Marshal(q)
	dctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	conn, err := n.dial(dctx, rec, signer)
	if err != nil {
		return nil, errors.New("нет связи с сервером «" + rec.Name + "»")
	}
	req, _ := http.NewRequestWithContext(dctx, http.MethodPost, "http://srvnet/srv/v1/relay", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	if err := signRequest(req, signer, netID, self, body, n.now()); err != nil {
		conn.Close()
		return nil, err
	}
	if dl, ok := dctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, errors.New("нет связи с сервером «" + rec.Name + "»")
	}
	br := newBufReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, errors.New("сервер «" + rec.Name + "» не ответил")
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		conn.Close()
		var e struct {
			Error string `json:"error"`
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, errors.New("на сервере «" + rec.Name + "» панель не обновлена — пересылки между серверами там нет")
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return nil, errors.New(e.Error)
		}
		return nil, errors.New("сервер «" + rec.Name + "» ответил " + resp.Status)
	}
	conn.SetDeadline(time.Time{})
	return &readerConn{Conn: conn, r: br}, nil
}

// handleRelay — /srv/v1/relay: вызов с другого сервера сети к устройству
// этого (или к нему самому). Отдаётся meshd, он проверяет правила сам.
func (n *ServerNet) handleRelay(w http.ResponseWriter, r *http.Request) {
	body, ok := readSyncBody(w, r)
	if !ok {
		return
	}
	from, ok := n.authPeer(w, r, body)
	if !ok {
		return
	}
	ra, ok := n.mesh.admin.(relayAdmin)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "на этом сервере нет пересылки"})
		return
	}
	var q meshsvc.RelayAsk
	if err := json.Unmarshal(body, &q); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	q.Call, q.Srv = "", from
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	target, err := ra.RelayIn(ctx, q)
	cancel()
	if err != nil {
		text := err.Error()
		if errors.Is(err, meshsvc.ErrUnsupported) {
			text = "на сервере «" + n.mesh.selfName(n.mesh.settings.Get().Mesh) + "» старый meshd — обнови его"
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": text})
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		target.Close()
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		target.Close()
		return
	}
	conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte("HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n")); err != nil {
		conn.Close()
		target.Close()
		return
	}
	pipe(&readerConn{Conn: conn, r: rw.Reader}, target)
}

// pipe перекладывает байты в обе стороны до конца обеих.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	a.Close()
	b.Close()
}

// ---------- кто есть на других серверах ----------

// pushRemote — meshd этого сервера узнаёт устройства и серверы сети,
// которые есть только на других серверах (из живых картин).
func (n *ServerNet) pushRemote(ctx context.Context) {
	ra, ok := n.mesh.admin.(relayAdmin)
	if !ok {
		return
	}
	users := map[string]string{}
	if n.srv != nil {
		for _, c := range n.srv.clients.List() {
			users[userHash(c.Username)] = c.Username
		}
	}
	// Сети этого сервера: в каждую из них — все серверы сети серверов, даже
	// если на том сервере ещё нет ни одного устройства (иначе до нового
	// сервера с устройств не дойти: замкнутый круг).
	var local []string
	if st, err := n.mesh.admin.State(ctx); err == nil {
		for _, nw := range st.Networks {
			local = append(local, nw.ID)
		}
	}
	nets := map[string][]meshsvc.RemotePeer{}
	n.mu.Lock()
	if n.f.Net != "" {
		now := n.now().Unix()
		servers := n.serversLocked()
		ids := make([]string, 0, len(n.views))
		for id := range n.views {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			r, live := servers[id]
			pv := n.views[id]
			if !live || id == n.f.Self || !n.liveLocked(id, pv, now) {
				continue
			}
			if pv.View.MeshIP != "" && pv.View.Meshd {
				seenNet := map[string]bool{}
				for _, nw := range pv.View.Networks {
					seenNet[nw.ID] = true
				}
				for _, id := range local {
					seenNet[id] = true
				}
				nids := make([]string, 0, len(seenNet))
				for nid := range seenNet {
					nids = append(nids, nid)
				}
				sort.Strings(nids)
				for _, nid := range nids {
					nets[nid] = append(nets[nid], meshsvc.RemotePeer{IP: pv.View.MeshIP, Host: pv.View.MeshHost,
						Name: r.Name, Server: true, Srv: id, SrvName: r.Name, Online: true})
				}
			}
			for _, nw := range pv.View.Networks {
				for _, d := range nw.Devices {
					if d.Pending || d.Frozen || d.IP == "" || d.Host == "" {
						continue
					}
					nets[nw.ID] = append(nets[nw.ID], meshsvc.RemotePeer{IP: d.IP, Host: d.Host, Name: d.Display,
						User: users[d.UH], Platform: d.Platform, OS: d.OS, Srv: id, SrvName: r.Name, Online: d.Online})
				}
			}
		}
	}
	data, _ := json.Marshal(nets)
	sig := string(data)
	same := sig == n.lastRemote && time.Since(n.remoteAt) < 5*time.Minute
	n.mu.Unlock()
	if same {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ra.SetRemote(cctx, nets); err == nil {
		n.mu.Lock()
		n.lastRemote, n.remoteAt = sig, time.Now()
		n.mu.Unlock()
	}
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

func newBufReader(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }

// readerConn — соединение, у которого часть принятого уже в буфере.
type readerConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *readerConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *readerConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}
