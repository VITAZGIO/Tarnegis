package main

// Пересылка между серверами сети (meshd 7, ТЗ 24): два и три meshd в одном
// процессе, вместо панелей и SSH — мост в тесте, который делает то же, что
// панель: ждёт вызовов у A (/v1/relay-wait), передаёт их B (/v1/relay-in) и
// отвечает A (/v1/relay-out).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rawPost — POST на вход панели; 200 — дальше сырые байты.
func rawPost(t *testing.T, base, path string, body any) (net.Conn, int, string) {
	t.Helper()
	data, _ := json.Marshal(body)
	c, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(data))
	req.Write(c)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		c.Close()
		return nil, 0, err.Error()
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		c.Close()
		return nil, resp.StatusCode, e.Error
	}
	return &hijackConn{Conn: c, r: br}, 200, ""
}

type relaySide struct {
	s     *server
	addr  string // для устройств
	admin string // вход панели (http://…)
}

func relayServer(t *testing.T, name, ip string) *relaySide {
	t.Helper()
	s, _ := testServer(t)
	s.connUser = func(net.Conn) string { return "u" }
	s.self = selfConfig{Enabled: true, Name: name, ID: name, IP: ip}
	hs := httptest.NewServer(s.adminHandler())
	t.Cleanup(hs.Close)
	return &relaySide{s: s, addr: serve(t, s), admin: hs.URL}
}

// bridge — «панель» сервера a: вызовы уходят к серверам по id (srv).
func bridge(t *testing.T, a *relaySide, peers map[string]*relaySide) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(a.admin, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	req, _ := http.NewRequest(http.MethodGet, a.admin+"/v1/relay-wait", nil)
	req.Write(c)
	br := bufio.NewReader(c)
	if resp, err := http.ReadResponse(br, req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("relay-wait: %v", err)
	}
	go func() {
		for {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return
			}
			var ask relayAsk
			if json.Unmarshal(line, &ask) != nil || ask.Call == "" {
				continue
			}
			go func(ask relayAsk) {
				b := peers[ask.Srv]
				if b == nil {
					rawPost(t, a.admin, "/v1/relay-out", map[string]any{"call": ask.Call, "error": "нет связи с сервером"})
					return
				}
				far, code, text := rawPost(t, b.admin, "/v1/relay-in", ask)
				if code != 200 {
					rawPost(t, a.admin, "/v1/relay-out", map[string]any{"call": ask.Call, "error": text})
					return
				}
				near, code, _ := rawPost(t, a.admin, "/v1/relay-out", map[string]any{"call": ask.Call, "ok": true})
				if code != 200 {
					far.Close()
					return
				}
				go func() { io.Copy(far, near); far.Close() }()
				io.Copy(near, far)
				near.Close()
			}(ask)
		}
	}()
	waitFor(t, func() bool {
		a.s.mu.Lock()
		defer a.s.mu.Unlock()
		return a.s.relayW != nil
	})
}

// echoDevice — устройство на сервере side: принимает вызовы и отвечает
// эхом с приставкой.
func echoDevice(t *testing.T, side *relaySide, dev string) (*wireConn, msg) {
	t.Helper()
	w, wel := helloConn(t, side.addr, msg{Net: "k", Device: dev, Name: dev})
	go func() {
		for {
			w.c.SetReadDeadline(time.Now().Add(30 * time.Second))
			line, err := w.r.ReadBytes('\n')
			if err != nil {
				return
			}
			var m msg
			json.Unmarshal(line, &m)
			if m.Op != "incoming" {
				continue
			}
			c, err := net.Dial("tcp", side.addr)
			if err != nil {
				return
			}
			writeMsg(c, msg{Op: "accept", Net: "k", Device: dev, Call: m.Call, OK: true})
			go func() {
				br := bufio.NewReader(c)
				l, _ := br.ReadString('\n')
				c.Write([]byte(dev + ":" + l))
				c.Close()
			}()
		}
	}()
	return w, wel
}

func ask(t *testing.T, c net.Conn, text string) string {
	t.Helper()
	c.Write([]byte(text + "\n"))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(got)
}

func remoteOf(nid string, list ...remotePeer) map[string][]remotePeer {
	return map[string][]remotePeer{nid: list}
}

func TestПересылкаКУстройствуИСерверуДругогоСервера(t *testing.T) {
	de := relayServer(t, "de", "198.18.0.1")
	lon := relayServer(t, "lon", "198.18.0.2")
	echoDevice(t, de, "pc")
	_, w2 := echoDevice(t, lon, "phone")
	// Служба на самом London (его 127.0.0.1:порт).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				l, _ := bufio.NewReader(c).ReadString('\n')
				c.Write([]byte("london:" + l))
				c.Close()
			}()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	nid := netID("k")
	de.s.setRemote(remoteOf(nid,
		remotePeer{IP: w2.IP, Host: w2.Host, Name: "phone", User: "u", Srv: "lon", SrvName: "London", Online: true},
		remotePeer{IP: "198.18.0.2", Host: "london", Name: "London", Server: true, Srv: "lon", SrvName: "London", Online: true}))
	// Без панели, ждущей вызовов, чужих устройств в списке нет.
	de.s.mu.Lock()
	n := de.s.nets[nid]
	before := de.s.peersLocked(n, n.Devices["pc"])
	de.s.mu.Unlock()
	if hasPeer(before, w2.IP) {
		t.Fatal("чужое устройство в списке без панели")
	}
	bridge(t, de, map[string]*relaySide{"lon": lon})
	de.s.mu.Lock()
	after := de.s.peersLocked(n, n.Devices["pc"])
	de.s.mu.Unlock()
	found := false
	for _, p := range after {
		if p.IP == w2.IP && p.Relay == "London" {
			found = true
		}
	}
	if !found {
		t.Fatalf("телефона London нет в списке ПК: %+v", after)
	}

	c, m := dialFrom(t, de.addr, "pc", w2.IP, 7)
	if m.Op != "connected" {
		t.Fatalf("ПК → телефон London: %+v", m)
	}
	if got := ask(t, c.c, "привет"); got != "phone:привет" {
		t.Fatalf("ответ: %q", got)
	}
	// По имени тоже, и до самого London.
	c, m = dialFrom(t, de.addr, "pc", "london", port)
	if m.Op != "connected" {
		t.Fatalf("ПК → сервер London: %+v", m)
	}
	if got := ask(t, c.c, "x"); got != "london:x" {
		t.Fatalf("ответ сервера: %q", got)
	}
}

func TestПересылкаПравилоНаВтором(t *testing.T) {
	de := relayServer(t, "de", "198.18.0.1")
	lon := relayServer(t, "lon", "198.18.0.2")
	echoDevice(t, de, "pc")
	_, w2 := echoDevice(t, lon, "phone")
	nid := netID("k")
	de.s.setRemote(remoteOf(nid, remotePeer{IP: w2.IP, Host: w2.Host, User: "u", Srv: "lon", SrvName: "London", Online: true}))
	bridge(t, de, map[string]*relaySide{"lon": lon})
	// На London правила не пускают «u» к «u» на порт 7 (только 22).
	lon.s.mu.Lock()
	lon.s.policy = policy{Enabled: true, Users: map[string][]string{"u": {"g"}}, Rules: []policyRule{{From: "g", To: "g", Ports: "22"}}}
	lon.s.mu.Unlock()
	_, m := dialFrom(t, de.addr, "pc", w2.IP, 7)
	if m.Op != "error" || !strings.Contains(m.Error, "нет доступа") {
		t.Fatalf("правило London не сработало: %+v", m)
	}
	if !strings.Contains(eventTypes(lon.s), "denied") {
		t.Fatal("отказ не в журнале London")
	}
	// И на этом сервере (правила общие): отказ до пересылки.
	de.s.mu.Lock()
	de.s.policy = lon.s.policy
	de.s.mu.Unlock()
	_, m = dialFrom(t, de.addr, "pc", w2.IP, 7)
	if m.Op != "error" || !strings.Contains(m.Error, "нет доступа") {
		t.Fatalf("правило Германии: %+v", m)
	}
}

func TestПересылкаСерверНеНаСвязи(t *testing.T) {
	de := relayServer(t, "de", "198.18.0.1")
	echoDevice(t, de, "pc")
	nid := netID("k")
	de.s.setRemote(remoteOf(nid, remotePeer{IP: "198.19.5.5", Host: "phone", User: "u", Srv: "lon", SrvName: "London", Online: true}))
	bridge(t, de, map[string]*relaySide{}) // London недоступен
	start := time.Now()
	_, m := dialFrom(t, de.addr, "pc", "198.19.5.5", 7)
	if m.Op != "error" || m.Error == "" {
		t.Fatalf("без London: %+v", m)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("отказ шёл слишком долго")
	}
}

func TestПересылкаБезПетли(t *testing.T) {
	// Три сервера: телефон только на C. A думает, что он на B (старая
	// картина); B дальше не пересылает — отказ, а не круг.
	a := relayServer(t, "a", "198.18.0.1")
	b := relayServer(t, "b", "198.18.0.2")
	c := relayServer(t, "c", "198.18.0.3")
	echoDevice(t, a, "pc")
	echoDevice(t, b, "tv") // сеть есть и на B
	_, w3 := echoDevice(t, c, "phone")
	nid := netID("k")
	a.s.setRemote(remoteOf(nid, remotePeer{IP: w3.IP, Host: w3.Host, User: "u", Srv: "b", SrvName: "B", Online: true}))
	// У B тоже есть сведения о C и мост — вход relay-in их не использует.
	b.s.setRemote(remoteOf(nid, remotePeer{IP: w3.IP, Host: w3.Host, User: "u", Srv: "c", SrvName: "C", Online: true}))
	bridge(t, b, map[string]*relaySide{"c": c})
	bridge(t, a, map[string]*relaySide{"b": b})
	_, m := dialFrom(t, a.addr, "pc", w3.IP, 7)
	if m.Op != "error" || !strings.Contains(m.Error, "нет такого устройства") {
		t.Fatalf("переслано дальше одного перехода: %+v", m)
	}
}

func TestПересылкаВСостоянииДляПанели(t *testing.T) {
	de := relayServer(t, "de", "198.18.0.1")
	if de.s.adminState().Relay {
		t.Fatal("панель ещё не ждёт, а Relay=true")
	}
	bridge(t, de, nil)
	if !de.s.adminState().Relay {
		t.Fatal("панель ждёт, а Relay=false")
	}
	if code, _ := admin(t, de.s, http.MethodPost, "/v1/remote", `{"nets":{"x":[{"ip":"nope","host":"h"}]}}`); code != 200 {
		t.Fatalf("неверный адрес должен просто отброситься: %d", code)
	}
}

// К самому серверу London — и тогда, когда у него нет ни одного
// устройства (своей сети нет): так устройства Германии доходят до нового
// сервера сети, не заводя его профиль.
func TestПересылкаКПустомуСерверу(t *testing.T) {
	de := relayServer(t, "de", "198.18.0.1")
	lon := relayServer(t, "lon", "198.18.0.2")
	echoDevice(t, de, "pc")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				l, _ := bufio.NewReader(c).ReadString('\n')
				c.Write([]byte("london:" + l))
				c.Close()
			}()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	nid := netID("k")
	lon.s.mu.Lock()
	_, has := lon.s.nets[nid]
	lon.s.mu.Unlock()
	if has {
		t.Fatal("у London в этой проверке не должно быть сети")
	}
	de.s.setRemote(remoteOf(nid,
		remotePeer{IP: "198.18.0.2", Host: "london", Name: "London", Server: true, Srv: "lon", SrvName: "London", Online: true}))
	bridge(t, de, map[string]*relaySide{"lon": lon})
	c, m := dialFrom(t, de.addr, "pc", "london", port)
	if m.Op != "connected" {
		t.Fatalf("ПК → пустой сервер London: %+v", m)
	}
	if got := ask(t, c.c, "x"); got != "london:x" {
		t.Fatalf("ответ сервера: %q", got)
	}
	// А к устройству, которого у London нет, — по-прежнему отказ.
	_, code, text := rawPost(t, lon.admin, "/v1/relay-in", relayAsk{Net: nid, To: "198.19.0.77", Port: 7, Srv: "de",
		From: relayFrom{IP: "198.19.0.2", Host: "pc", User: "u"}})
	if code == 200 || !strings.Contains(text, "нет такой сети") {
		t.Fatalf("устройство пустого сервера: %d %q", code, text)
	}
}

// На Германии осталась запись сервера London от старого способа (не на
// связи, адрес 198.19.255.253). Она не должна прятать London из сети
// серверов (на связи, другой адрес): так у Виталия London был серым.
func TestСтараяЗаписьСервераНеПрячетНастоящий(t *testing.T) {
	de := relayServer(t, "de", "198.18.0.1")
	lon := relayServer(t, "lon", "198.18.0.2")
	echoDevice(t, de, "pc")
	nid := netID("k")
	de.s.mu.Lock()
	if de.s.relayRecs == nil {
		de.s.relayRecs = map[string]*relayRecord{}
	}
	de.s.relayRecs["old-london"] = &relayRecord{Name: "London", IP: "198.19.255.253", LastSeen: time.Now().Add(-72 * time.Hour)}
	de.s.mu.Unlock()
	de.s.setRemote(remoteOf(nid,
		remotePeer{IP: "198.19.255.31", Host: "london", Name: "London", Server: true, Srv: "lon", SrvName: "London", Online: true}))
	bridge(t, de, map[string]*relaySide{"lon": lon})
	de.s.mu.Lock()
	n := de.s.nets[nid]
	peers := de.s.peersLocked(n, n.Devices["pc"])
	de.s.mu.Unlock()
	londons := 0
	for _, p := range peers {
		if p.Host == "london" || p.Name == "London" {
			londons++
			if !p.Online || p.IP != "198.19.255.31" {
				t.Fatalf("London не тот: %+v", p)
			}
		}
	}
	if londons != 1 {
		t.Fatalf("London в списке %d раз: %+v", londons, peers)
	}
}
