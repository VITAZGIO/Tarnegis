package config

import (
	"encoding/json"
	"slices"
	"testing"
)

// testNetKey — ключ сети панели в профиле (ТЗ 49).
const testNetKey = "ключ-сети-панели-0123456789"

func devSrv(name, host string) DeviceServer {
	return DeviceServer{Name: name, Host: host, Port: 22, HostKey: "ssh-ed25519 AAAA"}
}

// joined — конфиг с одним сервером руками и устройством из панели на A и B.
func joined(t *testing.T) Config {
	t.Helper()
	c := Default()
	c.Profiles[0].Name, c.Profiles[0].Host = "Свой", "192.0.2.10"
	res := c.SetDevice(Device{ID: "tun_1", Name: "Ноутбук", KeyPath: "/k/device_tun_1"},
		DeviceProfile{ID: "tun_1", MeshKey: testNetKey, Rev: 5, Mesh: true, Servers: []DeviceServer{devSrv("А", "203.0.113.1"), devSrv("Б", "203.0.113.2")}})
	if !res.Changed || len(res.Added) != 2 || len(res.Pins) != 2 {
		t.Fatalf("%+v", res)
	}
	c.normalize()
	return c
}

func panelProfiles(c Config) []Profile {
	var out []Profile
	for _, p := range c.Profiles {
		if p.FromPanel != "" {
			out = append(out, p)
		}
	}
	return out
}

func TestDeviceСерверыИзПанели(t *testing.T) {
	c := joined(t)
	pp := panelProfiles(c)
	if len(pp) != 2 || len(c.Profiles) != 3 {
		t.Fatalf("%+v", c.Profiles)
	}
	for _, p := range pp {
		if p.User != "tun_1" || p.KeyPath != "/k/device_tun_1" || p.SSHPort != 22 || !p.MeshEnabled || p.MeshKey == "" {
			t.Fatalf("%+v", p)
		}
	}
	// Ключ сети один на устройство.
	if pp[0].MeshKey != pp[1].MeshKey || c.Device.Net.Key != pp[0].MeshKey {
		t.Fatal("разные ключи сети")
	}
	// Ручной профиль не тронут.
	if c.Profiles[0].FromPanel != "" || c.Profiles[0].MeshEnabled {
		t.Fatalf("%+v", c.Profiles[0])
	}
}

func TestDeviceRevНеВыросНичегоНеМеняет(t *testing.T) {
	c := joined(t)
	before, _ := json.Marshal(c)
	res := c.ApplyDevice(DeviceProfile{ID: "tun_1", MeshKey: testNetKey, Rev: 5, Mesh: false, Servers: nil})
	after, _ := json.Marshal(c)
	if res.Changed || string(before) != string(after) {
		t.Fatal("применил старый rev")
	}
	if res := c.ApplyDevice(DeviceProfile{ID: "tun_2", Rev: 9}); res.Changed {
		t.Fatal("применил чужую учётную запись")
	}
}

func TestDeviceПанельУбралаИДобавила(t *testing.T) {
	c := joined(t)
	res := c.ApplyDevice(DeviceProfile{ID: "tun_1", MeshKey: testNetKey, Rev: 6, Mesh: true,
		Servers: []DeviceServer{devSrv("А", "203.0.113.1"), devSrv("В", "203.0.113.3")}})
	if !res.Changed || len(res.Added) != 1 || res.Added[0].Name != "В" || len(res.Removed) != 1 || res.Removed[0] != "Б" {
		t.Fatalf("%+v", res)
	}
	if _, ok := c.ProfileByKey("203.0.113.2:22"); ok {
		t.Fatal("убранный сервер остался")
	}
	if p, ok := c.ProfileByKey("203.0.113.3:22"); !ok || p.User != "tun_1" {
		t.Fatal("новый сервер не появился")
	}
}

func TestDeviceАктивныйУбранПереход(t *testing.T) {
	c := joined(t)
	b, _ := c.ProfileByKey("203.0.113.2:22")
	c.ActiveProfile = b.ID
	res := c.ApplyDevice(DeviceProfile{ID: "tun_1", MeshKey: testNetKey, Rev: 6, Mesh: true, Servers: []DeviceServer{devSrv("А", "203.0.113.1")}})
	a, _ := c.ProfileByKey("203.0.113.1:22")
	if c.ActiveProfile != a.ID || res.Switched != "А" {
		t.Fatalf("active=%s res=%+v", c.ActiveProfile, res)
	}
	// Панель убрала все — активным становится ручной.
	res = c.ApplyDevice(DeviceProfile{ID: "tun_1", MeshKey: testNetKey, Rev: 7, Mesh: true})
	if c.ActiveProfile != c.Profiles[0].ID || res.Switched != "Свой" || len(c.Profiles) != 1 {
		t.Fatalf("active=%s res=%+v %+v", c.ActiveProfile, res, c.Profiles)
	}
}

func TestDeviceСкрытьУСебя(t *testing.T) {
	c := joined(t)
	b, _ := c.ProfileByKey("203.0.113.2:22")
	c.ActiveProfile = b.ID
	sw, ok := c.SetHidden(b.ID, true)
	if !ok || sw == "" || c.ActiveProfile == b.ID {
		t.Fatalf("sw=%q ok=%v", sw, ok)
	}
	if p, _ := c.ProfileByKey("203.0.113.2:22"); !p.Hidden {
		t.Fatal("не скрыт")
	}
	// Скрытый не идёт в другие серверы сети устройств.
	a, _ := c.ProfileByKey("203.0.113.1:22")
	if len(c.MeshServers(a)) != 0 {
		t.Fatal("скрытый сервер в сети устройств")
	}
	// Новый профиль панели (rev вырос) скрытость не сбрасывает.
	c.ApplyDevice(DeviceProfile{ID: "tun_1", MeshKey: testNetKey, Rev: 6, Mesh: true, Servers: []DeviceServer{devSrv("А", "203.0.113.1"), devSrv("Б", "203.0.113.2")}})
	if p, _ := c.ProfileByKey("203.0.113.2:22"); !p.Hidden {
		t.Fatal("скрытость пропала")
	}
	// Ручной профиль скрыть нельзя.
	if _, ok := c.SetHidden(c.Profiles[0].ID, true); ok {
		t.Fatal("скрыл ручной")
	}
	c.SetHidden(b.ID, false)
	if p, _ := c.ProfileByKey("203.0.113.2:22"); p.Hidden {
		t.Fatal("не вернулся")
	}
}

func TestDeviceMeshFalseХранитСвойВыбор(t *testing.T) {
	c := joined(t)
	res := c.ApplyDevice(DeviceProfile{ID: "tun_1", MeshKey: testNetKey, Rev: 6, Mesh: false, Servers: []DeviceServer{devSrv("А", "203.0.113.1")}})
	if !res.MeshOff {
		t.Fatalf("%+v", res)
	}
	p, _ := c.ProfileByKey("203.0.113.1:22")
	if p.MeshEnabled || !c.Device.Net.On || c.DeviceMeshAllowed() {
		t.Fatal("mesh:false не выключил сеть или стёр выбор устройства")
	}
	// Панель вернула доступ — сеть снова включена сама.
	res = c.ApplyDevice(DeviceProfile{ID: "tun_1", MeshKey: testNetKey, Rev: 7, Mesh: true, Servers: []DeviceServer{devSrv("А", "203.0.113.1")}})
	p, _ = c.ProfileByKey("203.0.113.1:22")
	if !p.MeshEnabled || !res.MeshBack {
		t.Fatalf("не вернулась: %+v", res)
	}
	// mesh:true, а на устройстве выключено — выключено.
	n := c.DeviceNetOf()
	n.On = false
	c.SetDeviceNet(n)
	p, _ = c.ProfileByKey("203.0.113.1:22")
	if p.MeshEnabled {
		t.Fatal("выключенная на устройстве сеть включилась")
	}
	// Заморожено — как mesh:false.
	n.On = true
	c.SetDeviceNet(n)
	c.ApplyDevice(DeviceProfile{ID: "tun_1", MeshKey: testNetKey, Rev: 8, Mesh: true, Frozen: true, Servers: []DeviceServer{devSrv("А", "203.0.113.1")}})
	if p, _ = c.ProfileByKey("203.0.113.1:22"); p.MeshEnabled {
		t.Fatal("замороженное устройство в сети")
	}
}

func TestDeviceОдинБлокНаВсеСерверы(t *testing.T) {
	c := joined(t)
	a, _ := c.ProfileByKey("203.0.113.1:22")
	b, _ := c.ProfileByKey("203.0.113.2:22")
	c.SetDeviceNet(DeviceNet{On: true, OnlyThis: true, ExitVia: []string{b.ID}})
	a, _ = c.ProfileByID(a.ID)
	b, _ = c.ProfileByID(b.ID)
	if !a.MeshOnlyThis || !b.MeshOnlyThis || !slices.Equal(a.ExitVia, []string{b.ID}) || len(b.ExitVia) != 0 {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	// Правка профиля руками (форма настроек) адрес и вход не меняет.
	a.Host, a.User, a.KeyPath = "evil.example", "root", "/tmp/x"
	c.SetProfile(a)
	c.normalize()
	a, _ = c.ProfileByID(a.ID)
	if a.Host != "203.0.113.1" || a.User != "tun_1" || a.KeyPath != "/k/device_tun_1" {
		t.Fatalf("%+v", a)
	}
}

func TestDeviceNetOfСтарыеЗначения(t *testing.T) {
	// Конфиг до ТЗ 48: устройства нет — значения активного профиля.
	c := Default()
	c.Profiles[0].MeshEnabled, c.Profiles[0].MeshKey, c.Profiles[0].MeshOnlyThis = true, "ключ", true
	c.Profiles[0].ExitVia = []string{"p_x"}
	n := c.DeviceNetOf()
	if !n.On || n.Key != "ключ" || !n.OnlyThis || !slices.Equal(n.ExitVia, []string{"p_x"}) {
		t.Fatalf("%+v", n)
	}
	// Устройство без блока сети — значения его первого профиля, и профили
	// из панели свои поля сохраняют.
	c.Device = &Device{ID: "tun_1", KeyPath: "/k", Mesh: true, Servers: []DeviceServer{devSrv("А", "203.0.113.1")}}
	p := NewProfile("А", "")
	p.Host, p.FromPanel, p.MeshEnabled, p.MeshKey = "203.0.113.1", "tun_1", false, "другой"
	c.Profiles = append(c.Profiles, p)
	c.normalize()
	n = c.DeviceNetOf()
	if n.On || n.Key != "другой" {
		t.Fatalf("%+v", n)
	}
	q, _ := c.ProfileByID(p.ID)
	if q.MeshKey != "другой" || q.User != "tun_1" {
		t.Fatalf("%+v", q)
	}
}

// ТЗ 49: устройство из панели — только в сети панели: ключ из профиля, свой
// (даже уже бывший на устройстве) не берётся и не заводится.
func TestDeviceКлючСетиТолькоОтПанели(t *testing.T) {
	c := Default()
	c.Profiles[0].Host, c.Profiles[0].MeshEnabled, c.Profiles[0].MeshKey = "192.0.2.1", true, "моя-сеть"
	c.SetDevice(Device{ID: "tun_1", KeyPath: "/k"}, DeviceProfile{ID: "tun_1", Rev: 1, Mesh: true, MeshKey: testNetKey,
		Servers: []DeviceServer{devSrv("А", "203.0.113.1")}})
	c.normalize()
	if c.Device.Net.Key != testNetKey {
		t.Fatalf("%+v", c.Device.Net)
	}
	if p, _ := c.ProfileByKey("203.0.113.1:22"); !p.MeshEnabled || p.MeshKey != testNetKey {
		t.Fatalf("%+v", p)
	}
	// Ручной профиль остаётся в своей сети.
	if c.Profiles[0].MeshKey != "моя-сеть" {
		t.Fatal("ручной профиль сменил сеть")
	}

	// Панель старше ТЗ 49: ключа в профиле нет — сети нет, своей не заводится.
	old := Default()
	old.SetDevice(Device{ID: "tun_2", KeyPath: "/k"}, DeviceProfile{ID: "tun_2", Rev: 1, Mesh: true,
		Servers: []DeviceServer{devSrv("А", "203.0.113.1")}})
	old.normalize()
	if p, _ := old.ProfileByKey("203.0.113.1:22"); p.MeshEnabled || p.MeshKey != "" {
		t.Fatalf("своя сеть у устройства из панели: %+v", p)
	}
	if !old.DeviceNetMissing() || old.Device.Net.Key != "" {
		t.Fatalf("нет отметки «панель не выдала сеть»: %+v", old.Device.Net)
	}
	// Сохранение и чтение — ключ не появляется.
	data, _ := json.Marshal(old)
	back, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := back.ProfileByKey("203.0.113.1:22"); p.MeshEnabled || p.MeshKey != "" {
		t.Fatalf("после чтения: %+v", p)
	}
	// Сервер обновили: ключ пришёл с тем же rev — применяется, сеть есть.
	res := old.ApplyDevice(DeviceProfile{ID: "tun_2", Rev: 1, Mesh: true, MeshKey: testNetKey,
		Servers: []DeviceServer{devSrv("А", "203.0.113.1")}})
	if !res.Changed || !res.NetJoined {
		t.Fatalf("%+v", res)
	}
	if p, _ := old.ProfileByKey("203.0.113.1:22"); !p.MeshEnabled || p.MeshKey != testNetKey || old.DeviceNetMissing() {
		t.Fatalf("%+v", p)
	}
	// Экран ключ не меняет.
	n := old.DeviceNetOf()
	n.Key = "подмена"
	old.SetDeviceNet(n)
	if old.Device.Net.Key != testNetKey {
		t.Fatal("ключ сети сменён экраном")
	}
}

func TestDeviceПрофильРукамиПодЭтойУчёткой(t *testing.T) {
	c := Default()
	c.Profiles[0].Host, c.Profiles[0].User, c.Profiles[0].Name = "203.0.113.1", "tun_1", "Мой А"
	res := c.SetDevice(Device{ID: "tun_1", KeyPath: "/k"}, DeviceProfile{ID: "tun_1", MeshKey: testNetKey, Rev: 1, Mesh: true, Servers: []DeviceServer{devSrv("А", "203.0.113.1")}})
	if len(c.Profiles) != 1 || c.Profiles[0].FromPanel != "tun_1" || len(res.Added) != 0 {
		t.Fatalf("%+v %+v", c.Profiles, res)
	}
}

func TestDeviceИмяИзПанелиЕслиНеПравили(t *testing.T) {
	c := joined(t)
	b, _ := c.ProfileByKey("203.0.113.2:22")
	b.Name = "Мой Б"
	c.SetProfile(b)
	c.ApplyDevice(DeviceProfile{ID: "tun_1", MeshKey: testNetKey, Rev: 6, Mesh: true, Servers: []DeviceServer{devSrv("Альфа", "203.0.113.1"), devSrv("Бета", "203.0.113.2")}})
	a, _ := c.ProfileByKey("203.0.113.1:22")
	b, _ = c.ProfileByKey("203.0.113.2:22")
	if a.Name != "Альфа" || b.Name != "Мой Б" {
		t.Fatalf("%s %s", a.Name, b.Name)
	}
}

func TestDeviceСетьСерверовНеДобавляетЧерезПанель(t *testing.T) {
	c := joined(t)
	a, _ := c.ProfileByKey("203.0.113.1:22")
	n := len(c.Profiles)
	c.ApplyNetServers([]NetServerAd{{ID: "s9", Name: "Чужой", Host: "203.0.113.9", Port: 22, Net: "n1", Acct: true, Via: ServerKey(a)}}, []string{"n1"})
	if len(c.Profiles) != n {
		t.Fatal("сеть серверов добавила сервер в обход профиля панели")
	}
}

func TestDeviceСтарыйКонфигЧитается(t *testing.T) {
	c, err := Parse([]byte(`{"profiles":[{"id":"p_1","name":"S","host":"h","sshPort":22,"user":"u","meshEnabled":true,"meshKey":"k"}],"activeProfile":"p_1"}`))
	if err != nil || c.Device != nil || !c.Profiles[0].MeshEnabled || c.Profiles[0].MeshKey != "k" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestDeviceЦепочкаИзПанелиСервераСтановитсяОбщей(t *testing.T) {
	c := joined(t)
	a, _ := c.ProfileByKey("203.0.113.1:22")
	b, _ := c.ProfileByKey("203.0.113.2:22")
	// Панель сервера (ТЗ 16) задала «А через Б».
	changed, _, err := c.ApplyExitOrder(ExitOrder{Entry: "203.0.113.1:22", Via: []string{"203.0.113.2:22"}, T: 1 << 40, By: "А"})
	if !changed || err != nil {
		t.Fatal(changed, err)
	}
	c.normalize()
	if !slices.Equal(c.Device.Net.ExitVia, []string{b.ID}) || c.Device.Net.ExitBy != "А" {
		t.Fatalf("%+v", c.Device.Net)
	}
	a, _ = c.ProfileByID(a.ID)
	b, _ = c.ProfileByID(b.ID)
	if !slices.Equal(a.ExitVia, []string{b.ID}) || len(b.ExitVia) != 0 {
		t.Fatalf("a=%v b=%v", a.ExitVia, b.ExitVia)
	}
	// Тот же старый приказ ещё раз — ничего.
	if changed, _, _ := c.ApplyExitOrder(ExitOrder{Entry: "203.0.113.1:22", Via: nil, T: 1 << 40, By: "А"}); changed {
		t.Fatal("старый приказ применился")
	}
	// Правка на устройстве новее — побеждает.
	n := c.DeviceNetOf()
	n.ExitVia = nil
	c.SetDeviceNet(n)
	a, _ = c.ProfileByID(a.ID)
	if len(a.ExitVia) != 0 || c.Device.Net.ExitT <= 1<<40 {
		t.Fatalf("%v %d", a.ExitVia, c.Device.Net.ExitT)
	}
}

func TestDeviceПрофильИзПанелиНеУдаляется(t *testing.T) {
	c := joined(t)
	c.Profiles = c.Profiles[:1] // экран прислал список без серверов из панели
	c.normalize()
	if len(panelProfiles(c)) != 2 {
		t.Fatalf("%+v", c.Profiles)
	}
	if p, _ := c.ProfileByKey("203.0.113.2:22"); p.User != "tun_1" || !p.MeshEnabled {
		t.Fatalf("%+v", p)
	}
}

func TestDeviceВыгрузкаИЗагрузка(t *testing.T) {
	c := joined(t)
	b, _ := c.ProfileByKey("203.0.113.2:22")
	c.SetDeviceNet(DeviceNet{On: true, ExitVia: []string{b.ID}})
	c.SetHidden(b.ID, true)
	ex := c.DeviceForExport()
	data, _ := json.Marshal(ex)
	var back Device
	json.Unmarshal(data, &back)
	if back.ID != "tun_1" || back.KeyPath != "" || !slices.Equal(back.Net.ExitVia, []string{"203.0.113.2:22"}) || len(back.Hidden) != 1 {
		t.Fatalf("%+v %+v", back, back.Net)
	}
	// Новое устройство: профили собраны из выгрузки заново, id свои.
	n := Default()
	n.Profiles = nil
	for _, p := range c.Profiles {
		q := NewProfile(p.Name, "")
		q.Host, q.SSHPort, q.User, q.KeyPath = p.Host, p.SSHPort, p.User, "/new/"+p.User
		if p.FromPanel != "" {
			q.FromPanel, q.PanelKey = p.FromPanel, ServerKey(q)
		}
		n.Profiles = append(n.Profiles, q)
	}
	n.Device = DeviceFromExport(&back, n.Profiles)
	n.normalize()
	nb, _ := n.ProfileByKey("203.0.113.2:22")
	na, _ := n.ProfileByKey("203.0.113.1:22")
	if n.Device.KeyPath != "/new/tun_1" || !nb.Hidden || !slices.Equal(na.ExitVia, []string{nb.ID}) || na.KeyPath != "/new/tun_1" {
		t.Fatalf("%+v a=%+v b=%+v", n.Device, na, nb)
	}
}
