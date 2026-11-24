package main

import (
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// updServer — meshd с правами: tun_pc — «Моё устройство», tun_guest — гость.
// user задаёт, под каким SSH-пользователем придёт следующее соединение.
func updServer(t *testing.T) (*server, string, func(string)) {
	t.Helper()
	s := newServer("")
	var mu sync.Mutex
	cur := "tun_pc"
	s.connUser = func(net.Conn) string {
		mu.Lock()
		defer mu.Unlock()
		return cur
	}
	s.policy = policy{Users: map[string][]string{"tun_pc": {mineGroup}, "tun_guest": {"Гости"}}}
	return s, serve(t, s), func(u string) { mu.Lock(); cur = u; mu.Unlock() }
}

func devByID(t *testing.T, s *server, id string) *device {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.nets {
		if d := n.Devices[id]; d != nil {
			return d
		}
	}
	t.Fatalf("нет устройства %s", id)
	return nil
}

func TestВерсияИАвтообновлениеДоходятДоСервера(t *testing.T) {
	s, addr, _ := updServer(t)
	_, w := helloConn(t, addr, msg{Net: "k", Device: "pc", Name: "ПК", App: "v1.10.5", Platform: "windows",
		Mode: "vpn", Build: "vpn", Au: "on", AuT: 100})
	if w.Mine != true || w.Au != "on" || w.AuT != 100 {
		t.Fatalf("welcome: %+v", w)
	}
	st := s.adminState()
	d := st.Networks[0].Devices[0]
	if d.App != "v1.10.5" || d.Platform != "windows" || d.Build != "vpn" || d.AutoUpd != "on" || d.AutoUpdT != 100 || !d.Mine {
		t.Fatalf("в панели: %+v", d)
	}
}

func TestВерсияСетиКомандаТолькоСАвтообновлением(t *testing.T) {
	s, addr, _ := updServer(t)
	on, w1 := helloConn(t, addr, msg{Net: "k", Device: "a", Name: "a", App: "1.10.5", Au: "on", AuT: 1})
	off, w2 := helloConn(t, addr, msg{Net: "k", Device: "b", Name: "b", App: "1.10.5", Au: "off", AuT: 1})
	if w1.NetVer != "" || w1.Upd || w2.Upd {
		t.Fatalf("версии сети ещё нет: %+v %+v", w1, w2)
	}
	if err := s.setNetVer("v1.10.7"); err != nil {
		t.Fatal(err)
	}
	m1 := on.next(t, "netver")
	m2 := off.next(t, "netver")
	if m1.NetVer != "1.10.7" || !m1.Upd || m1.Force {
		t.Errorf("с автообновлением: %+v", m1)
	}
	if m2.NetVer != "1.10.7" || m2.Upd {
		t.Errorf("без автообновления команды быть не должно: %+v", m2)
	}
	// «Обновить» в панели — и без автообновления.
	if err := s.devUpdate(netID("k"), "b"); err != nil {
		t.Fatal(err)
	}
	if m := off.next(t, "netver"); !m.Upd || !m.Force {
		t.Errorf("кнопка в панели: %+v", m)
	}
	// Устройство не старше — кнопка отказывает.
	if _, err := s.setAutoUpd(netID("k"), "a", true); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.nets[netID("k")].Devices["a"].App = "1.10.7"
	s.mu.Unlock()
	if err := s.devUpdate(netID("k"), "a"); err == nil {
		t.Error("обновление устройства, которое не старше сети")
	}
}

func TestНеНаСвязиОбновитсяПриПодключении(t *testing.T) {
	s, addr, _ := updServer(t)
	if err := s.setNetVer("1.10.7"); err != nil {
		t.Fatal(err)
	}
	_, w := helloConn(t, addr, msg{Net: "k", Device: "pc", Name: "pc", App: "1.10.6", Au: "on", AuT: 1})
	if w.NetVer != "1.10.7" || !w.Upd {
		t.Fatalf("welcome после возвращения: %+v", w)
	}
	// Новее сети — ничего не делать (вниз не обновляемся).
	_, w = helloConn(t, addr, msg{Net: "k", Device: "tv", Name: "tv", App: "1.11.0", Au: "on", AuT: 1})
	if w.Upd {
		t.Fatalf("команда устройству новее сети: %+v", w)
	}
}

func TestСтарыйКлиентБезНовыхПолей(t *testing.T) {
	s, addr, _ := updServer(t)
	s.setNetVer("1.10.7")
	c, w := helloConn(t, addr, msg{Net: "k", Device: "old", Name: "old", App: "1.9.0"})
	if w.Op != "welcome" || w.IP == "" || w.Upd {
		t.Fatalf("старый клиент: %+v", w)
	}
	// Связь живёт: на ping отвечает.
	writeMsg(c.c, msg{Op: "ping", T: 1})
	if m := c.next(t, "pong"); m.T != 1 {
		t.Fatalf("pong: %+v", m)
	}
	if d := devByID(t, s, "old"); d.AutoUpd != "" {
		t.Errorf("автообновление выдумано: %q", d.AutoUpd)
	}
}

func TestАвтообновлениеПоследняяПравкаПобеждает(t *testing.T) {
	s, addr, _ := updServer(t)
	c, _ := helloConn(t, addr, msg{Net: "k", Device: "pc", Name: "pc", App: "1.10.5", Au: "on", AuT: time.Now().Unix()})
	t1, err := s.setAutoUpd(netID("k"), "pc", false)
	if err != nil {
		t.Fatal(err)
	}
	if m := c.next(t, "autoupd"); m.Au != "off" || m.AuT != t1 {
		t.Fatalf("панель выключила: %+v", m)
	}
	c.c.Close()
	// Устройство подключается со старой правкой — побеждает панель.
	_, w := helloConn(t, addr, msg{Net: "k", Device: "pc", Name: "pc", App: "1.10.5", Au: "on", AuT: t1 - 5})
	if w.Au != "off" || w.AuT != t1 {
		t.Fatalf("старая правка устройства победила: %+v", w)
	}
	// Человек включил на устройстве позже — побеждает устройство.
	c2, _ := helloConn(t, addr, msg{Net: "k", Device: "pc", Name: "pc", App: "1.10.5", Au: "off", AuT: t1})
	writeMsg(c2.c, msg{Op: "upd", Au: "on", AuT: t1 + 10, UpdSt: "ошибка: нет сети"})
	waitFor(t, func() bool {
		d := devByID(t, s, "pc")
		s.mu.Lock()
		defer s.mu.Unlock()
		return d.AutoUpd == "on" && d.AutoUpdT == t1+10 && d.UpdSt == "ошибка: нет сети"
	})
}

func TestОбновитьСетьТолькоМоёУстройство(t *testing.T) {
	s, addr, as := updServer(t)
	s.setNetVer("1.10.6")
	pc, _ := helloConn(t, addr, msg{Net: "k", Device: "pc", Name: "pc", App: "1.10.6"})
	as("tun_guest")
	guest, wg := helloConn(t, addr, msg{Net: "k", Device: "g", Name: "g", App: "1.10.6"})
	if wg.Mine {
		t.Fatal("гость с ролью «Моё устройство»")
	}

	writeMsg(guest.c, msg{Op: "netupdate", Call: "g1", NetVer: "1.10.7"})
	if m := guest.next(t, "netupdate"); m.Call != "g1" || m.OK || m.Error == "" {
		t.Fatalf("гостю не отказали: %+v", m)
	}
	writeMsg(pc.c, msg{Op: "netupdate", Call: "p0", NetVer: "1.10.6"})
	if m := pc.next(t, "netupdate"); m.OK || m.Error == "" {
		t.Fatalf("не новее сети — принято: %+v", m)
	}
	writeMsg(pc.c, msg{Op: "netupdate", Call: "p1", NetVer: "v1.10.7"})
	var got []netReq
	waitFor(t, func() bool {
		got = append(got, s.takeNetReqs(nil)...)
		return len(got) > 0
	})
	if len(got) != 1 || got[0].Call != "p1" || got[0].Version != "1.10.7" || got[0].User != "tun_pc" || got[0].Device != "pc" {
		t.Fatalf("просьбы для панели: %+v", got)
	}
	s.takeNetReqs([]netAnswer{{Call: "p1"}})
	if m := pc.next(t, "netupdate"); m.Call != "p1" || !m.OK {
		t.Fatalf("ответ панели: %+v", m)
	}
	// Отказ панели доходит текстом.
	writeMsg(pc.c, msg{Op: "netupdate", Call: "p2", NetVer: "9.0.0"})
	waitFor(t, func() bool { return len(s.takeNetReqs(nil)) == 1 })
	s.takeNetReqs([]netAnswer{{Call: "p2", Error: "это не релиз проекта"}})
	if m := pc.next(t, "netupdate"); m.OK || m.Error != "это не релиз проекта" {
		t.Fatalf("отказ панели: %+v", m)
	}
}

func TestAdminHTTPОбновлениеСети(t *testing.T) {
	s, addr, _ := updServer(t)
	c, _ := helloConn(t, addr, msg{Net: "k", Device: "pc", Name: "pc", App: "1.10.5", Au: "on", AuT: 1})
	if code, out := admin(t, s, http.MethodPost, "/v1/netver", `{"version":"ой"}`); code != 400 {
		t.Errorf("кривая версия: %d %v", code, out)
	}
	if code, out := admin(t, s, http.MethodPost, "/v1/netver", `{"version":"1.10.7"}`); code != 200 || out["ok"] != true {
		t.Fatalf("netver: %d %v", code, out)
	}
	c.next(t, "netver")
	if _, out := admin(t, s, http.MethodGet, "/v1/netver", ""); out["netVer"] != "1.10.7" {
		t.Errorf("GET netver: %v", out)
	}
	if _, out := admin(t, s, http.MethodGet, "/v1/state", ""); out["netVer"] != "1.10.7" {
		t.Errorf("state.netVer: %v", out["netVer"])
	}
	if code, out := admin(t, s, http.MethodPost, "/v1/autoupd", `{"net":"`+netID("k")+`","device":"pc","on":false}`); code != 200 || out["t"] == nil {
		t.Fatalf("autoupd: %d %v", code, out)
	}
	if code, _ := admin(t, s, http.MethodPost, "/v1/devupdate", `{"net":"`+netID("k")+`","device":"нет"}`); code != 400 {
		t.Errorf("devupdate чужого: %d", code)
	}
	if code, out := admin(t, s, http.MethodPost, "/v1/devupdate", `{"net":"`+netID("k")+`","device":"pc"}`); code != 200 {
		t.Errorf("devupdate: %d %v", code, out)
	}
	if code, out := admin(t, s, http.MethodPost, "/v1/netupdate", `{"answers":[]}`); code != 200 || out["requests"] == nil {
		t.Errorf("netupdate: %d %v", code, out)
	}
}

func TestВерсииСравниваются(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{{"1.9.9", "1.10.0", true}, {"v1.10.7", "1.10.7", false}, {"dev", "1.10.7", false}, {"1.10", "1.10.1", true}} {
		if verLess(c.a, c.b) != c.less {
			t.Errorf("verLess(%q,%q)", c.a, c.b)
		}
	}
	if cleanVer("v1.10.7") != "1.10.7" || cleanVer("1.10.7;rm") != "" || cleanVer("") != "" {
		t.Error("cleanVer")
	}
}
