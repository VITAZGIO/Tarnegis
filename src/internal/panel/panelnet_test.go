package panel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ТЗ 49: устройство по приглашению получает ключ сети панели в профиле —
// на любом сервере сети один и тот же; без сети устройств и замороженное —
// без ключа; в приглашении ключа нет.
func TestСетьПанелиВПрофиле(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "A")
	b := tn.node(t, "b.example", "B")
	b.join(t, a.invite(t))
	settle(a, b)
	if a.n.PanelNetID() != "" {
		t.Fatal("сеть панели завелась сама, без нужды")
	}

	res, err := a.srv.createInvite(inviteReq{Name: "Телефон", DeviceType: "android", Servers: []string{b.n.SelfID()}})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := a.n.PanelNet(false)
	if !rec.valid() {
		t.Fatal("первое приглашение с сетью не завело сеть панели")
	}
	if strings.Contains(res.Text, rec.Key) {
		t.Fatal("ключ сети в тексте приглашения")
	}
	r := joinAs(t, a.srv, tokOf(t, res.Text), newJoinKey(t), "198.51.100.7")
	if !r.OK {
		t.Fatalf("вход: %+v", r)
	}
	settle(a, b)
	ca, _ := a.store.Get(r.ID)
	pa := a.srv.profileOf(ca)
	if pa.MeshKey != rec.Key {
		t.Fatalf("в профиле нет ключа сети панели: %+v", pa)
	}
	cb, _ := b.store.Get(r.ID)
	if pb := b.srv.profileOf(cb); pb.MeshKey != rec.Key {
		t.Fatalf("на B другой ключ: %q", pb.MeshKey)
	}
	// Файл на B — тот же ключ, права 0600.
	path := filepath.Join(filepath.Dir(b.n.path), "panel-net.json")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), rec.Key) {
		t.Fatalf("panel-net.json на B: %v %s", err, data)
	}
	if st := a.mesh.State(context.Background()); st.PanelNet != meshNetID(rec.Key) || strings.Contains(st.PanelNet, rec.Key) {
		t.Fatalf("panelNet в состоянии: %q", st.PanelNet)
	}

	// Второе устройство по другому приглашению — в ту же сеть.
	res2, err := b.srv.createInvite(inviteReq{Name: "Ноутбук", DeviceType: "windows"})
	if err != nil {
		t.Fatal(err)
	}
	r2 := joinAs(t, b.srv, tokOf(t, res2.Text), newJoinKey(t), "198.51.100.8")
	c2, _ := b.store.Get(r2.ID)
	if p2 := b.srv.profileOf(c2); p2.MeshKey != rec.Key {
		t.Fatalf("второе устройство в другой сети: %q", p2.MeshKey)
	}

	// Без сети устройств — без ключа.
	off := false
	res3, err := a.srv.createInvite(inviteReq{Name: "Камера", DeviceType: "linux", Mesh: &off})
	if err != nil {
		t.Fatal(err)
	}
	r3 := joinAs(t, a.srv, tokOf(t, res3.Text), newJoinKey(t), "198.51.100.9")
	c3, _ := a.store.Get(r3.ID)
	if p3 := a.srv.profileOf(c3); p3.MeshKey != "" || p3.Mesh {
		t.Fatalf("ключ устройству без сети: %+v", p3)
	}
	// Замороженное — без ключа.
	if err := a.srv.clients.Freeze(r.ID); err != nil {
		t.Fatal(err)
	}
	ca, _ = a.store.Get(r.ID)
	if p := a.srv.profileOf(ca); p.MeshKey != "" || !p.Frozen {
		t.Fatalf("ключ замороженному: %+v", p)
	}
}

// Две панели завели сеть панели каждая у себя, потом связались: остаётся
// та, что заведена раньше, — на обоих серверах.
func TestСетьПанелиРаньшеЗаведённаяОстаётся(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "A")
	b := tn.node(t, "b.example", "B")
	ra, err := a.n.PanelNet(true)
	if err != nil {
		t.Fatal(err)
	}
	tn.advance(time.Second) // часы стенда общие и сами не идут
	rb, _ := b.n.PanelNet(true)
	if ra.Key == rb.Key {
		t.Fatal("одинаковые ключи")
	}
	b.join(t, a.invite(t))
	settle(a, b)
	settle(a, b)
	if got, _ := b.n.PanelNet(false); got.Key != ra.Key {
		t.Fatal("на B осталась своя сеть панели, а не заведённая раньше")
	}
	if got, _ := a.n.PanelNet(false); got.Key != ra.Key {
		t.Fatal("A сменил сеть панели")
	}
	// Более новая правка записи не перебивает заведённый раньше ключ.
	a.n.mu.Lock()
	a.n.stampLocked(kMeshKey, false, panelNetRec{Key: "новый-ключ-подделка-xx", Created: ra.Created + 1})
	snap := a.n.snapshotLocked()
	a.n.mu.Unlock()
	b.n.mu.Lock()
	b.n.mergeLocked(snap)
	got, _ := b.n.netPanelRecLocked()
	b.n.mu.Unlock()
	if got.Key != ra.Key {
		t.Fatalf("новая запись перебила ключ: %q", got.Key)
	}
}

// ТЗ 49, C.1: unix-пользователя на сервере нет — удаление устройства всё
// равно проходит: здесь и в общих данных.
func TestУдалениеУстройстваБезПользователя(t *testing.T) {
	m, prov := newTestClientManager(t)
	c, err := m.CreateClient("COMP", DeviceWindows)
	if err != nil {
		t.Fatal(err)
	}
	prov.mu.Lock()
	delete(prov.users, c.Username)
	prov.mu.Unlock()
	if err := m.DeleteClient(c.ID); err != nil {
		t.Fatalf("удаление без пользователя: %v", err)
	}
	if _, ok := m.store.Get(c.ID); ok {
		t.Fatal("запись осталась")
	}

	// В сети серверов: на A пользователя нет — устройство уходит и с B.
	tn := newTestNet()
	a := tn.node(t, "a.example", "A")
	b := tn.node(t, "b.example", "B")
	b.join(t, a.invite(t))
	settle(a, b)
	pc, err := a.srv.clients.CreateClient("COMP", DeviceWindows)
	if err != nil {
		t.Fatal(err)
	}
	settle(a, b)
	if _, ok := b.hasUser(pc.ID); !ok {
		t.Fatal("запись не дошла до B")
	}
	a.prov.mu.Lock()
	delete(a.prov.users, pc.Username)
	a.prov.mu.Unlock()
	if err := a.srv.deleteDevice(pc.ID); err != nil {
		t.Fatalf("deleteDevice: %v", err)
	}
	settle(a, b)
	if _, ok := a.hasUser(pc.ID); ok {
		t.Fatal("осталась на A")
	}
	if _, ok := b.hasUser(pc.ID); ok {
		t.Fatal("осталась на B")
	}
	if userMissing(nil) || !userMissing(ErrNoUser) {
		t.Fatal("userMissing")
	}
}
