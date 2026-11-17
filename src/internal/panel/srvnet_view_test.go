package panel

// Одна картина сети на всех серверах (ТЗ 24): правило сложения, подписанная
// картина без секретов, действия с устройством другого сервера и причины
// разрыва «устройства не получили London».

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/meshsvc"
)

// ---------- правило сложения ----------

func vdev(id string, online bool, lastSeen int64) viewDevice {
	return viewDevice{ID: id, Name: id, Display: id, Host: id, IP: "198.19.0." + id[len(id)-1:], Online: online,
		FirstSeen: 1000, LastSeen: lastSeen, RTTMs: 10, Quality: "good", BytesUp: 100}
}

func srcOf(id string, live bool, age int64, devs ...viewDevice) viewSrc {
	return viewSrc{ID: id, Live: live, Age: age, View: srvView{Networks: []viewNet{{ID: "N", Devices: devs}}}}
}

func localNet(devs ...meshsvc.Device) []MeshNetwork {
	nw := MeshNetwork{ID: "N", Name: "Сеть 1"}
	for _, d := range devs {
		nw.Devices = append(nw.Devices, MeshDevice{Device: d, Server: "main"})
	}
	return []MeshNetwork{nw}
}

func findDev(t *testing.T, nets []MeshNetwork, id string) MeshDevice {
	t.Helper()
	for _, n := range nets {
		for _, d := range n.Devices {
			if d.ID == id {
				return d
			}
		}
	}
	t.Fatalf("нет устройства %s в %+v", id, nets)
	return MeshDevice{}
}

func TestСложениеНаСвязиТолькоСОднимСервером(t *testing.T) {
	// Устройство d1 только на «London»: на этом сервере его нет вовсе.
	got := mergeNetworks(localNet(), []viewSrc{srcOf("lon", true, 3, vdev("d1", true, 500))}, nil, nil)
	d := findDev(t, got, "d1")
	if !d.Online || d.Here || d.Server != "lon" || d.Src != "lon" || d.Age != 3 || len(d.Conns) != 1 || d.Stale {
		t.Fatalf("d1: %+v", d)
	}
	if got[0].Name != "Сеть 1" || len(got) != 1 {
		t.Fatalf("сети: %+v", got)
	}
}

func TestСложениеНаСвязиСОбоими(t *testing.T) {
	local := localNet(meshsvc.Device{ID: "d1", Name: "старое", Display: "старое", Online: true, LastSeen: 400, RTTMs: 5})
	got := mergeNetworks(local, []viewSrc{srcOf("lon", true, 2, vdev("d1", true, 500))}, nil, nil)
	if n := len(got[0].Devices); n != 1 {
		t.Fatalf("одно устройство дважды: %d", n)
	}
	d := got[0].Devices[0]
	if !d.Online || !d.Here || d.Server != "main" || len(d.Conns) != 2 || d.RTTMs != 5 {
		t.Fatalf("d1: %+v", d)
	}
	// Имя — самое свежее из присланных; имя из панели важнее.
	if d.Name != "d1" || d.Display != "d1" {
		t.Fatalf("имя: %q / %q", d.Name, d.Display)
	}
	items := map[string]srvItem{devKey(kAlias, "N", "d1"): {T: 1, D: json.RawMessage(`{"alias":"Ноутбук"}`)}}
	d = mergeNetworks(local, []viewSrc{srcOf("lon", true, 2, vdev("d1", true, 500))}, items, nil)[0].Devices[0]
	if d.Display != "Ноутбук" || d.Alias != "Ноутбук" {
		t.Fatalf("имя из панели: %+v", d)
	}
}

func TestСложениеУбранноеУстройствоВСтаройКартине(t *testing.T) {
	// Убрали в 2000 мс; в старой картине London оно ещё есть (с 1000 с).
	items := map[string]srvItem{devKey(kForget, "N", "d1"): {T: 2_000_000}}
	got := mergeNetworks(localNet(), []viewSrc{srcOf("lon", false, 600, vdev("d1", true, 900))}, items, nil)
	if len(got[0].Devices) != 0 {
		t.Fatalf("убранное устройство показано: %+v", got[0].Devices)
	}
	// Вернулось после этого (meshd завёл его заново) — показываем.
	back := vdev("d1", true, 3000)
	back.FirstSeen = 2500
	got = mergeNetworks(localNet(), []viewSrc{srcOf("lon", true, 1, back)}, items, nil)
	if len(got[0].Devices) != 1 {
		t.Fatal("вернувшееся устройство не показано")
	}
	// Удалённая сеть в старой картине — тоже нет.
	items = map[string]srvItem{kNDel + "X": {T: 2_000_000}}
	src := viewSrc{ID: "lon", Age: 600, View: srvView{Networks: []viewNet{{ID: "X", Created: 100, Devices: []viewDevice{vdev("d2", true, 1)}}}}}
	if got := mergeNetworks(nil, []viewSrc{src}, items, nil); len(got) != 0 {
		t.Fatalf("удалённая сеть показана: %+v", got)
	}
}

func TestСложениеСерверБезСвязи(t *testing.T) {
	// Картине London 7 минут, связи нет: устройство серое и не на связи.
	got := mergeNetworks(localNet(), []viewSrc{srcOf("lon", false, 420, vdev("d1", true, 500))}, nil, nil)
	d := findDev(t, got, "d1")
	if d.Online || !d.Stale || d.Age != 420 || !d.Conns[0].Stale || d.Conns[0].Online {
		t.Fatalf("d1: %+v", d)
	}
	// Оно же на связи с этим сервером — на связи, серая только одна линия.
	local := localNet(meshsvc.Device{ID: "d1", Name: "d1", Online: true, LastSeen: 600})
	d = mergeNetworks(local, []viewSrc{srcOf("lon", false, 420, vdev("d1", true, 500))}, nil, nil)[0].Devices[0]
	if !d.Online || d.Stale || len(d.Conns) != 2 || d.Conns[1].Online {
		t.Fatalf("d1 здесь: %+v", d)
	}
}

func TestСложениеПользовательПоХешу(t *testing.T) {
	v := vdev("d1", true, 1)
	v.UH = userHash("tun_aaaa")
	got := mergeNetworks(nil, []viewSrc{srcOf("lon", true, 1, v)}, nil, map[string]string{userHash("tun_aaaa"): "tun_aaaa"})
	if d := findDev(t, got, "d1"); d.User != "tun_aaaa" {
		t.Fatalf("пользователь: %q", d.User)
	}
}

// ---------- два сервера целиком ----------

// twoServers — Германия и London в одной сети, устройство «ноут» только на
// Германии, «телефон» только на London.
func twoServers(t *testing.T) (*testNet, *netNode, *netNode) {
	t.Helper()
	tn := newTestNet()
	de := tn.node(t, "de.example", "Германия")
	lon := tn.node(t, "lon.example", "London")
	pc := de.client(t, "ПК")
	lon.join(t, de.invite(t))
	settle(de, lon)
	de.adm.addDevice("N", "noutbuk", pc.Username, false)
	de.adm.withDev("N", "noutbuk", func(d *meshsvc.Device) {
		d.Online, d.FirstSeen, d.LastSeen, d.App = true, 10, 20, "1.10.1"
		d.Host, d.IP = "noutbuk", "198.19.0.2"
	})
	lon.adm.addDevice("N", "telefon", pc.Username, false)
	lon.adm.withDev("N", "telefon", func(d *meshsvc.Device) {
		d.Online, d.FirstSeen, d.LastSeen, d.Platform, d.App = true, 10, 20, "android", "1.10.1"
		d.Host, d.IP = "telefon", "198.19.0.3"
	})
	settle(de, lon)
	return tn, de, lon
}

func TestОднаКартинаНаОбоихСерверах(t *testing.T) {
	_, de, lon := twoServers(t)
	for _, nd := range []*netNode{de, lon} {
		st := nd.mesh.State(context.Background())
		if len(st.Networks) != 1 || len(st.Networks[0].Devices) != 2 {
			t.Fatalf("%s: сети %+v", nd.host, st.Networks)
		}
		on := 0
		for _, d := range st.Networks[0].Devices {
			if d.Online {
				on++
			}
		}
		if on != 2 {
			t.Fatalf("%s: на связи %d из 2", nd.host, on)
		}
		// На схеме оба сервера и линия между ними.
		var names []string
		for _, s := range st.Servers {
			names = append(names, s.Name)
		}
		if len(st.Servers) != 2 || len(st.ServerLinks) != 1 || !st.ServerLinks[0].Online {
			t.Fatalf("%s: серверы %v, линии %+v", nd.host, names, st.ServerLinks)
		}
	}
	// Картина лежит на диске: после перезапуска схема не пустая.
	n2 := newServerNet(lon.mesh)
	if len(n2.views) != 1 {
		t.Fatalf("картины после перезапуска: %d", len(n2.views))
	}
}

func TestСерверБезСвязиСерыйСПоследнейКартиной(t *testing.T) {
	tn, de, lon := twoServers(t)
	tn.setDown("lon.example", true)
	tn.advance(7 * time.Minute)
	settle(de)
	st := de.mesh.State(context.Background())
	var srv MeshServer
	for _, s := range st.Servers {
		if s.Net {
			srv = s
		}
	}
	if srv.Online || !srv.Stale || srv.Age < 400 {
		t.Fatalf("London: %+v", srv)
	}
	d := findDev(t, st.Networks, "telefon")
	if d.Online || !d.Stale {
		t.Fatalf("телефон London без связи считается на связи: %+v", d)
	}
	_ = lon
}

func TestДействияСУстройствомДругогоСервера(t *testing.T) {
	_, de, lon := twoServers(t)
	ctx := context.Background()
	// На London «ноута» нет — всё делается оттуда и доходит до Германии.
	if err := lon.mesh.Rename(ctx, "N", "noutbuk", "Ноутбук"); err != nil {
		t.Fatal(err)
	}
	if err := lon.mesh.Freeze(ctx, "N", "noutbuk", true); err != nil {
		t.Fatal(err)
	}
	settle(de, lon)
	d, _ := de.adm.dev("N", "noutbuk")
	if d.Alias != "Ноутбук" || !d.Frozen {
		t.Fatalf("на Германии: %+v", d)
	}
	if got := findDev(t, lon.mesh.State(ctx).Networks, "noutbuk"); got.Display != "Ноутбук" || !got.Frozen {
		t.Fatalf("на London: %+v", got)
	}
	// Убрать — с обоих, и в картине London его больше нет.
	if err := lon.mesh.Forget(ctx, "N", "noutbuk"); err != nil {
		t.Fatal(err)
	}
	settle(de, lon)
	if _, ok := de.adm.dev("N", "noutbuk"); ok {
		t.Fatal("на Германии не убрано")
	}
	for _, nd := range []*netNode{de, lon} {
		for _, d := range nd.mesh.State(ctx).Networks[0].Devices {
			if d.ID == "noutbuk" {
				t.Fatalf("%s: убранное видно", nd.host)
			}
		}
	}
}

type exitAdmin struct {
	*memAdmin
	got []string
}

func (a *exitAdmin) SetExit(_ context.Context, netID, dev, entry string, via []string) (meshsvc.ExitOrder, error) {
	if _, ok := a.dev(netID, dev); !ok {
		return meshsvc.ExitOrder{}, io.EOF
	}
	a.got = append(a.got, dev+">"+entry)
	return meshsvc.ExitOrder{Entry: entry, Via: via, T: 1}, nil
}

func TestЦепочкаВыходаЧерезДругойСервер(t *testing.T) {
	_, de, lon := twoServers(t)
	ea := &exitAdmin{memAdmin: de.adm}
	de.mesh.admin = ea
	o, err := lon.mesh.SetExit(context.Background(), "N", "noutbuk", "de.example:22", []string{"uk.example:22"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Entry != "de.example:22" || len(ea.got) != 1 {
		t.Fatalf("цепочка не дошла до Германии: %+v %v", o, ea.got)
	}
}

func TestПереименованиеСервераОбщее(t *testing.T) {
	_, de, lon := twoServers(t)
	ctx := context.Background()
	if err := de.n.RenameServer(ctx, lon.n.SelfID(), "Англия"); err != nil {
		t.Fatal(err)
	}
	settle(de, lon)
	settle(de, lon)
	if got := lon.sets.Get().Mesh.Name; got != "Англия" {
		t.Fatalf("London сам не взял имя: %q", got)
	}
	for _, nd := range []*netNode{de, lon} {
		found := false
		for _, s := range nd.mesh.State(ctx).ServerNet.Servers {
			if s.Name == "Англия" {
				found = true
			}
			if s.Name == "London" {
				t.Fatalf("%s: старое имя осталось", nd.host)
			}
		}
		if !found {
			t.Fatalf("%s: нового имени нет", nd.host)
		}
	}
	if err := de.n.RenameServer(ctx, "нет-такого", "x"); err == nil {
		t.Fatal("неизвестный сервер переименован")
	}
}

// ---------- /srv/v1/view ----------

func postPath(t *testing.T, addr, path string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+path, strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func postView(t *testing.T, addr string, h map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/srv/v1/view", strings.NewReader(`{}`))
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestКартинаТолькоПоПодписи(t *testing.T) {
	tn, de, lon := twoServers(t)
	addr := tn.addrs["de.example"]
	netID := de.mesh.State(context.Background()).ServerNet.Net
	from := lon.n.SelfID()
	if c := postView(t, addr, nil); c != http.StatusForbidden {
		t.Fatalf("без подписи: %d", c)
	}
	sign := func(s ssh.Signer, at time.Time) map[string]string {
		req, _ := http.NewRequest(http.MethodPost, "http://x/srv/v1/view", nil)
		if err := signRequest(req, s, netID, from, []byte(`{}`), at); err != nil {
			t.Fatal(err)
		}
		return map[string]string{"X-Srv-Net": netID, "X-Srv-From": from,
			"X-Srv-Time": req.Header.Get("X-Srv-Time"), "X-Srv-Sig": req.Header.Get("X-Srv-Sig")}
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	foreign, _ := ssh.NewSignerFromKey(priv)
	if c := postView(t, addr, sign(foreign, tn.clock())); c != http.StatusForbidden {
		t.Fatalf("чужой ключ: %d", c)
	}
	lon.n.mu.Lock()
	own, _ := lon.n.keyLocked()
	lon.n.mu.Unlock()
	if c := postView(t, addr, sign(own, tn.clock().Add(-time.Hour))); c != http.StatusForbidden {
		t.Fatalf("старый запрос: %d", c)
	}
	if c := postView(t, addr, sign(own, tn.clock())); c != http.StatusOK {
		t.Fatalf("свой ключ: %d", c)
	}
}

func TestВКартинеНетСекретов(t *testing.T) {
	_, de, _ := twoServers(t)
	data, _ := json.Marshal(de.n.buildView(context.Background()))
	low := strings.ToLower(string(data))
	for _, bad := range []string{"ssh-", "tun_", "password", "key"} {
		if strings.Contains(low, bad) {
			t.Fatalf("в картине %q: %s", bad, data)
		}
	}
	if !strings.Contains(string(data), "noutbuk") {
		t.Fatalf("в картине нет устройства: %s", data)
	}
}

// ---------- причины разрыва (п. 12) ----------

// London запущен с -ssh-host london.mesh: в сеть серверов этот адрес не
// уходит, устройствам его не советуют.
func TestАдресMeshНеУходитВСетьСерверов(t *testing.T) {
	tn := newTestNet()
	de := tn.node(t, "de.example", "Германия")
	lon := tn.node(t, "lon.example", "London")
	lon.sys.addrs = []string{"10.0.0.5", "203.0.113.7", "198.18.0.9"}
	lon.mesh.sshHost = "london.mesh"
	lon.n.mu.Lock()
	signer, _ := lon.n.keyLocked()
	lon.n.mu.Unlock()
	if r := lon.n.selfRec(signer, ""); r.Host != "203.0.113.7" {
		t.Fatalf("адрес London для сети: %q", r.Host)
	}
	if st := lon.mesh.State(context.Background()).ServerNet; st.SSHHostInside != "london.mesh" {
		t.Fatalf("панель не сказала про -ssh-host: %+v", st)
	}
	// Запись от необновлённой панели (london.mesh) устройствам не советуется.
	lon.mesh.sshHost = "lon.example"
	lon.join(t, de.invite(t))
	settle(de, lon)
	de.n.mu.Lock()
	id := lon.n.SelfID()
	rec, _ := decodeItem[srvRec](de.n.f.Items[kSrv+id])
	rec.Host = "london.mesh"
	de.n.f.Items[kSrv+id] = srvItem{T: de.n.f.Items[kSrv+id].T, By: id, D: mustJSON(rec)}
	de.n.mu.Unlock()
	de.n.lastKnwn = ""
	de.n.reconcile(context.Background())
	de.adm.mu.Lock()
	known := de.adm.known
	de.adm.mu.Unlock()
	for _, k := range known {
		if k.Host == "london.mesh" {
			t.Fatalf("устройствам советуется london.mesh: %+v", known)
		}
	}
	var bad bool
	for _, s := range de.mesh.State(context.Background()).ServerNet.Servers {
		if s.Host == "london.mesh" && s.HostBad {
			bad = true
		}
	}
	if !bad {
		t.Fatal("панель не пометила плохой адрес London")
	}
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func TestDeviceReachable(t *testing.T) {
	for h, want := range map[string]bool{"london.mesh": false, "LONDON.MESH.": false, "198.18.0.2": false,
		"198.19.0.7": false, "127.0.0.1": false, "::1": false, "89.125.168.26": true, "uk.example": true,
		"192.168.1.10": true, "London": false, "localhost.": false, "uk": false} {
		if got := deviceReachable(h); got != want {
			t.Errorf("%s: %v", h, got)
		}
	}
}

// ---------- почему не на связи (п. 13) ----------

func TestПричиныНеНаСвязи(t *testing.T) {
	user := "tun_aaaa"
	base := MeshDevice{Device: meshsvc.Device{ID: "d", User: user, App: "1.10.1", Platform: "windows"},
		Conns: []DevConn{{Server: "main", Online: true}}}
	main := whySrv{ID: "main", Host: "89.125.168.26", Online: true, Meshd: "6", Accts: map[string]bool{userHash(user): true}}
	lon := whySrv{ID: "lon", Host: "203.0.113.7", Online: true, Meshd: "6", Accts: map[string]bool{userHash(user): true}}
	code := func(d MeshDevice, s ...whySrv) string {
		m := missingServers(d, s)
		if len(m) != 1 || m[0].Server != "lon" {
			t.Fatalf("missing: %+v", m)
		}
		return m[0].Code
	}
	cases := []struct {
		name string
		dev  func(*MeshDevice)
		lon  func(*whySrv)
		main func(*whySrv)
		want string
	}{
		{"сервер не на связи", nil, func(s *whySrv) { s.Online = false }, nil, "server-down"},
		{"старый meshd на сервере устройства", nil, nil, func(s *whySrv) { s.Meshd = "5" }, "old-meshd"},
		{"адрес london.mesh", nil, func(s *whySrv) { s.Host = "london.mesh" }, nil, "bad-host"},
		{"учётной записи нет", nil, func(s *whySrv) { s.Accts = map[string]bool{} }, nil, "no-account"},
		{"профиль есть, не подключён", func(d *MeshDevice) { d.Profiles = []meshsvc.ProfileRef{{Key: "203.0.113.7:22"}} }, nil, nil, "profile-off"},
		{"старая программа", func(d *MeshDevice) { d.App = "1.9.16" }, nil, nil, "old-app"},
		{"телефон", func(d *MeshDevice) { d.Platform = "android" }, nil, nil, "phone"},
		{"не знаем", nil, nil, nil, "unknown"},
	}
	for _, c := range cases {
		d, l, m := base, lon, main
		if c.dev != nil {
			c.dev(&d)
		}
		if c.lon != nil {
			c.lon(&l)
		}
		if c.main != nil {
			c.main(&m)
		}
		if got := code(d, m, l); got != c.want {
			t.Errorf("%s: %s, а нужно %s", c.name, got, c.want)
		}
	}
	if !versionLess("1.9.16", "1.10.1") || versionLess("v1.10.1", "1.10.1") || versionLess("1.11.0-beta", "1.10.1") {
		t.Fatal("сравнение версий")
	}
}

// Две панели целиком: устройство Германии, у чьей учётной записи на London
// записи нет, получает причину «учётная запись на London не создана».
func TestПричинаНетУчётнойЗаписиСДвумяСерверами(t *testing.T) {
	_, de, lon := twoServers(t)
	// Учётная запись «чужого» пользователя есть только в meshd Германии.
	de.adm.addDevice("N", "gost", "tun_0000000000000001", false)
	de.adm.withDev("N", "gost", func(d *meshsvc.Device) { d.Online, d.App = true, "1.10.1" })
	settle(de, lon)
	d := findDev(t, de.mesh.State(context.Background()).Networks, "gost")
	if len(d.Missing) != 1 || d.Missing[0].Code != "no-account" || d.Missing[0].Server != lon.n.SelfID() {
		t.Fatalf("gost: %+v", d.Missing)
	}
	// Телефон London на Германию сам не придёт: причина «телефон».
	p := findDev(t, de.mesh.State(context.Background()).Networks, "telefon")
	if len(p.Missing) != 1 || p.Missing[0].Server != "main" || p.Missing[0].Code != "phone" {
		t.Fatalf("telefon: %+v", p.Missing)
	}
}
