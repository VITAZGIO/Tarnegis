package app

import (
	"testing"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
)

// ТЗ 49, B: профиль с тремя серверами — три профиля сразу в работающей
// программе (a.Config) и событие config экрану: окно перечитывает настройки
// само, без перезапуска (пока туннель поднят, оно их не опрашивает).
func TestПрофильУстройстваСразуВРаботающейПрограмме(t *testing.T) {
	a := New(config.Default())
	t.Cleanup(a.Stop)
	cfg := cloneConfig(a.Config())
	cfg.SetDevice(config.Device{ID: "tun_1", Name: "ПК", KeyPath: "/k/device_tun_1"},
		config.DeviceProfile{ID: "tun_1", Rev: 0, Mesh: true,
			Servers: []config.DeviceServer{{Name: "А", Host: "203.0.113.1", Port: 22}}})
	if _, err := a.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}

	ch, stop := a.Bus.Subscribe()
	defer stop()
	res, err := a.applyDevice(config.DeviceProfile{ID: "tun_1", Rev: 5, Mesh: true, MeshKey: "ключ-сети-панели-0123456789",
		Servers: []config.DeviceServer{
			{Name: "А", Host: "203.0.113.1", Port: 22},
			{Name: "Б", Host: "203.0.113.2", Port: 22},
			{Name: "В", Host: "203.0.113.3", Port: 22},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || len(res.Added) != 2 || !res.NetJoined {
		t.Fatalf("%+v", res)
	}
	n := 0
	for _, p := range a.Config().Profiles {
		if p.IsPanel() {
			n++
			if !p.MeshEnabled || p.MeshKey != "ключ-сети-панели-0123456789" {
				t.Fatalf("сеть не панели: %+v", p)
			}
		}
	}
	if n != 3 {
		t.Fatalf("в работающей программе %d серверов из панели: %+v", n, a.Config().Profiles)
	}
	got := false
	deadline := time.After(2 * time.Second)
	for !got {
		select {
		case e := <-ch:
			got = e.Kind == events.KindConfig
		case <-deadline:
			t.Fatal("нет события config для экрана")
		}
	}
	// Сохранено на диск тем же.
	if disk := config.Load(); len(disk.Profiles) != len(a.Config().Profiles) {
		t.Fatalf("на диске %d профилей, в программе %d", len(disk.Profiles), len(a.Config().Profiles))
	}
}

// ТЗ 49, C.2: удалить можно и последний сервер; сервер из панели — только
// когда устройство удалено в панели.
func TestУдалитьПоследнийСервер(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles[0].Host = "192.0.2.1"
	a := New(cfg)
	t.Cleanup(a.Stop)
	id := a.Config().Profiles[0].ID
	if _, err := a.RemoveProfile(id); err != nil {
		t.Fatal(err)
	}
	if len(a.Config().Profiles) != 0 || a.Config().Active().Host != "" {
		t.Fatalf("%+v", a.Config().Profiles)
	}
	if err := a.Start(); err == nil {
		t.Fatal("подключение без серверов")
	}

	c := cloneConfig(a.Config())
	c.SetDevice(config.Device{ID: "tun_1", KeyPath: "/k"}, config.DeviceProfile{ID: "tun_1", Rev: 1, Mesh: true,
		Servers: []config.DeviceServer{{Name: "А", Host: "203.0.113.1", Port: 22}}})
	a.SetConfig(c)
	pid := a.Config().Profiles[0].ID
	if _, err := a.RemoveProfile(pid); err == nil {
		t.Fatal("удалён сервер из панели, хотя устройство не удалено")
	}
	a.dev.mu.Lock()
	a.dev.gone = true
	a.dev.mu.Unlock()
	if _, err := a.RemoveProfile(pid); err != nil {
		t.Fatal(err)
	}
	if len(a.Config().Profiles) != 0 || a.Config().Device != nil {
		t.Fatalf("%+v %+v", a.Config().Profiles, a.Config().Device)
	}
}
