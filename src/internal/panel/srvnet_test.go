package panel

// Сеть серверов (ТЗ 22): два и три сервера в одном процессе. Обмен идёт по
// настоящему HTTP на 127.0.0.1, вместо SSH — подделка, которая пускает
// только ключи из authorized_keys пользователя meshlink этого сервера (как
// sshd), meshd — подделка в памяти.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/meshsvc"
)

// ---------- meshd в памяти ----------

type memNet struct {
	name     string
	approval bool
	devs     map[string]*meshsvc.Device
}

type memAdmin struct {
	mu     sync.Mutex
	id     string
	nets   map[string]*memNet
	known  []meshsvc.KnownServer
	shared []string
	policy meshsvc.Policy
}

func newMemAdmin(id string) *memAdmin { return &memAdmin{id: id, nets: map[string]*memNet{}} }

// addDevice — устройство пришло на этот сервер.
func (a *memAdmin) addDevice(netID, dev, user string, pending bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := a.nets[netID]
	if n == nil {
		n = &memNet{devs: map[string]*meshsvc.Device{}}
		a.nets[netID] = n
	}
	n.devs[dev] = &meshsvc.Device{ID: dev, Name: dev, User: user, Pending: pending}
}

func (a *memAdmin) dev(netID, dev string) (meshsvc.Device, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := a.nets[netID]; n != nil {
		if d := n.devs[dev]; d != nil {
			return *d, true
		}
	}
	return meshsvc.Device{}, false
}

func (a *memAdmin) net(netID string) (memNet, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := a.nets[netID]; n != nil {
		return *n, true
	}
	return memNet{}, false
}

func (a *memAdmin) State(context.Context) (meshsvc.State, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := meshsvc.State{Version: "6", Self: meshsvc.Self{ID: a.id, Name: "x"}}
	for id, n := range a.nets {
		nw := meshsvc.Network{ID: id, Name: n.name, Approval: n.approval}
		for _, d := range n.devs {
			nw.Devices = append(nw.Devices, *d)
		}
		st.Networks = append(st.Networks, nw)
	}
	return st, nil
}

func (a *memAdmin) withDev(netID, dev string, f func(*meshsvc.Device)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := a.nets[netID]; n != nil {
		if d := n.devs[dev]; d != nil {
			f(d)
			return nil
		}
	}
	return errors.New("нет такого устройства")
}

func (a *memAdmin) Rename(_ context.Context, netID, dev, alias string) error {
	return a.withDev(netID, dev, func(d *meshsvc.Device) { d.Alias = alias })
}
func (a *memAdmin) Forget(_ context.Context, netID, dev string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := a.nets[netID]; n != nil {
		delete(n.devs, dev)
	}
	return nil
}
func (a *memAdmin) SetSelf(context.Context, string, bool, string) error { return nil }
func (a *memAdmin) ForgetServer(context.Context, string) error         { return nil }
func (a *memAdmin) SetKnown(_ context.Context, l []meshsvc.KnownServer) error {
	return a.SetKnownShared(context.Background(), l, a.shared)
}
func (a *memAdmin) SetKnownShared(_ context.Context, l []meshsvc.KnownServer, shared []string) error {
	a.mu.Lock()
	a.known, a.shared = l, shared
	a.mu.Unlock()
	return nil
}
func (a *memAdmin) SetSelfIP(context.Context, string) error { return nil }
func (a *memAdmin) SetPolicy(_ context.Context, p meshsvc.Policy) error {
	a.mu.Lock()
	a.policy = p
	a.mu.Unlock()
	return nil
}
func (a *memAdmin) RenameNetwork(_ context.Context, netID, name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := a.nets[netID]; n != nil {
		n.name = name
		return nil
	}
	return errors.New("нет такой сети")
}
func (a *memAdmin) DeleteNetwork(_ context.Context, netID string) error {
	a.mu.Lock()
	delete(a.nets, netID)
	a.mu.Unlock()
	return nil
}
func (a *memAdmin) Events(context.Context, int64, int) (meshsvc.Events, error) {
	return meshsvc.Events{}, nil
}
func (a *memAdmin) Check(context.Context, string, string, string, int) (meshsvc.CheckResult, error) {
	return meshsvc.CheckResult{}, nil
}
func (a *memAdmin) SetApproval(_ context.Context, netID string, on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := a.nets[netID]; n != nil {
		n.approval = on
		return nil
	}
	return errors.New("нет такой сети")
}
func (a *memAdmin) Approve(_ context.Context, netID, dev string) error {
	return a.withDev(netID, dev, func(d *meshsvc.Device) { d.Pending = false })
}
func (a *memAdmin) Freeze(_ context.Context, netID, dev string, on bool) error {
	return a.withDev(netID, dev, func(d *meshsvc.Device) { d.Frozen = on })
}
func (a *memAdmin) SetExit(context.Context, string, string, string, []string) (meshsvc.ExitOrder, error) {
	return meshsvc.ExitOrder{}, nil
}

// ---------- стенд ----------

type testNet struct {
	mu    sync.Mutex
	addrs map[string]string
	down  map[string]bool
	sys   map[string]*fakeMeshSystem
	now   time.Time
}

func newTestNet() *testNet {
	return &testNet{addrs: map[string]string{}, down: map[string]bool{}, sys: map[string]*fakeMeshSystem{},
		now: time.Now()}
}

func (tn *testNet) clock() time.Time {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	return tn.now
}

func (tn *testNet) advance(d time.Duration) {
	tn.mu.Lock()
	tn.now = tn.now.Add(d)
	tn.mu.Unlock()
}

func (tn *testNet) setDown(host string, down bool) {
	tn.mu.Lock()
	tn.down[host] = down
	tn.mu.Unlock()
}

// dial — как SSH: сервер должен быть на связи, его ключ — тот, что в
// записи, а наш ключ входа — в его authorized_keys у meshlink.
func (tn *testNet) dial(_ context.Context, rec srvRec, login ssh.Signer) (net.Conn, error) {
	tn.mu.Lock()
	addr, down, sys := tn.addrs[rec.Host], tn.down[rec.Host], tn.sys[rec.Host]
	tn.mu.Unlock()
	if addr == "" || down {
		return nil, errors.New("dial tcp: connection timed out")
	}
	if !slices.Contains(rec.HostKeys, sys.HostKeys()[0]) {
		return nil, errors.New("SSH-ключ сервера не тот")
	}
	sys.mu.Lock()
	keys := strings.Join(sys.linkKeys, "\n")
	sys.mu.Unlock()
	if !strings.Contains(keys, pubLine(login.PublicKey())) {
		return nil, errors.New("ssh: handshake failed: ssh: unable to authenticate")
	}
	return net.Dial("tcp", addr)
}

type netNode struct {
	host  string
	srv   *Server
	mesh  *MeshManager
	n     *ServerNet
	adm   *memAdmin
	sys   *fakeMeshSystem
	prov  *fakeProvisioner
	sets  *SettingsStore
	store *ClientStore
}

func (tn *testNet) node(t *testing.T, host, name string) *netNode {
	t.Helper()
	dir := t.TempDir()
	users, err := OpenStore(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := OpenClientStore(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	prov := newFakeProvisioner()
	srv := NewServer(users, NewClientManager(cs, prov))
	sets, err := OpenSettings(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	sets.Update(func(s *Settings) error { s.Mesh.Role, s.Mesh.Name = MeshRoleMain, name; return nil })
	sys := newFakeMeshSystem()
	sys.units[meshdUnit] = UnitState{Installed: true, Active: true, Enabled: true}
	adm := newMemAdmin(randomHex(8))
	m := NewMeshManager(sets, sys, adm, dir, "9.9.9").WithSSH(host, 22)
	srv.WithSettings(sets).WithMesh(m)
	n := m.net
	// Сервер без связи не достаёт и сам ни до кого.
	n.dial = func(ctx context.Context, rec srvRec, login ssh.Signer) (net.Conn, error) {
		tn.mu.Lock()
		down := tn.down[host]
		tn.mu.Unlock()
		if down {
			return nil, errors.New("dial tcp: network is unreachable")
		}
		return tn.dial(ctx, rec, login)
	}
	n.now, n.listen = tn.clock, "127.0.0.1:0"
	if err := n.startListener(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.stopListener)
	tn.mu.Lock()
	tn.addrs[host] = n.ln.Addr().String()
	tn.sys[host] = sys
	tn.mu.Unlock()
	return &netNode{host: host, srv: srv, mesh: m, n: n, adm: adm, sys: sys, prov: prov, sets: sets, store: cs}
}

// settle — несколько кругов обмена и сверки на всех серверах.
func settle(nodes ...*netNode) {
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		for _, nd := range nodes {
			nd.n.syncAll(ctx)
			nd.n.reconcile(ctx)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func waitJob(t *testing.T, m *MeshManager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if j := m.jobSnapshot(); j != nil && !j.Running {
			if j.Error != "" {
				t.Fatalf("задание: %s\n%s", j.Error, strings.Join(j.Lines, "\n"))
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("задание не закончилось")
}

func (nd *netNode) join(t *testing.T, code string) {
	t.Helper()
	if err := nd.n.Join(code); err != nil {
		t.Fatal(err)
	}
	waitJob(t, nd.mesh)
}

func (nd *netNode) invite(t *testing.T) string {
	t.Helper()
	code, _, err := nd.n.Invite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func (nd *netNode) client(t *testing.T, name string) Client {
	t.Helper()
	c, err := nd.srv.clients.CreateClient(name, DeviceWindows)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (nd *netNode) hasUser(user string) (Client, bool) { return nd.store.Get(user) }

func (nd *netNode) serverIDs() []string {
	var out []string
	for _, s := range nd.mesh.State(context.Background()).ServerNet.Servers {
		out = append(out, s.Name)
	}
	slices.Sort(out)
	return out
}

func (nd *netNode) setAccess(t *testing.T, a AccessSettings) {
	t.Helper()
	if err := nd.sets.Update(func(s *Settings) error { s.Access = a; return nil }); err != nil {
		t.Fatal(err)
	}
	nd.n.noteAccess(a)
}

// ---------- тесты ----------

func TestСерверыСвязываютсяОднимКодом(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "Германия")
	b := tn.node(t, "b.example", "Англия")

	pcA := a.client(t, "ПК")
	a.setAccess(t, AccessSettings{Enabled: true, Groups: []string{"дом"}, Rules: []AccessRule{{From: "дом", To: "*"}}})
	phoneB := b.client(t, "Телефон")
	b.setAccess(t, AccessSettings{Enabled: false, Groups: []string{"работа"},
		Rules: []AccessRule{{From: "работа", To: "*", Ports: "22"}, {From: "*", To: "работа"}}})

	code := a.invite(t)
	if !strings.HasPrefix(code, codePrefix) {
		t.Fatalf("код: %s", code)
	}
	// Разовый ключ кода стоит у meshlink, пока код не использован.
	if len(a.sys.linkKeys) != 1 || !strings.Contains(a.sys.linkKeys[0], "invite") {
		t.Fatalf("ключи meshlink: %v", a.sys.linkKeys)
	}

	p, err := b.n.Preview(context.Background(), code)
	if err != nil {
		t.Fatal(err)
	}
	if p.Rules != 2 || p.NetRules != 1 || p.AddAccounts != 1 || p.GiveAccounts != 1 || !slices.Equal(p.Servers, []string{"Германия"}) {
		t.Fatalf("окно подтверждения: %+v", p)
	}

	b.join(t, code)
	settle(a, b)

	// Списки серверов одинаковые на обоих.
	if got := a.serverIDs(); !slices.Equal(got, []string{"Англия", "Германия"}) {
		t.Fatalf("серверы у А: %v", got)
	}
	if got := b.serverIDs(); !slices.Equal(got, []string{"Англия", "Германия"}) {
		t.Fatalf("серверы у Б: %v", got)
	}
	// Правила — сети (А), свои у Б заменились.
	if acc := b.sets.Get().Access; !acc.Enabled || len(acc.Rules) != 1 || acc.Rules[0].From != "дом" {
		t.Fatalf("правила у Б: %+v", acc)
	}
	// Учётные записи объединились: те же имена и ключи на обоих.
	for _, nd := range []*netNode{a, b} {
		for _, c := range []Client{pcA, phoneB} {
			got, ok := nd.hasUser(c.Username)
			if !ok || got.PublicKey != c.PublicKey {
				t.Fatalf("%s: нет учётной записи %s (%+v)", nd.host, c.Username, got)
			}
			if !strings.Contains(nd.prov.users[c.Username], c.PublicKey) {
				t.Fatalf("%s: ключ %s не в authorized_keys", nd.host, c.Username)
			}
		}
	}
	// Закрытый ключ не ушёл.
	if got, _ := b.hasUser(pcA.Username); got.PrivateKey != "" || got.Origin != "Германия" {
		t.Fatalf("на Б запись А: %+v", got)
	}
	// Ключи: у А — ключ Б (и больше никаких разовых), у Б — ключ А.
	for _, nd := range []*netNode{a, b} {
		nd.sys.mu.Lock()
		keys := slices.Clone(nd.sys.linkKeys)
		nd.sys.mu.Unlock()
		if len(keys) != 1 || strings.Contains(keys[0], "invite") || !strings.Contains(keys[0], `permitopen="127.0.0.1:47847"`) {
			t.Fatalf("%s: ключи meshlink: %v", nd.host, keys)
		}
	}
	// Код одноразовый.
	if _, err := b.n.Preview(context.Background(), code); err == nil {
		t.Fatal("использованный код принят снова")
	}
	c := tn.node(t, "c.example", "Франция")
	if err := c.n.Join(code); err != nil {
		t.Fatal(err)
	}
	if j := waitJobErr(c.mesh); j == "" {
		t.Fatal("использованный код сработал второй раз")
	}
	// Устройствам советуются другие серверы сети с их ключами.
	b.adm.mu.Lock()
	known, shared := slices.Clone(b.adm.known), slices.Clone(b.adm.shared)
	b.adm.mu.Unlock()
	// В списке и сам Б: по нему устройство узнаёт, в какой сети его сервер.
	if len(known) != 2 || known[0].Net == "" || known[0].HostKey == "" || known[0].ID == "" ||
		!slices.Contains(shared, pcA.Username) || !slices.Contains(shared, phoneB.Username) {
		t.Fatalf("советы устройствам у Б: %+v %v", known, shared)
	}
}

func waitJobErr(m *MeshManager) string {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if j := m.jobSnapshot(); j != nil && !j.Running {
			return j.Error
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "не закончилось"
}

// threeNodes — А, Б, В в одной сети: Б вошёл по коду А, В — по коду Б.
func threeNodes(t *testing.T) (*testNet, *netNode, *netNode, *netNode) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "А")
	b := tn.node(t, "b.example", "Б")
	c := tn.node(t, "c.example", "В")
	b.join(t, a.invite(t))
	settle(a, b)
	c.join(t, b.invite(t))
	settle(a, b, c)
	return tn, a, b, c
}

func TestТретийСерверПоявляетсяУВсех(t *testing.T) {
	_, a, b, c := threeNodes(t)
	for _, nd := range []*netNode{a, b, c} {
		if got := nd.serverIDs(); !slices.Equal(got, []string{"А", "Б", "В"}) {
			t.Fatalf("%s: серверы %v", nd.host, got)
		}
		for _, s := range nd.mesh.State(context.Background()).ServerNet.Servers {
			if !s.Self && !s.Online {
				t.Fatalf("%s: %s не на связи: %s", nd.host, s.Name, s.Error)
			}
		}
	}
	// А и В связались напрямую, хотя код В давал Б.
	a.sys.mu.Lock()
	keys := len(a.sys.linkKeys)
	a.sys.mu.Unlock()
	if keys != 2 {
		t.Fatalf("у А ключей meshlink %d, нужно 2", keys)
	}
}

func TestПравкиНаОдномЕстьНаВсех(t *testing.T) {
	_, a, b, c := threeNodes(t)
	ctx := context.Background()
	// Одно и то же устройство на связи со всеми тремя.
	for _, nd := range []*netNode{a, b, c} {
		nd.adm.addDevice("net1", "dev1", "tun_x", true)
		nd.adm.addDevice("net1", "dev2", "tun_x", false)
		nd.adm.addDevice("net2", "dev3", "tun_x", false)
	}
	settle(a, b, c)

	if err := a.mesh.SetApproval(ctx, "net1", true); err != nil {
		t.Fatal(err)
	}
	if err := b.mesh.Approve(ctx, "net1", "dev1"); err != nil {
		t.Fatal(err)
	}
	if err := c.mesh.Freeze(ctx, "net1", "dev2", true); err != nil {
		t.Fatal(err)
	}
	if err := a.mesh.RenameNetwork(ctx, "net1", "Дом"); err != nil {
		t.Fatal(err)
	}
	if err := b.mesh.Rename(ctx, "net1", "dev2", "Ноутбук"); err != nil {
		t.Fatal(err)
	}
	if err := c.mesh.DeleteNetwork(ctx, "net2"); err != nil {
		t.Fatal(err)
	}
	pc := a.client(t, "ПК")
	settle(a, b, c)
	if err := b.srv.clients.Freeze(pc.ID); err != nil {
		t.Fatal(err)
	}
	settle(a, b, c)

	for _, nd := range []*netNode{a, b, c} {
		n, _ := nd.adm.net("net1")
		d1, _ := nd.adm.dev("net1", "dev1")
		d2, _ := nd.adm.dev("net1", "dev2")
		if !n.approval || n.name != "Дом" || d1.Pending || !d2.Frozen || d2.Alias != "Ноутбук" {
			t.Fatalf("%s: сеть %+v, устройства %+v %+v", nd.host, n, d1, d2)
		}
		if _, ok := nd.adm.net("net2"); ok {
			t.Fatalf("%s: удалённая сеть осталась", nd.host)
		}
		got, ok := nd.hasUser(pc.Username)
		if !ok || got.State != StateFrozen || strings.Contains(nd.prov.users[pc.Username], pc.PublicKey) {
			t.Fatalf("%s: заморозка учётной записи не дошла: %+v", nd.host, got)
		}
	}

	// Убрать устройство — на всех; вернётся — снова ждёт одобрения.
	if err := c.mesh.Forget(ctx, "net1", "dev1"); err != nil {
		t.Fatal(err)
	}
	settle(a, b, c)
	for _, nd := range []*netNode{a, b, c} {
		if _, ok := nd.adm.dev("net1", "dev1"); ok {
			t.Fatalf("%s: убранное устройство осталось", nd.host)
		}
	}
	a.adm.addDevice("net1", "dev1", "tun_x", true)
	settle(a, b, c)
	if d, _ := a.adm.dev("net1", "dev1"); !d.Pending {
		t.Fatal("убранное устройство вернулось одобренным")
	}

	// Удаление учётной записи — на всех.
	if err := c.srv.clients.DeleteClient(pc.ID); err != nil {
		t.Fatal(err)
	}
	settle(a, b, c)
	for _, nd := range []*netNode{a, b, c} {
		if _, ok := nd.hasUser(pc.Username); ok {
			t.Fatalf("%s: удалённая учётная запись осталась", nd.host)
		}
	}
}

func TestСерверБезСвязиДогоняет(t *testing.T) {
	tn, a, b, c := threeNodes(t)
	ctx := context.Background()
	for _, nd := range []*netNode{a, b, c} {
		nd.adm.addDevice("net1", "dev1", "tun_x", false)
	}
	tn.setDown("c.example", true)
	if err := a.mesh.Freeze(ctx, "net1", "dev1", true); err != nil {
		t.Fatal(err)
	}
	a.setAccess(t, AccessSettings{Enabled: true})
	settle(a, b, c)
	if d, _ := c.adm.dev("net1", "dev1"); d.Frozen {
		t.Fatal("сервер без связи получил правку")
	}
	if d, _ := b.adm.dev("net1", "dev1"); !d.Frozen {
		t.Fatal("правка не дошла до Б, пока В без связи")
	}
	// Связь вернулась.
	tn.setDown("c.example", false)
	settle(a, b, c)
	if d, _ := c.adm.dev("net1", "dev1"); !d.Frozen || !c.sets.Get().Access.Enabled {
		t.Fatal("В не догнал")
	}
}

func TestУбранныйСерверБольшеНеПолучает(t *testing.T) {
	_, a, b, c := threeNodes(t)
	ctx := context.Background()
	stC := c.mesh.State(ctx).ServerNet
	if err := a.n.Remove(ctx, stC.SelfID); err != nil {
		t.Fatal(err)
	}
	settle(a, b, c)
	for _, nd := range []*netNode{a, b} {
		if got := nd.serverIDs(); !slices.Equal(got, []string{"А", "Б"}) {
			t.Fatalf("%s: серверы %v", nd.host, got)
		}
		nd.sys.mu.Lock()
		keys := strings.Join(nd.sys.linkKeys, "\n")
		nd.sys.mu.Unlock()
		if strings.Contains(keys, c.n.signerPub()) {
			t.Fatalf("%s: ключ убранного сервера остался", nd.host)
		}
	}
	st := c.mesh.State(ctx).ServerNet
	if st.Net != "" || st.Removed == "" {
		t.Fatalf("убранный сервер: %+v", st)
	}
	// Дальше правки до него не доходят.
	for _, nd := range []*netNode{a, b, c} {
		nd.adm.addDevice("net1", "dev1", "tun_x", false)
	}
	if err := a.mesh.Freeze(ctx, "net1", "dev1", true); err != nil {
		t.Fatal(err)
	}
	settle(a, b, c)
	if d, _ := c.adm.dev("net1", "dev1"); d.Frozen {
		t.Fatal("убранный сервер получил правку")
	}
	if d, _ := b.adm.dev("net1", "dev1"); !d.Frozen {
		t.Fatal("правка не дошла до Б")
	}
	// Убранный может войти снова по новому коду.
	c.join(t, a.invite(t))
	settle(a, b, c)
	if got := b.serverIDs(); !slices.Equal(got, []string{"А", "Б", "В"}) {
		t.Fatalf("после возврата: %v", got)
	}
}

func TestИстёкшийКодНеПринимается(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "А")
	b := tn.node(t, "b.example", "Б")
	code := a.invite(t)
	tn.advance(inviteTTL + time.Minute)
	a.n.reconcile(context.Background())
	if _, err := b.n.Preview(context.Background(), code); err == nil {
		t.Fatal("истёкший код принят")
	}
	// Разовый ключ ушёл из authorized_keys.
	a.sys.mu.Lock()
	keys := len(a.sys.linkKeys)
	a.sys.mu.Unlock()
	if keys != 0 {
		t.Fatalf("ключей meshlink у А: %d", keys)
	}
	// Старый код старого способа — понятная ошибка.
	if _, _, err := decodeNetCode(invitePrefix + "e30"); err == nil || !strings.Contains(err.Error(), "старого способа") {
		t.Fatalf("старый код: %v", err)
	}
}

func TestУчётныеЗаписиСОдинаковымИменемОбъединяются(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "А")
	b := tn.node(t, "b.example", "Б")
	pc := a.client(t, "ПК")
	// Тот же пользователь на Б со своим ключом (заведён руками раньше).
	pub2, _, _ := generateKeyPair()
	if err := b.srv.clients.ApplyShared(pc.Username, &acctRec{Name: "ПК", Keys: []string{pub2}}); err != nil {
		t.Fatal(err)
	}
	b.join(t, a.invite(t))
	settle(a, b)
	for _, nd := range []*netNode{a, b} {
		got, _ := nd.hasUser(pc.Username)
		if !slices.Contains(got.keys(), pc.PublicKey) || !slices.Contains(got.keys(), pub2) {
			t.Fatalf("%s: ключи %v", nd.host, got.keys())
		}
		if !strings.Contains(nd.prov.users[pc.Username], pc.PublicKey) || !strings.Contains(nd.prov.users[pc.Username], strings.Fields(pub2)[1]) {
			t.Fatalf("%s: authorized_keys %q", nd.host, nd.prov.users[pc.Username])
		}
	}
	// У А закрытый ключ своего — на месте, основной ключ — свой.
	if got, _ := a.hasUser(pc.Username); got.PrivateKey == "" || got.PublicKey != pc.PublicKey {
		t.Fatalf("на А: %+v", got)
	}
}

func TestПодписьОбменаПроверяется(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "А")
	b := tn.node(t, "b.example", "Б")
	b.join(t, a.invite(t))
	settle(a, b)
	// Чужая программа на петле А с выдуманной подписью — отказ.
	stB := b.mesh.State(context.Background()).ServerNet
	req, _ := http.NewRequest(http.MethodPost, "http://"+tn.addrs["a.example"]+"/srv/v1/exchange", strings.NewReader(`{"items":{}}`))
	req.Header.Set("X-Srv-Net", stB.Net)
	req.Header.Set("X-Srv-From", stB.SelfID)
	req.Header.Set("X-Srv-Time", "1")
	req.Header.Set("X-Srv-Sig", "AAAA")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("ответ %d", resp.StatusCode)
	}
	// Записи чужого ключа с неверным id не принимаются.
	a.n.mu.Lock()
	ch := a.n.mergeLocked(map[string]srvItem{"evil": {T: 1}, "acct:": {T: 1}})
	a.n.mu.Unlock()
	if len(ch) != 0 {
		t.Fatalf("приняты %v", ch)
	}
}

func TestРучкиСетиСерверовИСтарыеОтвечают(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "А")
	b := tn.node(t, "b.example", "Б")
	if err := a.srv.store.CreateWithPassword("admin", "onetimepass", true); err != nil {
		t.Fatal(err)
	}
	h := a.srv.Handler()
	cookies := loggedInCookies(t, h, "onetimepass")
	rec, out := doJSON(t, h, http.MethodPost, "/api/mesh/servers/invite", map[string]any{}, cookies)
	if rec.Code != http.StatusOK || out["code"] == "" {
		t.Fatalf("invite: %d %v", rec.Code, out)
	}
	hb := b.srv.Handler()
	if err := b.srv.store.CreateWithPassword("admin", "onetimepass", true); err != nil {
		t.Fatal(err)
	}
	cb := loggedInCookies(t, hb, "onetimepass")
	rec, out = doJSON(t, hb, http.MethodPost, "/api/mesh/servers/preview", map[string]any{"code": out["code"]}, cb)
	if rec.Code != http.StatusOK || out["servers"] == nil {
		t.Fatalf("preview: %d %v", rec.Code, out)
	}
	rec, out = doJSON(t, h, http.MethodGet, "/api/mesh/servers", nil, cookies)
	if rec.Code != http.StatusOK || out["net"] == "" {
		t.Fatalf("servers: %d %v", rec.Code, out)
	}
	// Старые ручки отвечают (для старых страниц).
	for _, p := range []string{"/api/mesh/known", "/api/mesh/links/delete", "/api/mesh/join", "/api/mesh/role"} {
		rec, _ := doJSON(t, h, http.MethodPost, p, map[string]any{}, cookies)
		if rec.Code == http.StatusNotFound {
			t.Fatalf("%s: 404", p)
		}
	}
}

// signerPub — открытый ключ связи сервера (для проверок).
func (n *ServerNet) signerPub() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	s, err := n.keyLocked()
	if err != nil {
		return ""
	}
	return strings.Fields(pubLine(s.PublicKey()))[1]
}

// Сервер убрали из сети, пока он был без связи: он не узнал об этом и
// остался с номером той сети один. Новый код той же сети должен пускать
// его обратно (так застрял London на v1.10.2: «этот сервер уже в этой
// сети»).
func TestУбранныйБезСвязиВходитЗаново(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "Германия")
	b := tn.node(t, "b.example", "London")
	b.join(t, a.invite(t))
	settle(a, b)
	ctx := context.Background()
	stB := b.mesh.State(ctx).ServerNet
	tn.setDown("b.example", true)
	if err := a.n.Remove(ctx, stB.SelfID); err != nil {
		t.Fatal(err)
	}
	settle(a)
	tn.setDown("b.example", false)
	// А больше не стучится к Б, Б к А пускать перестали: Б остаётся с
	// номером сети, но без связи. Чтобы это было так, как у Виталия, —
	// убираем у Б запись А (у него в списке остался только он сам).
	b.n.mu.Lock()
	for k := range b.n.f.Items {
		if strings.HasPrefix(k, kSrv) && k != kSrv+b.n.f.Self {
			delete(b.n.f.Items, k)
		}
	}
	netB := b.n.f.Net
	b.n.mu.Unlock()
	if netB == "" {
		t.Fatal("в этой проверке Б должен остаться с номером сети")
	}
	b.join(t, a.invite(t))
	settle(a, b)
	for _, nd := range []*netNode{a, b} {
		if got := nd.serverIDs(); !slices.Equal(got, []string{"London", "Германия"}) {
			t.Fatalf("%s: серверы %v", nd.host, got)
		}
	}
}

// Сервер, который в сети вместе с другими, по коду той же сети второй
// раз не входит.
func TestВСетиСДругимиВторойРазНеВходит(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "Германия")
	b := tn.node(t, "b.example", "London")
	b.join(t, a.invite(t))
	settle(a, b)
	if _, err := b.n.Preview(context.Background(), a.invite(t)); err == nil || !strings.Contains(err.Error(), "уже в этой сети") {
		t.Fatalf("ждали «уже в этой сети», получили %v", err)
	}
}
