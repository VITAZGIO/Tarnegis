package app

import (
	"testing"

	"sshtunnel/internal/config"
)

// ТЗ 55: профиль «как в файле» (без общих настроек) не должен поднимать сеть
// устройств с выключенными входящими, когда галочка стоит в общих настройках.
func TestTunnelConfigResolvesMeshIncoming(t *testing.T) {
	raw := config.Profile{ID: "p1", Host: "example.org", MeshEnabled: true, MeshKey: "k"}
	cfg := config.Config{
		Profiles:      []config.Profile{raw},
		ActiveProfile: "p1",
		PrefsV:        config.PrefsVersion,
		Prefs:         config.Prefs{MeshIncoming: true},
	}
	a := &App{}
	tc, _, _ := a.tunnelConfig(cfg, raw)
	mc := tc.MeshConfig()
	if mc == nil {
		t.Fatal("сеть устройств не собралась")
	}
	if !mc.AllowIncoming {
		t.Fatal("входящие выключены, хотя в общих настройках включены")
	}
}
