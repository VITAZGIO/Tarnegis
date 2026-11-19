package mobile

// ТЗ 26: телефон сам добавляет серверы сети — тем же общим кодом, что ПК
// (config.ApplyNetServers); мост только передаёт и сохраняет ключ сервера.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/mesh"
)

type netListenerFunc func()

func (f netListenerFunc) OnNetServers() { f() }

func TestApplyNetServersТелефон(t *testing.T) {
	dir := t.TempDir()
	tun := NewTunnel()
	tun.cfg.KnownHostsPath = filepath.Join(dir, "known_hosts")
	called := make(chan struct{}, 4)
	tun.SetNetServersListener(netListenerFunc(func() { called <- struct{}{} }))

	// Список пришёл через основной сервер: Via — его адрес без порта, как
	// mesh.Config.Via на телефоне (t.cfg.Host) и на ПК (Profile.Host).
	tun.onNetServers(mesh.NetServers{Nets: []string{"N"}, Servers: []mesh.NetServer{
		{ID: "london", Name: "London", Host: "london.example", Port: 22, HostKey: "ssh-ed25519 AAAAlondon",
			Net: "N", Acct: true, Via: "de.example"},
		{ID: "ams", Name: "Amsterdam", Host: "ams.example", Port: 2222, Net: "N", Acct: true, Via: "de.example"},
	}})
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("слушатель не позвали")
	}
	profiles := profilesJSON(t,
		srv("de", "de.example", map[string]any{"meshEnabled": true, "meshKey": "K"}),
		srv("old", "ams.example", map[string]any{"sshPort": 2222}), // заведён руками, сеть выключена
	)
	out, err := tun.ApplyNetServers(profiles, "")
	if err != nil || out == "" {
		t.Fatalf("ApplyNetServers: %q %v", out, err)
	}
	var res struct {
		Profiles []struct {
			ID, Name, Host, KeyPath, MeshKey, MeshAuto string
			MeshEnabled                                bool
		}
		Seen   []string
		Added  []string
		Off    []string
		MeshOn []string `json:"meshOn"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Added, ",") != "London" || strings.Join(res.MeshOn, ",") != "Сервер old" {
		t.Fatalf("added %v meshOn %v", res.Added, res.MeshOn)
	}
	if len(res.Profiles) != 3 {
		t.Fatalf("профилей %d", len(res.Profiles))
	}
	ln := res.Profiles[2]
	if ln.Host != "london.example" || !ln.MeshEnabled || ln.MeshKey != "K" || ln.MeshAuto != "N/london" ||
		ln.KeyPath != "/keys/de" || ln.ID == "de" {
		t.Fatalf("новый профиль %+v", ln)
	}
	if !res.Profiles[1].MeshEnabled || res.Profiles[1].MeshKey != "K" {
		t.Fatalf("старый профиль Амстердама не вошёл в сеть: %+v", res.Profiles[1])
	}
	kh, _ := os.ReadFile(tun.cfg.KnownHostsPath)
	if string(kh) != "london.example ssh-ed25519 AAAAlondon\n" {
		t.Fatalf("known_hosts %q", kh)
	}
	// Второй раз с сохранённым ответом — ничего нового.
	pj, _ := json.Marshal(res.Profiles)
	sj, _ := json.Marshal(res.Seen)
	var raw []map[string]any
	json.Unmarshal(pj, &raw)
	for _, p := range raw {
		p["sshPort"] = 22
		if p["host"] == "ams.example" {
			p["sshPort"] = 2222
		}
	}
	pj, _ = json.Marshal(raw)
	if again, err := tun.ApplyNetServers(string(pj), string(sj)); err != nil || again != "" {
		t.Fatalf("повтор: %q %v", again, err)
	}
}
