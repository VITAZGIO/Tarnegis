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

// ТЗ 24, причина разрыва: London сначала пришёл с адресом london.mesh, а у
// устройства уже был старый профиль «Лондон» на этом адресе. Программа
// v1.10.1 помечала сервер «уже добавлен» и, когда London стал давать свой
// настоящий адрес, больше его не добавляла никогда.
func TestСерверССтарымАдресомДобавляетсяПослеСменыАдреса(t *testing.T) {
	c := Config{Profiles: []Profile{
		{ID: "a", Name: "Германия", Host: "89.125.168.26", User: "tun_x", KeyPath: "/k", MeshEnabled: true, MeshKey: "K"},
		{ID: "b", Name: "Лондон", Host: "london.mesh", User: "root"},
	}}
	bad := []NetServerAd{{ID: "lon", Name: "London", Host: "london.mesh", Net: "N", Acct: true, Via: "89.125.168.26:22"}}
	if res := c.ApplyNetServers(bad, []string{"N"}); len(res.Added) != 0 {
		t.Fatalf("профиль уже есть — добавлять нечего: %+v", res)
	}
	if len(c.MeshAutoSeen) != 0 {
		t.Fatalf("сервер помечен «уже добавлен», хотя не добавлялся: %v", c.MeshAutoSeen)
	}
	good := []NetServerAd{{ID: "lon", Name: "London", Host: "203.0.113.7", Net: "N", Acct: true, Via: "89.125.168.26:22"}}
	res := c.ApplyNetServers(good, []string{"N"})
	if len(res.Added) != 1 || res.Added[0].Host != "203.0.113.7" || !res.Added[0].MeshEnabled {
		t.Fatalf("London с настоящим адресом не добавлен: %+v", res)
	}
}

// Отметка старого вида (v1.10.1, без адреса), оставшаяся от ветки «профиль
// уже есть», сервер не закрывает; от настоящего добавления — закрывает.
func TestСтараяОтметкаУжеДобавлен(t *testing.T) {
	base := Profile{ID: "a", Host: "de.example", User: "tun_x", MeshEnabled: true, MeshKey: "K"}
	ad := []NetServerAd{{ID: "lon", Host: "uk.example", Net: "N", Acct: true, Via: "de.example:22"}}
	c := Config{Profiles: []Profile{base}, MeshAutoSeen: []string{"N/lon"}}
	if res := c.ApplyNetServers(ad, []string{"N"}); len(res.Added) != 1 {
		t.Fatalf("старая отметка без профиля закрыла сервер: %+v", res)
	}
	c = Config{Profiles: []Profile{base, {ID: "b", Host: "uk.example", MeshAuto: "N/lon"}}, MeshAutoSeen: []string{"N/lon"}}
	c.Profiles[1].MeshEnabled = false
	if res := c.ApplyNetServers(ad, []string{"N"}); len(res.Added) != 0 || c.Profiles[1].MeshEnabled {
		t.Fatalf("выключенный человеком вернулся: %+v", res)
	}
}

// Профиль сервера сети человек завёл сам раньше (Лондон на устройстве), а
// сеть устройств в нём выключена: программа включает её с ключом сети
// один раз. Выключит человек снова — так и останется. Включённую с другим
// ключом (другая сеть) не трогает.
func TestСтарыйПрофильСервераВходитВСеть(t *testing.T) {
	c := Config{Profiles: []Profile{
		{ID: "a", Name: "Германия", Host: "89.125.168.26", User: "tun_x", KeyPath: "/k", MeshEnabled: true, MeshKey: "K"},
		{ID: "b", Name: "Лондон", Host: "87.120.219.28", User: "tun_y"},
		{ID: "c", Name: "Своя сеть", Host: "203.0.113.9", User: "tun_z", MeshEnabled: true, MeshKey: "OTHER"},
	}}
	ads := []NetServerAd{
		{ID: "lon", Name: "London", Host: "87.120.219.28", Port: 22, Net: "N", Acct: true, Via: "89.125.168.26:22"},
		{ID: "x", Name: "X", Host: "203.0.113.9", Port: 22, Net: "N", Acct: true, Via: "89.125.168.26:22"},
	}
	res := c.ApplyNetServers(ads, []string{"N"})
	if len(res.Added) != 0 || len(res.MeshOn) != 1 || res.MeshOn[0] != "Лондон" {
		t.Fatalf("результат %+v", res)
	}
	if p := c.Profiles[1]; !p.MeshEnabled || p.MeshKey != "K" || p.User != "tun_y" {
		t.Fatalf("Лондон %+v", p)
	}
	if p := c.Profiles[2]; p.MeshKey != "OTHER" {
		t.Fatalf("другая сеть тронута: %+v", p)
	}
	c.Profiles[1].MeshEnabled = false
	if res := c.ApplyNetServers(ads, []string{"N"}); len(res.MeshOn) != 0 || c.Profiles[1].MeshEnabled {
		t.Fatalf("выключенный человеком включился снова: %+v", res)
	}
}

// Via от сети устройств — адрес профиля без порта (так у живой программы:
// ключ соединения — p.Host). v1.10.4 искала профиль по «адрес:22», не
// находила и молча ничего не делала: London не включался, Amsterdam не
// добавлялся.
func TestViaБезПорта(t *testing.T) {
	c := Config{Profiles: []Profile{
		{ID: "a", Name: "Германия", Host: "89.125.168.26", User: "tun_x", KeyPath: "/k", MeshEnabled: true, MeshKey: "K"},
		{ID: "b", Name: "Лондон", Host: "87.120.219.28", User: "tun_y"},
	}}
	ads := []NetServerAd{
		{ID: "lon", Name: "London", Host: "87.120.219.28", Port: 22, Net: "N", Acct: true, Via: "89.125.168.26"},
		{ID: "ams", Name: "Amsterdam", Host: "vitazgio.ru", Port: 22, Net: "N", Acct: true, Via: "89.125.168.26"},
	}
	res := c.ApplyNetServers(ads, []string{"N"})
	if len(res.MeshOn) != 1 || len(res.Added) != 1 || res.Added[0].Host != "vitazgio.ru" || res.Added[0].User != "tun_x" {
		t.Fatalf("результат %+v", res)
	}
	if !c.Profiles[1].MeshEnabled || c.Profiles[1].MeshKey != "K" {
		t.Fatalf("Лондон %+v", c.Profiles[1])
	}
}

// Список серверов пришёл через дополнительное соединение (второй сервер):
// его Via — id профиля. Профиль находится, сервер добавляется.
func TestViaIDПрофиля(t *testing.T) {
	c := Config{Profiles: []Profile{
		{ID: "de", Name: "Германия", Host: "89.125.168.26", User: "tun_x", KeyPath: "/k", MeshEnabled: true, MeshKey: "K"},
		{ID: "lonp", Name: "London", Host: "87.120.219.28", User: "tun_x", KeyPath: "/k", MeshEnabled: true, MeshKey: "K"},
	}}
	ads := []NetServerAd{{ID: "ams", Name: "Amsterdam", Host: "vitazgio.ru", Port: 22, Net: "N", Acct: true, Via: "lonp"}}
	res := c.ApplyNetServers(ads, []string{"N"})
	if len(res.Added) != 1 || res.Added[0].Host != "vitazgio.ru" {
		t.Fatalf("через второй сервер не добавлен: %+v", res)
	}
}
