package config

import "testing"

func TestСерверСетиДобавляетсяСам(t *testing.T) {
	c := Config{Profiles: []Profile{{ID: "a", Name: "Германия", Host: "de.example", SSHPort: 22, User: "tun_x",
		KeyPath: "/k", MeshEnabled: true, MeshKey: "K", MeshName: "ноут"}}}
	ads := []NetServerAd{
		{ID: "m2", Name: "Англия", Host: "uk.example", Port: 2222, Net: "N", Acct: true, Via: "de.example:22"},
		{ID: "m3", Name: "Франция", Host: "fr.example", Net: "N", Acct: false, Via: "de.example:22"},
	}
	res := c.ApplyNetServers(ads, []string{"N"})
	if len(res.Added) != 1 || len(c.Profiles) != 2 {
		t.Fatalf("добавлено %+v", res)
	}
	p := c.Profiles[1]
	if p.Host != "uk.example" || p.SSHPort != 2222 || p.User != "tun_x" || p.KeyPath != "/k" || !p.MeshEnabled ||
		p.MeshKey != "K" || p.MeshAuto != "N/m2" || p.ID == "a" || p.Name != "Англия" {
		t.Fatalf("профиль %+v", p)
	}
	// Второй раз — ничего (уже есть).
	if res := c.ApplyNetServers(ads, []string{"N"}); len(res.Added) != 0 || len(res.Off) != 0 {
		t.Fatalf("повтор: %+v", res)
	}
	// Человек выключил — так и остаётся; удалил — не возвращается.
	c.Profiles[1].MeshEnabled = false
	c.ApplyNetServers(ads, []string{"N"})
	if c.Profiles[1].MeshEnabled {
		t.Fatal("выключенный вернулся")
	}
	c.Profiles = c.Profiles[:1]
	if res := c.ApplyNetServers(ads, []string{"N"}); len(res.Added) != 0 {
		t.Fatal("удалённый вернулся")
	}
}

func TestУбранныйСерверСетиВыключается(t *testing.T) {
	c := Config{Profiles: []Profile{
		{ID: "a", Host: "de.example", MeshEnabled: true, MeshKey: "K"},
		{ID: "b", Name: "Англия", Host: "uk.example", MeshEnabled: true, MeshKey: "K", MeshAuto: "N/m2"},
	}}
	// Список сети N неизвестен — ничего не трогаем.
	if res := c.ApplyNetServers(nil, nil); len(res.Off) != 0 {
		t.Fatal("выключен без списка")
	}
	// Список известен, сервера в нём нет — выключить, не удалять.
	res := c.ApplyNetServers(nil, []string{"N"})
	if len(res.Off) != 1 || c.Profiles[1].MeshEnabled || len(c.Profiles) != 2 {
		t.Fatalf("%+v %+v", res, c.Profiles)
	}
}
