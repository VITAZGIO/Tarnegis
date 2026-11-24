package mesh

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeUpdMeshd — крошечный meshd на проводе: welcome с версией сети и роль,
// ответ на netupdate; что прислало устройство — в hellos / upds.
type fakeUpdMeshd struct {
	t       *testing.T
	addr    string
	welcome wire
	answer  func(wire) wire // ответ на netupdate (nil — не отвечать)

	mu     sync.Mutex
	hellos []wire
	upds   []wire
	conns  []net.Conn
}

func startFakeUpdMeshd(t *testing.T, welcome wire) *fakeUpdMeshd {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeUpdMeshd{t: t, addr: ln.Addr().String(), welcome: welcome}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeUpdMeshd) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	var hello wire
	if readLine(r, &hello) != nil {
		return
	}
	f.mu.Lock()
	f.hellos = append(f.hellos, hello)
	f.conns = append(f.conns, c)
	w := f.welcome
	f.mu.Unlock()
	w.Op, w.IP, w.Host = "welcome", "198.19.0.5", "pc"
	if w.Server == nil {
		w.Server = &wireServer{ID: "srv1", Name: "Лондон"}
	}
	send(c, w)
	for {
		var m wire
		if readLine(r, &m) != nil {
			return
		}
		switch m.Op {
		case "ping":
			send(c, wire{Op: "pong", T: m.T})
		case "upd":
			f.mu.Lock()
			f.upds = append(f.upds, m)
			f.mu.Unlock()
		case "netupdate":
			if f.answer != nil {
				a := f.answer(m)
				a.Op, a.Call = "netupdate", m.Call
				send(c, a)
			}
		}
	}
}

// push — сообщение всем устройствам на связи.
func (f *fakeUpdMeshd) push(m wire) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		send(c, m)
	}
}

func (f *fakeUpdMeshd) lastHello() wire {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.hellos) == 0 {
		return wire{}
	}
	return f.hellos[len(f.hellos)-1]
}

// updHost — что видит хозяин клиента: версии сети и автообновление.
type updHost struct {
	mu   sync.Mutex
	on   bool
	t    int64
	vers []NetVersion
	auto []bool
}

func (h *updHost) cfg(c *Config) {
	c.Build = "vpn"
	c.AutoUpdate = func() (bool, int64) { h.mu.Lock(); defer h.mu.Unlock(); return h.on, h.t }
	c.UpdateStatus = func() string { return "" }
	c.OnNetVersion = func(v NetVersion) { h.mu.Lock(); h.vers = append(h.vers, v); h.mu.Unlock() }
	c.OnAutoUpdate = func(on bool, t int64) {
		h.mu.Lock()
		h.auto = append(h.auto, on)
		h.on, h.t = on, t
		h.mu.Unlock()
	}
}

func (h *updHost) last() (NetVersion, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.vers) == 0 {
		return NetVersion{}, 0
	}
	return h.vers[len(h.vers)-1], len(h.vers)
}

func TestВерсияИАвтообновлениеВHello(t *testing.T) {
	f := startFakeUpdMeshd(t, wire{NetVer: "1.10.7", Upd: true, Mine: true})
	h := &updHost{on: true, t: 50}
	cfg := Config{Key: "k", Name: "pc", AppVersion: "v1.10.6", Platform: "windows", Mode: "vpn", NoNATProbe: true, NoDirect: true}
	h.cfg(&cfg)
	c := startClient(t, f.addr, cfg)
	hello := f.lastHello()
	if hello.App != "v1.10.6" || hello.Platform != "windows" || hello.Build != "vpn" || hello.Au != "on" || hello.AuT != 50 {
		t.Fatalf("hello: %+v", hello)
	}
	waitFor(t, func() bool { v, n := h.last(); return n > 0 && v.Version == "1.10.7" && v.Update && v.Mine }, "версия сети не дошла")
	if st := c.Status(); st.NetVersion != "1.10.7" || !st.Mine {
		t.Errorf("Status: %q %v", st.NetVersion, st.Mine)
	}
	// Человек выключил — серверу уходит op upd.
	h.mu.Lock()
	h.on, h.t = false, 60
	h.mu.Unlock()
	c.ReportUpdate()
	waitFor(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.upds) == 1 && f.upds[0].Au == "off" && f.upds[0].AuT == 60
	}, "op upd не дошёл")
}

func TestКомандаИАвтообновлениеОтПанели(t *testing.T) {
	f := startFakeUpdMeshd(t, wire{NetVer: "1.10.6"})
	h := &updHost{on: true, t: 10}
	cfg := Config{Key: "k", Name: "pc", AppVersion: "1.10.6", NoNATProbe: true, NoDirect: true}
	h.cfg(&cfg)
	startClient(t, f.addr, cfg)
	waitFor(t, func() bool { _, n := h.last(); return n == 1 }, "welcome без версии сети")
	f.push(wire{Op: "netver", NetVer: "1.10.7", Upd: true, Force: true})
	waitFor(t, func() bool { v, _ := h.last(); return v.Version == "1.10.7" && v.Update && v.Force }, "команда не дошла")
	// Панель выключила автообновление.
	f.push(wire{Op: "autoupd", Au: "off", AuT: 99})
	waitFor(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return !h.on && h.t == 99 }, "автообновление из панели")
	// В welcome — правка старше своей: не берём.
	h.mu.Lock()
	n := len(h.auto)
	h.mu.Unlock()
	f2 := startFakeUpdMeshd(t, wire{NetVer: "1.10.7", Au: "on", AuT: 5})
	cfg2 := cfg
	cfg2.Addr = ""
	startClient(t, f2.addr, cfg2)
	time.Sleep(100 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.auto) != n || h.on {
		t.Errorf("старая правка панели перебила свою: %v", h.auto)
	}
}

func TestОбновитьСетьПросьба(t *testing.T) {
	f := startFakeUpdMeshd(t, wire{NetVer: "1.10.6", Mine: true})
	f.answer = func(m wire) wire {
		if m.NetVer == "1.10.7" {
			return wire{OK: true}
		}
		return wire{Error: "это не релиз проекта"}
	}
	h := &updHost{on: true, t: 1}
	cfg := Config{Key: "k", Name: "pc", AppVersion: "1.10.6", NoNATProbe: true, NoDirect: true}
	h.cfg(&cfg)
	c := startClient(t, f.addr, cfg)
	waitFor(t, func() bool { _, mine := c.NetInfo(); return mine }, "роль не дошла")
	if err := c.RequestNetUpdate(context.Background(), "v1.10.7"); err != nil {
		t.Fatalf("принятая просьба: %v", err)
	}
	if err := c.RequestNetUpdate(context.Background(), "9.0.0"); err == nil || !strings.Contains(err.Error(), "не релиз") {
		t.Fatalf("отказ панели: %v", err)
	}
}

func TestГостьИСтарыйMeshdНеПросят(t *testing.T) {
	// Старый meshd: ни версии сети, ни роли — просьба не уходит вовсе.
	f := startFakeUpdMeshd(t, wire{})
	h := &updHost{on: true, t: 1}
	cfg := Config{Key: "k", Name: "pc", AppVersion: "1.10.6", NoNATProbe: true, NoDirect: true}
	h.cfg(&cfg)
	c := startClient(t, f.addr, cfg)
	if err := c.RequestNetUpdate(context.Background(), "1.10.7"); err != ErrNetUpdateUnsupported {
		t.Fatalf("старый meshd: %v", err)
	}
	if _, n := h.last(); n != 0 {
		t.Error("версия сети от старого meshd")
	}
	// Гость на новом meshd.
	g := startFakeUpdMeshd(t, wire{NetVer: "1.10.6"})
	c2 := startClient(t, g.addr, cfg)
	if err := c2.RequestNetUpdate(context.Background(), "1.10.7"); err != ErrNetUpdateUnsupported {
		t.Fatalf("гость: %v", err)
	}
}

func TestБезАвтообновленияСтарыйHello(t *testing.T) {
	// Хозяин не участвует в обновлении сети (AutoUpdate nil) — в hello нет
	// новых полей, версия сети не передаётся.
	f := startFakeUpdMeshd(t, wire{NetVer: "1.10.7", Upd: true})
	got := false
	cfg := Config{Key: "k", Name: "pc", NoNATProbe: true, NoDirect: true,
		OnNetVersion: func(NetVersion) { got = true }}
	startClient(t, f.addr, cfg)
	b, _ := json.Marshal(f.lastHello())
	if bytes.Contains(b, []byte(`"au"`)) {
		t.Errorf("hello: %s", b)
	}
	time.Sleep(100 * time.Millisecond)
	if got {
		t.Error("версия сети без участия в обновлении")
	}
}

// TestНастоящийMeshdВерсияСети — новый клиент и настоящий meshd: панель
// задаёт версию сети, устройство с автообновлением получает команду.
func TestНастоящийMeshdВерсияСети(t *testing.T) {
	addr, sock := startMeshdAdmin(t)
	h := &updHost{on: true, t: 1}
	cfg := Config{Key: NewKey(), Name: "pc", AppVersion: "1.10.6", NoNATProbe: true, NoDirect: true}
	h.cfg(&cfg)
	startClient(t, addr, cfg)
	hc := adminClient(sock)
	resp, err := hc.Post("http://meshd/v1/netver", "application/json", strings.NewReader(`{"version":"1.10.7"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFor(t, func() bool { v, _ := h.last(); return v.Version == "1.10.7" && v.Update }, "команда от настоящего meshd")
}
