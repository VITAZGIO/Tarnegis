package mobile

// ТЗ 33: телефон добавляет серверы сети, а в журнале видно, что прислал
// сервер и почему какой-то сервер не добавлен.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"sshtunnel/internal/mesh"
)

type logCB struct {
	Callbacks
	mu    sync.Mutex
	lines []string
}

func (c *logCB) OnLog(s string) {
	c.mu.Lock()
	c.lines = append(c.lines, s)
	c.mu.Unlock()
}

func (c *logCB) all() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

func TestApplyNetServersЖурнал(t *testing.T) {
	tun := NewTunnel()
	tun.cfg.KnownHostsPath = filepath.Join(t.TempDir(), "known_hosts")
	cb := &logCB{}
	tun.mu.Lock()
	tun.cb = cb
	tun.mu.Unlock()

	// Телефон подключён к Франкфурту; Лондон и Амстердам — с учётной
	// записью, Париж — без, Осло — с адресом .mesh.
	ns := mesh.NetServers{Nets: []string{"N"}, Servers: []mesh.NetServer{
		{ID: "lon", Name: "London", Host: "london.example", Net: "N", Acct: true, Via: "fra.example"},
		{ID: "ams", Name: "Amsterdam", Host: "ams.example", Net: "N", Acct: true, Via: "fra.example"},
		{ID: "par", Name: "Paris", Host: "par.example", Net: "N", Via: "fra.example"},
		{ID: "osl", Name: "Oslo", Host: "oslo.mesh", Net: "N", Acct: true, Via: "fra.example"},
	}}
	tun.onNetServers(ns)
	profiles := profilesJSON(t, srv("fra", "fra.example", map[string]any{"meshEnabled": true, "meshKey": "K"}))
	out, err := tun.ApplyNetServers(profiles, "[]")
	if err != nil || out == "" {
		t.Fatalf("ApplyNetServers: %q %v", out, err)
	}
	var res struct {
		Profiles []struct {
			ID, Host, MeshKey string
			MeshEnabled       bool
		}
		Added []string
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Added, ",") != "London,Amsterdam" {
		t.Fatalf("added %v", res.Added)
	}
	// Активный — по-прежнему Франкфурт (первый), новые — в сети с тем же ключом.
	if res.Profiles[0].ID != "fra" {
		t.Fatalf("порядок профилей: %+v", res.Profiles)
	}
	for _, p := range res.Profiles[1:] {
		if !p.MeshEnabled || p.MeshKey != "K" {
			t.Fatalf("новый профиль не в сети: %+v", p)
		}
	}
	log := cb.all()
	for _, want := range []string{
		"сервер прислал список — London, Amsterdam, Paris (нет учётной записи), Oslo; с учётной записью телефона: 3",
		"Paris не добавлен — учётной записи телефона на нём нет",
		"Oslo не добавлен — у сервера нет адреса",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("в журнале нет %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "London не добавлен") {
		t.Fatalf("добавленный записан пропущенным:\n%s", log)
	}
	// Тот же список при переподключении — журнал не повторяется.
	n := len(cb.lines)
	tun.onNetServers(ns)
	tun.ApplyNetServers(profiles, "[]")
	if len(cb.lines) != n {
		t.Fatalf("журнал повторился:\n%s", cb.all())
	}
}

// Список пришёл через соединение, у которого нет профиля, — причина в
// журнале, а не тишина (так было на v1.10.6).
func TestApplyNetServersНетПрофиляСоединения(t *testing.T) {
	tun := NewTunnel()
	cb := &logCB{}
	tun.mu.Lock()
	tun.cb = cb
	tun.mu.Unlock()
	tun.onNetServers(mesh.NetServers{Nets: []string{"N"}, Servers: []mesh.NetServer{
		{ID: "lon", Name: "London", Host: "london.example", Net: "N", Acct: true, Via: "other.example"},
	}})
	profiles := profilesJSON(t, srv("fra", "fra.example", map[string]any{"meshEnabled": true, "meshKey": "K"}))
	if out, _ := tun.ApplyNetServers(profiles, "[]"); out != "" {
		t.Fatalf("добавлен без профиля соединения: %s", out)
	}
	if !strings.Contains(cb.all(), "London не добавлен — не найден профиль сервера, через который пришёл список (other.example)") {
		t.Fatalf("журнал:\n%s", cb.all())
	}
}
