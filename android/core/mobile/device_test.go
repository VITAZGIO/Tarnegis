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
