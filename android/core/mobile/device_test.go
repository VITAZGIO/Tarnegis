package mobile

import (
	"encoding/json"
	"testing"

	"sshtunnel/internal/config"
)

func TestDeviceApplyТелефон(t *testing.T) {
	join, _ := json.Marshal(map[string]any{
		"device":  config.Device{ID: "tun_1", Name: "Телефон", KeyPath: "/k/device_tun_1"},
		"profile": config.DeviceProfile{ID: "tun_1", Rev: 2, Mesh: true, Servers: []config.DeviceServer{{Name: "А", Host: "203.0.113.1", Port: 22}, {Name: "Б", Host: "203.0.113.2", Port: 22}}},
	})
	st, _ := json.Marshal(map[string]any{"op": "set", "join": json.RawMessage(join), "active": "p1",
		"profiles": []config.Profile{{ID: "p1", Name: "Server 1"}}})
	out, err := DeviceApply(string(st))
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Profiles []config.Profile
		Active   string
		Device   *config.Device
		Added    []string
		Removed  []string
		Switched string
	}
	json.Unmarshal([]byte(out), &r)
	if len(r.Profiles) != 2 || r.Device == nil || len(r.Added) != 2 || r.Active != r.Profiles[0].ID || r.Profiles[0].User != "tun_1" {
		t.Fatalf("%s", out)
	}
	// Панель убрала активный — переход и одна строка.
	st, _ = json.Marshal(map[string]any{"op": "apply", "profiles": r.Profiles, "active": r.Active, "device": r.Device,
		"profile": config.DeviceProfile{ID: "tun_1", Rev: 3, Mesh: true, Servers: []config.DeviceServer{{Name: "Б", Host: "203.0.113.2", Port: 22}}}})
	out, err = DeviceApply(string(st))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(out), &r)
	if len(r.Profiles) != 1 || r.Switched != "Б" || len(r.Removed) != 1 {
		t.Fatalf("%s", out)
	}
	// rev не вырос — ничего.
	st, _ = json.Marshal(map[string]any{"op": "apply", "profiles": r.Profiles, "active": r.Active, "device": r.Device,
		"profile": config.DeviceProfile{ID: "tun_1", Rev: 3, Mesh: false}})
	out, _ = DeviceApply(string(st))
	var c struct{ Changed bool }
	json.Unmarshal([]byte(out), &c)
	if c.Changed {
		t.Fatal("применил старый rev")
	}
}

func TestInviteInfoМусор(t *testing.T) {
	if _, err := InviteInfo("привет"); err == nil {
		t.Fatal("принял мусор")
	}
	if !IsInvite("  sshtun1:abc") {
		t.Fatal("не узнал")
	}
}

// ТЗ 51 на телефоне: сервер из панели удаляется (активный — выбран другой),
// сверка с выросшим rev его не возвращает, «Вернуть» — возвращает.
func TestDeviceApplyУдалениеСервераИзПанели(t *testing.T) {
	srv := []config.DeviceServer{{Name: "Франкфурт", Host: "203.0.113.1", Port: 22}, {Name: "Б", Host: "203.0.113.2", Port: 22}}
	join, _ := json.Marshal(map[string]any{"device": config.Device{ID: "tun_1", KeyPath: "/k"},
		"profile": config.DeviceProfile{ID: "tun_1", Rev: 2, Mesh: true, Servers: srv}})
	st, _ := json.Marshal(map[string]any{"op": "set", "join": json.RawMessage(join), "profiles": []config.Profile{}})
	out, err := DeviceApply(string(st))
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Profiles       []config.Profile
		Active         string
		Device         *config.Device
		Switched       string
		RemovedServers []config.DeviceServer
	}
	json.Unmarshal([]byte(out), &r)
	if len(r.Profiles) != 2 || r.Profiles[0].Name != "Франкфурт" || r.Active != r.Profiles[0].ID {
		t.Fatalf("%s", out)
	}
	st, _ = json.Marshal(map[string]any{"op": "remove", "id": r.Active, "profiles": r.Profiles, "active": r.Active, "device": r.Device})
	if out, err = DeviceApply(string(st)); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(out), &r)
	if len(r.Profiles) != 1 || r.Profiles[0].Name != "Б" || r.Active != r.Profiles[0].ID || r.Switched != "Б" ||
		len(r.RemovedServers) != 1 || r.Device == nil {
		t.Fatalf("после удаления: %s", out)
	}
	st, _ = json.Marshal(map[string]any{"op": "apply", "profiles": r.Profiles, "active": r.Active, "device": r.Device,
		"profile": config.DeviceProfile{ID: "tun_1", Rev: 5, Mesh: true, Servers: srv}})
	out, _ = DeviceApply(string(st))
	json.Unmarshal([]byte(out), &r)
	if len(r.Profiles) != 1 {
		t.Fatalf("сверка вернула удалённый: %s", out)
	}
	st, _ = json.Marshal(map[string]any{"op": "restore", "key": "203.0.113.1:22", "profiles": r.Profiles, "active": r.Active, "device": r.Device})
	out, _ = DeviceApply(string(st))
	json.Unmarshal([]byte(out), &r)
	if len(r.Profiles) != 2 || len(r.RemovedServers) != 0 {
		t.Fatalf("после «Вернуть»: %s", out)
	}
	if m := RemovedMarks(`{"host":"203.0.113.1","sshPort":22,"fromPanel":"tun_1"}`); m != `["del@203.0.113.1:22"]` {
		t.Fatalf("отметки: %s", m)
	}
}
