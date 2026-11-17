package panel

// Одна картина сети на всех серверах (ТЗ 24).
//
// Общие данные (srvnet.go) — это настройки: учётные записи, правила, имена.
// Живая картина — какие устройства есть, кто на связи, задержка, трафик —
// у каждого сервера своя: устройство видно в meshd только того сервера, к
// которому оно само подключилось. Поэтому панели раз в 30 секунд (тем же
// заходом, что и обмен) и при открытии страницы (не чаще раза в 5 секунд на
// сервер) берут друг у друга живую картину подписанным запросом
// /srv/v1/view по той же связке SSH и складывают её со своей:
//
//   - устройство — это (id сети, id устройства); на связи, если на связи
//     хоть с одним сервером, который сам сейчас на связи;
//   - имя — из панели (общая запись alias:), иначе самое свежее из
//     присланных;
//   - убранное устройство (forget:) не показывается, даже если в старой
//     картине другого сервера оно ещё есть; удалённая сеть (ndel:) — так же;
//   - сервер без связи остаётся со своей последней картиной (она лежит на
//     диске, servernet-views.json): его устройства рисуются серыми и в
//     «на связи» не считаются.
//
// В картине нет ключей, паролей и имён SSH-пользователей: пользователь
// устройства идёт хешем (uh) — так другая панель узнаёт свою же учётную
// запись (они общие), но имени по сети не получает. id устройства между
// серверами сети и так ходит в общих данных (alias:, forget: …).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sshtunnel/internal/meshsvc"
)

const (
	// viewPullMin — не чаще раза в столько спрашивать картину у одного
	// сервера (страница открыта — она опрашивает каждые 5 секунд).
	viewPullMin = 5 * time.Second
	// viewFresh — картина старше этого (или сервер не на связи) — серая.
	viewFresh = 3 * syncEvery
	// maxViewDevices — сколько устройств принимать в чужой картине.
	maxViewDevices = 4096
)

// viewDevice — устройство в живой картине сервера.
type viewDevice struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Alias       string               `json:"alias,omitempty"`
	Display     string               `json:"display"`
	Host        string               `json:"host"`
	IP          string               `json:"ip"`
	Platform    string               `json:"platform,omitempty"`
	OS          string               `json:"os,omitempty"`
	App         string               `json:"app,omitempty"`
	Mode        string               `json:"mode,omitempty"`
	Hostname    string               `json:"hostname,omitempty"`
	Online      bool                 `json:"online"`
	FirstSeen   int64                `json:"firstSeen,omitempty"`
	LastSeen    int64                `json:"lastSeen,omitempty"`
	ConnectedAt int64                `json:"connectedAt,omitempty"`
	Sessions    int64                `json:"sessions,omitempty"`
	RTTMs       float64              `json:"rttMs,omitempty"`
	RTTAvgMs    float64              `json:"rttAvgMs,omitempty"`
	JitterMs    float64              `json:"jitterMs,omitempty"`
	LossPct     float64              `json:"lossPct,omitempty"`
	Quality     string               `json:"quality,omitempty"`
	BytesUp     int64                `json:"bytesUp,omitempty"`
	BytesDown   int64                `json:"bytesDown,omitempty"`
	Direct      []meshsvc.DirectLink `json:"direct,omitempty"`
	Pending     bool                 `json:"pending,omitempty"`
	Frozen      bool                 `json:"frozen,omitempty"`
	Profiles    []meshsvc.ProfileRef `json:"profiles,omitempty"`
	ExitSet     *meshsvc.ExitOrder   `json:"exitSet,omitempty"`
	UH          string               `json:"uh,omitempty"`
}

// viewNet — сеть в живой картине.
type viewNet struct {
	ID         string       `json:"id"`
	Name       string       `json:"name,omitempty"`
	Created    int64        `json:"created,omitempty"`
	EmptySince int64        `json:"emptySince,omitempty"`
	Approval   bool         `json:"approval,omitempty"`
	Devices    []viewDevice `json:"devices"`
}

// viewLink — как отправитель видит связь с другим сервером сети.
type viewLink struct {
	ID       string  `json:"id"`
	Online   bool    `json:"online"`
	RTTMs    float64 `json:"rttMs,omitempty"`
	LastSync int64   `json:"lastSync,omitempty"`
}

// srvView — ответ /srv/v1/view: живая картина сервера From.
type srvView struct {
	Net  string `json:"net"`
	From string `json:"from"`
	// T — когда снята (unix, часы отправителя).
	T        int64      `json:"t"`
	Meshd    bool       `json:"meshd"`
	MeshIP   string     `json:"meshIp,omitempty"`
	MeshHost string     `json:"meshHost,omitempty"`
	Version  string     `json:"version,omitempty"`
	Links    []viewLink `json:"links"`
	Networks []viewNet  `json:"networks"`
	// Accts — хеши пользователей (userHash), чьи учётные записи на этом
	// сервере действуют: так другая панель скажет «учётная запись на London
	// не создана» (ТЗ 24, п. 13). nil — старая панель, неизвестно.
	Accts []string `json:"accts,omitempty"`
	// Relays — идущие пересланные соединения от устройств этого сервера
	// (meshd 7): на схеме — пунктиром через оба сервера.
	Relays []meshsvc.RelayLine `json:"relays,omitempty"`
}

// peerView — картина другого сервера и когда она пришла (unix, свои часы).
type peerView struct {
	View srvView `json:"view"`
	Got  int64   `json:"got"`
}

// userHash — пользователь устройства в картине: по нему другая панель
// узнаёт свою учётную запись, не получая имени.
func userHash(user string) string {
	if user == "" {
		return ""
	}
	h := sha256.Sum256([]byte("ssh_tunnel user v1\x00" + user))
	return hex.EncodeToString(h[:12])
}

func viewDeviceOf(d meshsvc.Device) viewDevice {
	return viewDevice{ID: d.ID, Name: d.Name, Alias: d.Alias, Display: d.Display, Host: d.Host, IP: d.IP,
		Platform: d.Platform, OS: d.OS, App: d.App, Mode: d.Mode, Hostname: d.Hostname, Online: d.Online,
		FirstSeen: d.FirstSeen, LastSeen: d.LastSeen, ConnectedAt: d.ConnectedAt, Sessions: d.Sessions,
		RTTMs: d.RTTMs, RTTAvgMs: d.RTTAvgMs, JitterMs: d.JitterMs, LossPct: d.LossPct, Quality: d.Quality,
		BytesUp: d.BytesUp, BytesDown: d.BytesDown, Direct: d.Direct, Pending: d.Pending, Frozen: d.Frozen,
		Profiles: d.Profiles, ExitSet: d.ExitSet, UH: userHash(d.User)}
}

func (v viewDevice) device() meshsvc.Device {
	return meshsvc.Device{ID: v.ID, Name: v.Name, Alias: v.Alias, Display: v.Display, Host: v.Host, IP: v.IP,
		Platform: v.Platform, OS: v.OS, App: v.App, Mode: v.Mode, Hostname: v.Hostname, Online: v.Online,
		FirstSeen: v.FirstSeen, LastSeen: v.LastSeen, ConnectedAt: v.ConnectedAt, Sessions: v.Sessions,
		RTTMs: v.RTTMs, RTTAvgMs: v.RTTAvgMs, JitterMs: v.JitterMs, LossPct: v.LossPct, Quality: v.Quality,
		BytesUp: v.BytesUp, BytesDown: v.BytesDown, Direct: v.Direct, Pending: v.Pending, Frozen: v.Frozen,
		Profiles: v.Profiles, ExitSet: v.ExitSet}
}

// ---------- отдать свою картину ----------

// buildView — живая картина этого сервера для других серверов сети.
func (n *ServerNet) buildView(ctx context.Context) srvView {
	n.mu.Lock()
	v := srvView{Net: n.f.Net, From: n.f.Self, T: n.now().Unix(), Links: []viewLink{}, Networks: []viewNet{}}
	for id := range n.serversLocked() {
		if id == n.f.Self {
			continue
		}
		l := viewLink{ID: id}
		if p := n.peers[id]; p != nil {
			l.Online, l.RTTMs = p.Online, p.RTT
			if !p.LastOK.IsZero() {
				l.LastSync = p.LastOK.Unix()
			}
		}
		v.Links = append(v.Links, l)
	}
	n.mu.Unlock()
	sort.Slice(v.Links, func(i, j int) bool { return v.Links[i].ID < v.Links[j].ID })
	v.Accts = n.activeAccts()
	v.Meshd = n.mesh.sys.UnitState(meshdUnit).Active
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ms, err := n.mesh.admin.State(cctx)
	if err != nil {
		v.Meshd = false
		return v
	}
	v.MeshIP, v.MeshHost, v.Version = ms.Self.IP, ms.Self.Host, ms.Version
	for _, l := range ms.Relays {
		if l.Dir == "out" {
			v.Relays = append(v.Relays, l)
		}
	}
	for _, nw := range ms.Networks {
		vn := viewNet{ID: nw.ID, Name: nw.Name, Created: nw.Created, EmptySince: nw.EmptySince, Approval: nw.Approval,
			Devices: make([]viewDevice, 0, len(nw.Devices))}
		for _, d := range nw.Devices {
			vn.Devices = append(vn.Devices, viewDeviceOf(d))
		}
		v.Networks = append(v.Networks, vn)
	}
	return v
}

// activeAccts — хеши действующих учётных записей этого сервера.
func (n *ServerNet) activeAccts() []string {
	out := []string{}
	if n.srv == nil {
		return out
	}
	for _, c := range n.srv.clients.List() {
		if c.State == StateActive {
			out = append(out, userHash(c.Username))
		}
	}
	sort.Strings(out)
	return out
}

// handleView — /srv/v1/view: картина этого сервера, только серверу сети с
// подписанным запросом.
func (n *ServerNet) handleView(w http.ResponseWriter, r *http.Request) {
	body, ok := readSyncBody(w, r)
	if !ok {
		return
	}
	from, ok := n.authPeer(w, r, body)
	if !ok {
		return
	}
	n.mu.Lock()
	n.markPeerLocked(from)
	n.mu.Unlock()
	writeJSON(w, http.StatusOK, n.buildView(r.Context()))
}

// ---------- взять чужую картину ----------

// pullView — взять картину сервера id. Старая панель там (нет такого
// запроса) — картины просто нет, обмен общими данными идёт как шёл.
func (n *ServerNet) pullView(ctx context.Context, id string, rec srvRec) {
	n.mu.Lock()
	netID := n.f.Net
	signer, err := n.keyLocked()
	n.pulled[id] = n.now()
	n.mu.Unlock()
	if netID == "" || err != nil {
		return
	}
	var v srvView
	if err := n.call(ctx, rec, signer, true, netID, "/srv/v1/view", struct{}{}, &v); err != nil {
		return
	}
	if v.Net != netID || v.From != id {
		return
	}
	cleanView(&v)
	n.mu.Lock()
	if _, live := n.serversLocked()[id]; live && n.f.Net == netID {
		n.views[id] = peerView{View: v, Got: n.now().Unix()}
		n.saveViewsLocked()
	}
	n.mu.Unlock()
}

// cleanView — чужая картина не больше разумного.
func cleanView(v *srvView) {
	left := maxViewDevices
	nets := v.Networks[:0]
	for _, nw := range v.Networks {
		if nw.ID == "" || left <= 0 {
			continue
		}
		if len(nw.Devices) > left {
			nw.Devices = nw.Devices[:left]
		}
		left -= len(nw.Devices)
		devs := nw.Devices[:0]
		for _, d := range nw.Devices {
			if d.ID != "" {
				devs = append(devs, d)
			}
		}
		nw.Devices = devs
		nets = append(nets, nw)
	}
	v.Networks = nets
	if len(v.Links) > maxServers {
		v.Links = v.Links[:maxServers]
	}
}

// RefreshViews — страницу открыли: взять свежие картины у всех серверов
// сети в фоне (у каждого не чаще раза в 5 секунд).
func (n *ServerNet) RefreshViews() {
	n.mu.Lock()
	if n.f.Net == "" {
		n.mu.Unlock()
		return
	}
	now := n.now()
	todo := map[string]srvRec{}
	for id, rec := range n.serversLocked() {
		if id == n.f.Self || now.Sub(n.pulled[id]) < viewPullMin {
			continue
		}
		n.pulled[id] = now
		todo[id] = rec
	}
	n.mu.Unlock()
	for id, rec := range todo {
		go n.pullView(context.Background(), id, rec)
	}
}

func (n *ServerNet) loadViews() {
	if n.viewPath == "" {
		return
	}
	data, err := os.ReadFile(n.viewPath)
	if err != nil {
		return
	}
	var f map[string]peerView
	if json.Unmarshal(data, &f) == nil && f != nil {
		n.views = f
	}
}

func (n *ServerNet) saveViewsLocked() {
	if n.viewPath == "" {
		return
	}
	data, err := json.Marshal(n.views)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(n.viewPath), 0o700)
	tmp := n.viewPath + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, n.viewPath)
	}
}

// ---------- имя сервера из панели ----------

// srvNameLocked — имя сервера id: выбранное в панели (sname:), иначе то,
// которым сервер называет себя сам.
func (n *ServerNet) srvNameLocked(id string, r srvRec) string {
	if v, ok := decodeItem[nameRec](n.f.Items[kSName+id]); ok && v.Name != "" {
		return v.Name
	}
	return r.Name
}

// noteServerName — имя сервера выбрали в панели этого сервера.
func (n *ServerNet) noteServerName(id, name string) { n.note(kSName+id, false, nameRec{Name: name}) }

// selfNameWanted — имя этого сервера, которое ему дали в сети (пусто — не
// давали).
func (n *ServerNet) selfNameWanted() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.f.Net == "" {
		return ""
	}
	if v, ok := decodeItem[nameRec](n.f.Items[kSName+n.f.Self]); ok {
		return v.Name
	}
	return ""
}

// SelfID — id этого сервера в сети серверов.
func (n *ServerNet) SelfID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.f.Self
}

// RenameServer — дать серверу сети id новое имя (newest wins на всех
// серверах). Свой — переименовывается сразу.
func (n *ServerNet) RenameServer(ctx context.Context, id, name string) error {
	name = cleanName(name)
	if name == "" {
		return errors.New("дай серверу имя")
	}
	n.mu.Lock()
	self := n.f.Self
	inNet := n.f.Net != ""
	_, known := n.serversLocked()[id]
	n.mu.Unlock()
	if id == "" || id == "main" {
		id = self
	}
	if id == self {
		if err := n.mesh.renameSelf(ctx, name); err != nil {
			return err
		}
		n.noteServerName(id, name)
		n.refreshSelf(ctx)
		return nil
	}
	if !inNet || !known {
		return errors.New("нет такого сервера в сети")
	}
	n.noteServerName(id, name)
	return nil
}

// applySelfName — в сети этому серверу дали другое имя: взять его.
func (n *ServerNet) applySelfName(ctx context.Context) {
	want := n.selfNameWanted()
	if want == "" || want == n.mesh.settings.Get().Mesh.Name {
		return
	}
	if err := n.mesh.renameSelf(ctx, want); err != nil {
		n.mesh.logf("сеть серверов: имя сервера «%s» не применилось: %v", want, err)
	}
}

// ---------- цепочка выхода через другой сервер ----------

type exitReq struct {
	Net    string   `json:"net"`
	Device string   `json:"device"`
	Entry  string   `json:"entry"`
	Via    []string `json:"via"`
}

// handleExit — /srv/v1/exit: другой сервер сети просит отдать цепочку
// выхода устройству, которое подключено сюда, а не к нему.
func (n *ServerNet) handleExit(w http.ResponseWriter, r *http.Request) {
	body, ok := readSyncBody(w, r)
	if !ok {
		return
	}
	if _, ok := n.authPeer(w, r, body); !ok {
		return
	}
	var req exitReq
	if err := json.Unmarshal(body, &req); err != nil || len(req.Entry) > 80 || len(req.Via) > 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверная цепочка выхода"})
		return
	}
	o, err := n.mesh.admin.SetExit(r.Context(), req.Net, req.Device, req.Entry, req.Via)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "exit": o})
}

// forwardExit — цепочку выхода устройству другого сервера отдаёт тот
// сервер (устройство держит управляющее соединение с ним).
func (n *ServerNet) forwardExit(ctx context.Context, id, netID, device, entry string, via []string) (meshsvc.ExitOrder, error) {
	n.mu.Lock()
	rec, ok := n.serversLocked()[id]
	snet := n.f.Net
	signer, err := n.keyLocked()
	n.mu.Unlock()
	if !ok || snet == "" {
		return meshsvc.ExitOrder{}, errors.New("нет такого сервера в сети")
	}
	if err != nil {
		return meshsvc.ExitOrder{}, err
	}
	if via == nil {
		via = []string{}
	}
	var out struct {
		Exit meshsvc.ExitOrder `json:"exit"`
	}
	err = n.call(ctx, rec, signer, true, snet, "/srv/v1/exit", exitReq{Net: netID, Device: device, Entry: entry, Via: via}, &out)
	if err != nil {
		var ce *callError
		if errors.As(err, &ce) && ce.Status == http.StatusNotFound {
			return meshsvc.ExitOrder{}, errors.New("на сервере «" + rec.Name + "» панель не обновлена — цепочку задайте там")
		}
		return meshsvc.ExitOrder{}, err
	}
	return out.Exit, nil
}

// ---------- где устройство другого сервера ----------

// deviceServer — через какой сервер сети достучаться до устройства, которого
// нет в meshd этого: тот, с которым оно на связи (свежая картина), иначе
// любой, у кого оно есть. "" — ни у кого.
func (n *ServerNet) deviceServer(netID, device string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now().Unix()
	any := ""
	ids := make([]string, 0, len(n.views))
	for id := range n.views {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	live := n.serversLocked()
	for _, id := range ids {
		if _, ok := live[id]; !ok || id == n.f.Self {
			continue
		}
		pv := n.views[id]
		for _, nw := range pv.View.Networks {
			if nw.ID != netID {
				continue
			}
			for _, d := range nw.Devices {
				if d.ID != device {
					continue
				}
				if d.Online && n.liveLocked(id, pv, now) {
					return id
				}
				if any == "" {
					any = id
				}
			}
		}
	}
	return any
}

// remoteHasNet — сеть есть в картине другого сервера сети.
func (n *ServerNet) remoteHasNet(netID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	live := n.serversLocked()
	for id, pv := range n.views {
		if _, ok := live[id]; !ok {
			continue
		}
		for _, nw := range pv.View.Networks {
			if nw.ID == netID {
				return true
			}
		}
	}
	return false
}

// liveLocked — картина сервера id свежая и сам он на связи.
func (n *ServerNet) liveLocked(id string, pv peerView, now int64) bool {
	p := n.peers[id]
	return p != nil && p.Online && now-pv.Got <= int64(viewFresh/time.Second)
}

// ---------- сложить картины ----------

// DevConn — связь устройства с одним сервером сети.
type DevConn struct {
	// Server — id из MeshState.Servers ("main" — этот сервер).
	Server      string  `json:"server"`
	Online      bool    `json:"online"`
	RTTMs       float64 `json:"rttMs,omitempty"`
	RTTAvgMs    float64 `json:"rttAvgMs,omitempty"`
	Quality     string  `json:"quality,omitempty"`
	BytesUp     int64   `json:"bytesUp,omitempty"`
	BytesDown   int64   `json:"bytesDown,omitempty"`
	ConnectedAt int64   `json:"connectedAt,omitempty"`
	// Age — сколько секунд этим данным (0 — свои, живые); Stale — сервер
	// не на связи, это его последняя картина.
	Age   int64 `json:"age,omitempty"`
	Stale bool  `json:"stale,omitempty"`
}

// SrvLinkState — линия между двумя серверами на схеме.
type SrvLinkState struct {
	A      string  `json:"a"`
	B      string  `json:"b"`
	Online bool    `json:"online"`
	RTTMs  float64 `json:"rttMs,omitempty"`
	// Age — сколько секунд сведениям о ней (0 — мерил этот сервер).
	Age int64 `json:"age,omitempty"`
}

// viewSrc — картина одного сервера для сложения.
type viewSrc struct {
	ID   string
	Live bool
	Age  int64
	View srvView
}

// mergeInto складывает живые картины серверов сети с картиной этого
// сервера в st (после fillMain).
func (n *ServerNet) mergeInto(st *MeshState) {
	users := map[string]string{}
	if n.srv != nil {
		for _, c := range n.srv.clients.List() {
			users[userHash(c.Username)] = c.Username
		}
	}
	for _, d := range st.Networks {
		for _, x := range d.Devices {
			if x.User != "" {
				users[userHash(x.User)] = x.User
			}
		}
	}
	// Учётные записи — до блокировки сети серверов: ClientManager сам
	// зовёт её при правке (noteClient).
	mine := map[string]bool{}
	for _, h := range n.activeAccts() {
		mine[h] = true
	}
	host := n.selfHost()
	n.mu.Lock()
	st.SelfSrv = n.f.Self
	now := n.now().Unix()
	items := n.snapshotLocked()
	var srcs []viewSrc
	var why []whySrv
	links := map[string]*SrvLinkState{}
	addLink := func(a, b string, online bool, rtt float64, age int64) {
		if a == b {
			return
		}
		if a > b && a != "main" || b == "main" {
			a, b = b, a
		}
		k := a + "|" + b
		l := links[k]
		if l == nil {
			links[k] = &SrvLinkState{A: a, B: b, Online: online, RTTMs: rtt, Age: age}
			return
		}
		// Мерили оба — берём свежее и ту, что на связи.
		if online && !l.Online || online == l.Online && age < l.Age {
			*l = SrvLinkState{A: a, B: b, Online: online, RTTMs: rtt, Age: age}
		}
	}
	if n.f.Net != "" {
		servers := n.serversLocked()
		why = append(why, whySrv{ID: "main", Host: host, Port: n.mesh.sshPort, Online: st.Meshd.Active,
			Meshd: st.Version, Accts: mine})
		ids := make([]string, 0, len(servers))
		for id := range servers {
			if id != n.f.Self {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool {
			if servers[ids[i]].Name != servers[ids[j]].Name {
				return servers[ids[i]].Name < servers[ids[j]].Name
			}
			return ids[i] < ids[j]
		})
		for _, id := range ids {
			r := servers[id]
			ms := MeshServer{ID: id, Name: r.Name, Host: r.Host, Known: true, Net: true}
			p := n.peers[id]
			if p != nil && p.Online && now-p.LastOK.Unix() <= int64(viewFresh/time.Second) {
				ms.Online, ms.RTTMs = true, p.RTT
			}
			pv, have := n.views[id]
			w := whySrv{ID: id, Host: r.Host, Port: r.Port, Online: ms.Online}
			if have {
				w.Meshd = pv.View.Version
				if pv.View.Accts != nil {
					w.Accts = map[string]bool{}
					for _, h := range pv.View.Accts {
						w.Accts[h] = true
					}
				}
			}
			why = append(why, w)
			switch {
			case !have:
				ms.NoView = true
			default:
				live := n.liveLocked(id, pv, now)
				ms.MeshIP, ms.MeshHost = pv.View.MeshIP, pv.View.MeshHost
				ms.Age, ms.Stale = now-pv.Got, !live
				if ms.Age < 0 {
					ms.Age = 0
				}
				srcs = append(srcs, viewSrc{ID: id, Live: live, Age: ms.Age, View: pv.View})
				for _, l := range pv.View.Links {
					if l.ID == n.f.Self {
						continue // связь с этим сервером мерим сами
					}
					if _, ok := servers[l.ID]; ok {
						addLink(id, l.ID, l.Online && live, l.RTTMs, ms.Age)
					}
				}
			}
			addLink("main", id, ms.Online, ms.RTTMs, 0)
			st.Servers = append(st.Servers, ms)
		}
	}
	n.mu.Unlock()
	// Серверы старого способа: линия к этому серверу, задержка — от meshd.
	for _, s := range st.Servers {
		if s.Main || s.Net {
			continue
		}
		rtt := 0.0
		if s.Link != nil {
			rtt = s.Link.RTTMs
		}
		addLink("main", s.ID, s.Online, rtt, 0)
	}
	st.ServerLinks = make([]SrvLinkState, 0, len(links))
	for _, l := range links {
		st.ServerLinks = append(st.ServerLinks, *l)
	}
	sort.Slice(st.ServerLinks, func(i, j int) bool {
		if st.ServerLinks[i].A != st.ServerLinks[j].A {
			return st.ServerLinks[i].A < st.ServerLinks[j].A
		}
		return st.ServerLinks[i].B < st.ServerLinks[j].B
	})
	st.Networks = mergeNetworks(st.Networks, srcs, items, users)
	st.Relays = mergeRelays(st, srcs)
	if len(why) > 1 {
		for i := range st.Networks {
			for j := range st.Networks[i].Devices {
				d := &st.Networks[i].Devices[j]
				d.Missing = missingServers(*d, why)
			}
		}
	}
}

// tombT — время надгробия (forget:, ndel:) в мс; 0 — нет его.
func tombT(items map[string]srvItem, key string) int64 {
	it, ok := items[key]
	if !ok || it.Del {
		return 0
	}
	return it.T
}

type devAcc struct {
	dev   MeshDevice
	local bool
	nameT int64
}

// mergeNetworks — правило сложения картин (ТЗ 24). local — картина этого
// сервера (как её дал meshd), srcs — картины других серверов сети.
func mergeNetworks(local []MeshNetwork, srcs []viewSrc, items map[string]srvItem, users map[string]string) []MeshNetwork {
	type netAcc struct {
		nw    MeshNetwork
		local bool
		devs  []*devAcc
		by    map[string]*devAcc
	}
	var order []*netAcc
	nets := map[string]*netAcc{}
	for _, nw := range local {
		na := &netAcc{nw: nw, local: true, by: map[string]*devAcc{}}
		na.nw.Devices = nil
		for _, d := range nw.Devices {
			a := &devAcc{dev: d, local: true, nameT: d.LastSeen}
			a.dev.Here = d.Online
			a.dev.Conns = []DevConn{{Server: d.Server, Online: d.Online, RTTMs: d.RTTMs, RTTAvgMs: d.RTTAvgMs,
				Quality: d.Quality, BytesUp: d.BytesUp, BytesDown: d.BytesDown, ConnectedAt: d.ConnectedAt}}
			na.devs = append(na.devs, a)
			na.by[d.ID] = a
		}
		nets[nw.ID] = na
		order = append(order, na)
	}
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].ID < srcs[j].ID })
	var remote []*netAcc
	for _, src := range srcs {
		for _, vn := range src.View.Networks {
			na := nets[vn.ID]
			if na == nil {
				// Сеть удалили в панели, а у сервера без связи она ещё есть.
				if t := tombT(items, kNDel+vn.ID); t > 0 && vn.Created*1000 <= t {
					continue
				}
				na = &netAcc{nw: MeshNetwork{ID: vn.ID, Name: vn.Name, Created: vn.Created, EmptySince: vn.EmptySince,
					Approval: vn.Approval}, by: map[string]*devAcc{}}
				nets[vn.ID] = na
				remote = append(remote, na)
			} else {
				if na.nw.Name == "" {
					na.nw.Name = vn.Name
				}
				if vn.Created > 0 && (na.nw.Created == 0 || vn.Created < na.nw.Created) {
					na.nw.Created = vn.Created
				}
			}
			for _, vd := range vn.Devices {
				// Убрали в панели — в старой картине оно ещё есть. Вернулось
				// после этого (meshd завёл его заново) — показываем.
				if t := tombT(items, devKey(kForget, vn.ID, vd.ID)); t > 0 && vd.FirstSeen*1000 <= t {
					continue
				}
				conn := DevConn{Server: src.ID, Online: vd.Online && src.Live, RTTMs: vd.RTTMs, RTTAvgMs: vd.RTTAvgMs,
					Quality: vd.Quality, BytesUp: vd.BytesUp, BytesDown: vd.BytesDown, ConnectedAt: vd.ConnectedAt,
					Age: src.Age, Stale: !src.Live}
				a := na.by[vd.ID]
				if a == nil {
					d := MeshDevice{Device: vd.device(), Server: src.ID, Src: src.ID, Age: src.Age}
					d.User = users[vd.UH]
					d.Online = conn.Online
					a = &devAcc{dev: d, nameT: vd.LastSeen}
					na.devs = append(na.devs, a)
					na.by[vd.ID] = a
				} else {
					if !a.local && conn.Online && !a.dev.Online {
						// Чужое устройство: основные сведения — от сервера,
						// с которым оно сейчас на связи.
						conns, nameT := a.dev.Conns, a.nameT
						d := MeshDevice{Device: vd.device(), Server: src.ID, Src: src.ID, Age: src.Age}
						d.User = users[vd.UH]
						d.Online = true
						a.dev = d
						a.dev.Conns, a.nameT = conns, nameT
					}
					if vd.LastSeen > a.nameT && vd.Name != "" {
						a.dev.Name, a.nameT = vd.Name, vd.LastSeen
						if !a.local {
							a.dev.Host = vd.Host
						}
					}
					a.dev.Pending = a.dev.Pending || vd.Pending
					a.dev.Frozen = a.dev.Frozen || vd.Frozen
					if a.dev.User == "" {
						a.dev.User = users[vd.UH]
					}
				}
				a.dev.Conns = append(a.dev.Conns, conn)
			}
		}
	}
	sort.SliceStable(remote, func(i, j int) bool {
		if remote[i].nw.Created != remote[j].nw.Created {
			return remote[i].nw.Created < remote[j].nw.Created
		}
		return remote[i].nw.ID < remote[j].nw.ID
	})
	order = append(order, remote...)

	out := make([]MeshNetwork, 0, len(order))
	for _, na := range order {
		nw := na.nw
		if r, ok := decodeItem[nameRec](items[kNName+nw.ID]); ok {
			nw.Name = r.Name
		}
		if r, ok := decodeItem[onRec](items[kAppr+nw.ID]); ok {
			nw.Approval = r.On
		}
		nw.Devices = make([]MeshDevice, 0, len(na.devs))
		for _, a := range na.devs {
			d := a.dev
			online, stale := false, true
			first := ""
			for _, c := range d.Conns {
				if c.Online {
					online = true
					if first == "" {
						first = c.Server
					}
				}
				if !c.Stale {
					stale = false
				}
			}
			d.Online = online
			if !a.local {
				if first != "" {
					d.Server = first
				}
				d.Stale = stale
			}
			if r, ok := decodeItem[aliasRec](items[devKey(kAlias, nw.ID, d.ID)]); ok {
				d.Alias = r.Alias
			}
			if d.Alias != "" {
				d.Display = d.Alias
			} else if d.Name != "" {
				d.Display = d.Name
			}
			if it, ok := items[devKey(kOK, nw.ID, d.ID)]; ok && !it.Del {
				d.Pending = false
			}
			if r, ok := decodeItem[onRec](items[devKey(kFrz, nw.ID, d.ID)]); ok {
				d.Frozen = r.On
			}
			nw.Devices = append(nw.Devices, d)
		}
		if len(nw.Devices) > 0 {
			nw.EmptySince = 0
		}
		out = append(out, nw)
	}
	return out
}

// hasDevice — есть ли устройство в сетях.
func hasDevice(nets []meshsvc.Network, netID, device string) (net, dev bool) {
	for _, nw := range nets {
		if nw.ID != netID {
			continue
		}
		for _, d := range nw.Devices {
			if d.ID == device {
				return true, true
			}
		}
		return true, false
	}
	return false, false
}

// trimName — для сравнений имён без пробелов по краям.
func trimName(s string) string { return strings.TrimSpace(s) }

// RelayView — пересланное соединение на схеме: устройство From (сервер
// A) → цель To (устройство или "srv:"+id сервера) через сервер B.
type RelayView struct {
	Net  string `json:"net"`
	From string `json:"from"`
	To   string `json:"to"`
	A    string `json:"a"`
	B    string `json:"b"`
	Port int    `json:"port,omitempty"`
}

// mergeRelays — пересланные соединения всех серверов в именах схемы.
func mergeRelays(st *MeshState, srcs []viewSrc) []RelayView {
	out := []RelayView{}
	srvByIP := map[string]string{}
	if st.Self != nil && st.Self.IP != "" {
		srvByIP[st.Self.IP] = "main"
	}
	for _, s := range st.Servers {
		if s.MeshIP != "" && !s.Main {
			srvByIP[s.MeshIP] = s.ID
		}
	}
	devByIP := map[string]string{}
	for _, nw := range st.Networks {
		for _, d := range nw.Devices {
			devByIP[nw.ID+"|"+d.IP] = d.ID
		}
	}
	ref := func(id string) string {
		if id == st.SelfSrv {
			return "main"
		}
		return id
	}
	add := func(a string, l meshsvc.RelayLine) {
		if l.Dir != "out" {
			return
		}
		to := devByIP[l.Net+"|"+l.To]
		if to == "" {
			if s := srvByIP[l.To]; s != "" {
				to = "srv:" + s
			}
		}
		if to == "" {
			return
		}
		out = append(out, RelayView{Net: l.Net, From: l.Dev, To: to, A: a, B: ref(l.Srv), Port: l.Port})
	}
	for _, l := range st.localRelays {
		add("main", l)
	}
	for _, s := range srcs {
		if !s.Live {
			continue
		}
		for _, l := range s.View.Relays {
			add(s.ID, l)
		}
	}
	return out
}
