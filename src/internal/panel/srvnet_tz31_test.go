package panel

// ТЗ 31: все серверы сети одинаковые — состояние устройства, «Подключённые
// клиенты» и «Проверка» по общей картине сети; «Обновить на всех серверах».

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/meshsvc"
)

// ---------- A. одно состояние устройства ----------

// Телефон сидит через London (58 мс); на Германии от него осталась старая
// запись без связи — как MOBILA на Amsterdam 20.11.
func TestУстройствоНаСвязиТолькоСДругимСервером(t *testing.T) {
	_, de, lon := twoServers(t)
	lon.adm.withDev("N", "telefon", func(d *meshsvc.Device) {
		d.RTTMs, d.Quality, d.ConnectedAt, d.LastSeen, d.App = 58, "good", 1000, 2000, "1.10.6"
	})
	// Старая запись на Германии: та же учётная запись, давно не на связи.
	user, _ := lon.adm.dev("N", "telefon")
	de.adm.addDevice("N", "telefon", user.User, false)
	de.adm.withDev("N", "telefon", func(d *meshsvc.Device) {
		d.Online, d.LastSeen, d.App, d.Platform = false, 500, "1.10.1", "android"
	})
	settle(de, lon)

	st := de.mesh.State(context.Background())
	d := findDev(t, st.Networks, "telefon")
	lonID := lon.n.SelfID()
	if !d.Online || d.Here {
		t.Fatalf("на связи через London, а на Германии: online=%v here=%v", d.Online, d.Here)
	}
	if d.Server != lonID || d.RTTMs != 58 || d.Quality != "good" || d.ConnectedAt != 1000 {
		t.Fatalf("карточка не от London: server=%q rtt=%v q=%q since=%d", d.Server, d.RTTMs, d.Quality, d.ConnectedAt)
	}
	if d.LastSeen != 2000 || d.App != "1.10.6" {
		t.Fatalf("последний раз / версия: %d %q", d.LastSeen, d.App)
	}

	// «Подключённые клиенты»: тот же клиент — на связи через London.
	cn := clientNetOf(st)[user.User]
	if !cn.OnlineAny || cn.ViaServerID != lonID || cn.ViaServer != "London" || cn.ViaRTTMs != 58 || cn.LastSeenAny != 2000 {
		t.Fatalf("клиент: %+v", cn)
	}
	list := withNet([]Client{{ID: user.User, Username: user.User}, {ID: "tun_nobody", Username: "tun_nobody"}},
		clientNetOf(st), "Германия")
	if !list[0].OnlineAny || list[1].OnlineAny || list[1].LastSeenAny != 0 {
		t.Fatalf("список клиентов: %+v", list)
	}
	// В JSON — плоско, рядом с полями клиента.
	raw, _ := json.Marshal(list[0])
	for _, k := range []string{`"onlineAny":true`, `"viaServer":"London"`, `"lastSeenAny":2000`, `"username"`} {
		if !strings.Contains(string(raw), k) {
			t.Fatalf("нет %s в %s", k, raw)
		}
	}
}

func TestПричинаТелефонСНовойПрограммой(t *testing.T) {
	d := MeshDevice{Device: meshsvc.Device{ID: "p", Platform: "android", App: "1.10.6", User: "tun_a"},
		Conns: []DevConn{{Server: "main", Online: true}}}
	srv := []whySrv{{ID: "main", Host: "89.125.168.26", Online: true, Meshd: "7"},
		{ID: "lon", Host: "203.0.113.7", Online: true, Meshd: "7"}}
	if m := missingServers(d, srv); len(m) != 1 || m[0].Code != "phone-list" {
		t.Fatalf("v1.10.6: %+v", m)
	}
	d.App = "1.10.5"
	if m := missingServers(d, srv); len(m) != 1 || m[0].Code != "phone" {
		t.Fatalf("v1.10.5: %+v", m)
	}
}

// ---------- B. «Проверка» с любого сервера ----------

// checkAdmin — meshd в памяти, который отвечает на проверку как настоящий:
// только про свои устройства, по своей политике.
type checkAdmin struct{ *memAdmin }

func (a *checkAdmin) Check(_ context.Context, netID, from, to string, port int) (meshsvc.CheckResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := a.nets[netID]
	if n == nil {
		return meshsvc.CheckResult{}, errorsNew("нет такой сети")
	}
	f, t := n.devs[from], n.devs[to]
	if f == nil || t == nil {
		return meshsvc.CheckResult{Rule: -1, Reason: checkUnknown}, nil
	}
	// Своя, независимая от панели запись правила meshd.
	member := func(user, g string) bool {
		if g == "*" {
			return true
		}
		for _, x := range a.policy.Users[user] {
			if x == g {
				return true
			}
		}
		return false
	}
	res := meshsvc.CheckResult{Rule: -1, Visible: !a.policy.Enabled}
	for _, r := range a.policy.Rules {
		if member(f.User, r.From) && member(t.User, r.To) || member(t.User, r.From) && member(f.User, r.To) {
			res.Visible = true
		}
	}
	if !a.policy.Enabled {
		res.Allowed, res.Reason = true, "policy off"
		return res, nil
	}
	for i, r := range a.policy.Rules {
		if member(f.User, r.From) && member(t.User, r.To) && (r.Ports == "" || port > 0 && meshsvc.PortAllowed(r.Ports, port)) {
			res.Rule, res.Allowed, res.Reason = i, true, "rule"
			return res, nil
		}
	}
	res.Reason = "no rule"
	return res, nil
}

type textError string

func (e textError) Error() string { return string(e) }
func errorsNew(s string) error    { return textError(s) }

func TestПроверкаНаЛюбомСервере(t *testing.T) {
	_, de, lon := twoServers(t)
	ctx := context.Background()
	for _, nd := range []*netNode{de, lon} {
		nd.mesh.admin = &checkAdmin{nd.adm}
	}
	// Второе устройство тоже только на London: на Германии нет ни одного.
	pc := de.client(t, "Работа")
	settle(de, lon)
	lon.adm.addDevice("N", "rabota", pc.Username, false)
	lon.adm.withDev("N", "rabota", func(d *meshsvc.Device) { d.Online, d.FirstSeen, d.LastSeen = true, 10, 20 })
	tel, _ := lon.adm.dev("N", "telefon")
	de.setAccess(t, AccessSettings{Enabled: true, Groups: []string{"дом"}, Rules: []AccessRule{{From: "дом", To: "*", Ports: "22"}}})
	if err := de.srv.clients.Update(tel.User, func(c *Client) { c.Groups = []string{"дом"} }); err != nil {
		t.Fatal(err)
	}
	settle(de, lon)
	for _, nd := range []*netNode{de, lon} {
		if err := nd.srv.pushPolicy(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		from, to string
		port     int
	}{{"telefon", "rabota", 22}, {"telefon", "rabota", 80}, {"rabota", "telefon", 22}, {"telefon", "noutbuk", 22}} {
		onLon, err := lon.mesh.Check(ctx, "N", c.from, c.to, c.port)
		if err != nil {
			t.Fatal(err)
		}
		onDe, err := de.mesh.Check(ctx, "N", c.from, c.to, c.port)
		if err != nil {
			t.Fatal(err)
		}
		if onDe != onLon {
			t.Fatalf("%s → %s:%d: на Германии %+v, на London %+v", c.from, c.to, c.port, onDe, onLon)
		}
		if onDe.Reason == checkUnknown {
			t.Fatalf("%s → %s: «не найдено», хотя устройства есть в сети", c.from, c.to)
		}
	}
	if r, _ := de.mesh.Check(ctx, "N", "telefon", "rabota", 22); !r.Allowed || r.Rule != 0 {
		t.Fatalf("telefon → rabota:22: %+v", r)
	}
	// Никто в сети не знает — «не найдено».
	if r, _ := de.mesh.Check(ctx, "N", "telefon", "nikto", 22); r.Reason != checkUnknown {
		t.Fatalf("неизвестное: %+v", r)
	}
	// Замороженное (на любом сервере сети) — как в meshd.
	if err := de.mesh.Freeze(ctx, "N", "rabota", true); err != nil {
		t.Fatal(err)
	}
	settle(de, lon)
	if r, _ := de.mesh.Check(ctx, "N", "telefon", "rabota", 22); r.Allowed || r.Reason != "frozen" {
		t.Fatalf("замороженное: %+v", r)
	}
}

// ---------- C. обновить на всех серверах ----------

// fakeUpd — подделка самообновления одного сервера: «ставит» версию сразу.
type fakeUpd struct {
	mu      sync.Mutex
	ver     string
	meshdOK bool
	calls   int
	meshd   int
	order   *[]string
	name    string
	orderMu *sync.Mutex
}

func (f *fakeUpd) attach(nd *netNode, target string) {
	nd.n.updLoopOff = true
	nd.n.panelVer = func() string { f.mu.Lock(); defer f.mu.Unlock(); return f.ver }
	nd.n.startUpdate = func() error {
		f.mu.Lock()
		f.calls++
		f.ver, f.meshdOK = target, false
		f.mu.Unlock()
		f.orderMu.Lock()
		*f.order = append(*f.order, f.name)
		f.orderMu.Unlock()
		return nil
	}
	nd.n.updProgress = func() (bool, string, string) { return false, "", "" }
	nd.n.meshdLatest = func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.meshdOK }
	nd.n.meshdUpdate = func() error { f.mu.Lock(); f.meshd++; f.meshdOK = true; f.mu.Unlock(); return nil }
}

func (f *fakeUpd) state() (calls, meshd int, ver string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.meshd, f.ver
}

// tickAll — круги обмена и проверки обновления на серверах.
func tickAll(nodes ...*netNode) {
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		for _, nd := range nodes {
			nd.n.updTick(ctx)
			nd.n.syncAll(ctx)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestОбновитьНаВсехСерверах(t *testing.T) {
	tn := newTestNet()
	de := tn.node(t, "de.example", "Frankfurt")
	lon := tn.node(t, "lon.example", "London")
	ams := tn.node(t, "ams.example", "Amsterdam")
	lon.join(t, de.invite(t))
	ams.join(t, de.invite(t))
	settle(de, lon, ams)

	const target = "10.0.0"
	var order []string
	var omu sync.Mutex
	fakes := map[string]*fakeUpd{}
	for _, nd := range []*netNode{de, lon, ams} {
		f := &fakeUpd{ver: "9.9.9", meshdOK: true, order: &order, orderMu: &omu, name: nd.host}
		f.attach(nd, target)
		fakes[nd.host] = f
	}
	tn.setDown("ams.example", true)
	tn.advance(2 * time.Minute)
	settle(de, lon)

	ctx := context.Background()
	if err := de.n.UpdateAll(ctx, target); err != nil {
		t.Fatal(err)
	}
	// Запрос London уходит сразу, в фоне; этот сервер ждёт.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, _, _ := fakes["lon.example"].state(); c > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c, _, _ := fakes["de.example"].state(); c != 0 {
		t.Fatal("сервер, на котором нажали, обновился не последним")
	}
	for i := 0; i < 5; i++ {
		tickAll(de, lon)
	}
	for _, h := range []string{"de.example", "lon.example"} {
		calls, meshd, ver := fakes[h].state()
		if calls != 1 || meshd != 1 || ver != target {
			t.Fatalf("%s: панель %d раз, meshd %d раз, версия %s", h, calls, meshd, ver)
		}
	}
	if order[0] != "lon.example" || order[len(order)-1] != "de.example" {
		t.Fatalf("порядок: %v", order)
	}
	if c, _, _ := fakes["ams.example"].state(); c != 0 {
		t.Fatal("сервер без связи обновился")
	}
	// На странице: London готов, Amsterdam «обновится, когда вернётся».
	st := de.n.UpdateAllState(ctx)
	got := map[string]string{}
	for _, s := range st.Servers {
		got[s.Name] = s.Stage
	}
	if st.Target != target || got["Frankfurt"] != updDone || got["London"] != updDone || got["Amsterdam"] != "offline" {
		t.Fatalf("ход: %s %v", st.Target, got)
	}

	// Amsterdam вернулся: запись приходит обменом, он обновляется сам.
	tn.setDown("ams.example", false)
	for i := 0; i < 3; i++ {
		tickAll(de, lon, ams)
	}
	ams.n.mu.Lock()
	_, have := ams.n.f.Items[kUpd+target]
	ams.n.mu.Unlock()
	calls, meshd, ver := fakes["ams.example"].state()
	if !have || calls != 1 || meshd != 1 || ver != target {
		t.Fatalf("Amsterdam после возвращения: запись %v, панель %d, meshd %d, версия %s", have, calls, meshd, ver)
	}
	// Повторные круги ничего не запускают заново.
	tickAll(de, lon, ams)
	for h, f := range fakes {
		if c, m, _ := f.state(); c != 1 || m != 1 {
			t.Fatalf("%s: повтор (%d, %d)", h, c, m)
		}
	}
}

// Только новее своей версии: просьба обновиться до старой — ничего.
func TestОбновлениеТолькоНаНовуюВерсию(t *testing.T) {
	_, de, lon := twoServers(t)
	var order []string
	var omu sync.Mutex
	f := &fakeUpd{ver: "9.9.9", meshdOK: true, order: &order, orderMu: &omu, name: "lon"}
	f.attach(lon, "9.9.9")
	g := &fakeUpd{ver: "9.9.9", meshdOK: true, order: &order, orderMu: &omu, name: "de"}
	g.attach(de, "9.9.9")
	for _, v := range []string{"9.9.9", "1.0.0", "9.9.8"} {
		if err := de.n.UpdateAll(context.Background(), v); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		tickAll(de, lon)
	}
	if c, _, _ := f.state(); c != 0 {
		t.Fatalf("London обновился на старую версию (%d)", c)
	}
	if c, _, _ := g.state(); c != 0 {
		t.Fatalf("Германия обновилась на старую версию (%d)", c)
	}
	if err := de.n.UpdateAll(context.Background(), "x; rm -rf /"); err == nil {
		t.Fatal("мусор вместо версии принят")
	}
	for in, want := range map[string]string{"v1.10.7": "1.10.7", "1.10.7-beta": "1.10.7-beta", "../1": "", "1..2": "",
		"1.2.3 && x": "", "": ""} {
		if got := cleanVersion(in); got != want {
			t.Errorf("cleanVersion(%q) = %q, а нужно %q", in, got, want)
		}
	}
	if validItemKey(kUpd+"../x") || !validItemKey(kUpd+"1.10.7") {
		t.Fatal("ключ upd: проверяется не так")
	}
}

// /srv/v1/update — только подписанный запрос сервера этой сети.
func TestОбновлениеТолькоПоПодписи(t *testing.T) {
	tn, de, lon := twoServers(t)
	var order []string
	var omu sync.Mutex
	f := &fakeUpd{ver: "9.9.9", meshdOK: true, order: &order, orderMu: &omu, name: "de"}
	f.attach(de, "10.0.0")
	addr := tn.addrs["de.example"]
	netID := de.mesh.State(context.Background()).ServerNet.Net
	from := lon.n.SelfID()
	item := srvItem{T: tn.clock().UnixMilli(), By: from, D: mustJSON(updRec{Target: "10.0.0"})}
	body, _ := json.Marshal(updReq{Items: map[string]srvItem{kUpd + "10.0.0": item}})
	post := func(h map[string]string) int {
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/srv/v1/update", strings.NewReader(string(body)))
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
	sign := func(s ssh.Signer) map[string]string {
		req, _ := http.NewRequest(http.MethodPost, "http://x/srv/v1/update", nil)
		if err := signRequest(req, s, netID, from, body, tn.clock()); err != nil {
			t.Fatal(err)
		}
		return map[string]string{"X-Srv-Net": netID, "X-Srv-From": from,
			"X-Srv-Time": req.Header.Get("X-Srv-Time"), "X-Srv-Sig": req.Header.Get("X-Srv-Sig")}
	}
	if c := post(nil); c != http.StatusForbidden {
		t.Fatalf("без подписи: %d", c)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	foreign, _ := ssh.NewSignerFromKey(priv)
	if c := post(sign(foreign)); c != http.StatusForbidden {
		t.Fatalf("чужой ключ: %d", c)
	}
	time.Sleep(50 * time.Millisecond)
	if c, _, _ := f.state(); c != 0 {
		t.Fatal("обновился по неподписанному запросу")
	}
	lon.n.mu.Lock()
	own, _ := lon.n.keyLocked()
	lon.n.mu.Unlock()
	if c := post(sign(own)); c != http.StatusOK {
		t.Fatalf("свой ключ: %d", c)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, _, _ := f.state(); c == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("по подписанному запросу не обновился")
}

// Режим показа: «Обновить на всех» с выдуманным ходом.
func TestДемоОбновитьНаВсех(t *testing.T) {
	h := NewDemoHandler("1.10.6")
	_, out := demoCall(t, h, http.MethodGet, "/api/update/all", nil)
	if out["target"] != nil || len(out["servers"].([]any)) < 2 {
		t.Fatalf("до нажатия: %v", out)
	}
	_, out = demoCall(t, h, http.MethodPost, "/api/update/all", map[string]any{})
	if out["target"] != "1.10.7" {
		t.Fatalf("нажали: %v", out)
	}
	_, out = demoCall(t, h, http.MethodGet, "/api/update/all", nil)
	stages := map[string]string{}
	for _, s := range out["servers"].([]any) {
		m := s.(map[string]any)
		stages[m["name"].(string)] = m["stage"].(string)
	}
	if stages["Амстердам"] != "offline" || stages["Лондон"] != "download" || stages["Франкфурт"] != updWaiting {
		t.Fatalf("ход: %v", stages)
	}
}
