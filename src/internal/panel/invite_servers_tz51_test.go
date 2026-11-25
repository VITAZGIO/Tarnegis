package panel

import (
	"os"
	"strings"
	"testing"

	"sshtunnel/internal/config"
)

// ТЗ 51: «Добавить устройство» сразу отмечает все серверы сети. Раньше
// отмечался только этот (lockSelf → !!s.self), приглашение уходило с
// servers=[этот], и телефон получал в профиле один сервер вместо всех.
func TestНовоеУстройствоСоВсемиСерверами(t *testing.T) {
	data, err := os.ReadFile("assets/sections/devices.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if strings.Contains(src, "lockSelf ? !!s.self") {
		t.Fatal("новое устройство снова получает только этот сервер")
	}
	if !strings.Contains(src, "cb.checked = (lockSelf && s.self) || !selected || selected.includes(s.id);") {
		t.Fatal("галочки серверов нового устройства не «все»")
	}
}

// ТЗ 51, сценарий телефона в памяти: сеть из трёх серверов, приглашение со
// всеми серверами (как теперь шлёт «Добавить устройство»). Телефон, который
// на v1.10.23 знал только сервер приглашения (профиль не прочитался), после
// сверки получает все три; удалённый у себя сервер следующая сверка (rev
// вырос) не возвращает.
func TestТелефонПослеОбновленияПолучаетВсеСерверы(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "A")
	b := tn.node(t, "b.example", "B")
	c := tn.node(t, "c.example", "C")
	b.join(t, a.invite(t))
	c.join(t, a.invite(t))
	settle(a, b, c)
	var all []string
	for _, s := range a.srv.devServers() {
		all = append(all, s.ID)
	}
	if len(all) != 3 {
		t.Fatalf("серверов в сети: %d", len(all))
	}
	res, err := a.srv.createInvite(inviteReq{Name: "Телефон", DeviceType: "android", Servers: all})
	if err != nil {
		t.Fatal(err)
	}
	r := joinAs(t, a.srv, tokOf(t, res.Text), newJoinKey(t), "198.51.100.7")
	settle(a, b, c)
	ca, _ := a.store.Get(r.ID)
	prof := a.srv.profileOf(ca)

	// Состояние телефона v1.10.23: только сервер приглашения, rev 0.
	cfg := config.Default()
	cfg.Profiles = nil
	cfg.SetDevice(config.Device{ID: r.ID, KeyPath: "/k"}, config.DeviceProfile{ID: r.ID, Mesh: true,
		Servers: []config.DeviceServer{{Name: "a.example", Host: "a.example", Port: 22}}})
	if len(cfg.Profiles) != 1 {
		t.Fatalf("до сверки: %+v", cfg.Profiles)
	}
	dp := config.DeviceProfile{ID: prof.ID, Name: prof.Name, Rev: prof.Rev, Mesh: prof.Mesh, MeshKey: prof.MeshKey}
	for _, s := range prof.Servers {
		dp.Servers = append(dp.Servers, config.DeviceServer{Name: s.Name, Host: s.Host, Port: s.Port, HostKey: s.HK})
	}
	if res := cfg.ApplyDevice(dp); !res.Changed || len(cfg.Profiles) != 3 {
		t.Fatalf("после сверки серверов %d: %+v", len(cfg.Profiles), dp.Servers)
	}
	// Удалить один у себя — следующая сверка (rev вырос) его не вернёт.
	gone := cfg.Profiles[0]
	cfg.RemoveProfile(gone.ID)
	dp.Rev++
	cfg.ApplyDevice(dp)
	for _, p := range cfg.Profiles {
		if p.Host == gone.Host {
			t.Fatalf("удалённый вернулся: %+v", cfg.Profiles)
		}
	}
	if len(cfg.Profiles) != 2 {
		t.Fatalf("%+v", cfg.Profiles)
	}
}
