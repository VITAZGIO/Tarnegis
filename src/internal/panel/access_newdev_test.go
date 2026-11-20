package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// ТЗ 28: «Новое устройство» — общий выбор сети серверов, новее побеждает.
func TestНовоеУстройствоОбщееДляСерверов(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "А")
	b := tn.node(t, "b.example", "Б")
	b.join(t, a.invite(t))
	settle(a, b)

	set := func(nd *netNode, mode string) {
		t.Helper()
		rec := httptest.NewRecorder()
		nd.srv.handleNewDevice(rec, httptest.NewRequest(http.MethodPost, "/api/access/newdevice", strings.NewReader(`{"mode":"`+mode+`"}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", mode, rec.Code, rec.Body)
		}
	}
	set(a, NewDeviceNone)
	settle(a, b)
	if got := b.sets.Get().NewDevice; got != NewDeviceNone {
		t.Fatalf("на Б «новое устройство» = %q, ждали none", got)
	}
	tn.advance(2e9)
	set(b, NewDeviceAll)
	settle(a, b)
	if got := a.sets.Get().NewDevice; got != NewDeviceAll {
		t.Fatalf("на А «новое устройство» = %q, ждали all (правка Б новее)", got)
	}

	rec := httptest.NewRecorder()
	a.srv.handleNewDevice(rec, httptest.NewRequest(http.MethodPost, "/api/access/newdevice", strings.NewReader(`{"mode":"кто-то"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("непонятный выбор принят: %d", rec.Code)
	}
}

// Новое устройство при «видно всем» получает галочки (к нему все, оно ко
// всем) на всех серверах; при «без доступа» — ничего.
func TestНовоеУстройствоПолучаетГалочки(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "А")
	b := tn.node(t, "b.example", "Б")
	b.join(t, a.invite(t))
	settle(a, b)

	old := a.client(t, "Старый")
	other := a.client(t, "Второй")
	// Как после простого режима: «Старый» ходит только ко «Второму».
	if err := a.srv.clients.Update(old.ID, func(c *Client) { c.Groups = []string{"~mine", "~" + old.Username} }); err != nil {
		t.Fatal(err)
	}
	if err := a.srv.clients.Update(other.ID, func(c *Client) { c.Groups = []string{"~mine", "~" + other.Username} }); err != nil {
		t.Fatal(err)
	}
	a.setAccess(t, AccessSettings{Enabled: true,
		Groups: []string{"~guest", "~mine", "~server", "~" + old.Username, "~" + other.Username},
		Rules:  []AccessRule{{From: "~" + old.Username, To: "~" + other.Username}}})
	if err := a.sets.Update(func(s *Settings) error { s.NewDevice = NewDeviceAll; return nil }); err != nil {
		t.Fatal(err)
	}

	nw := a.client(t, "Новый")
	a.srv.grantNewDevice(context.Background(), nw)
	settle(a, b)

	for _, nd := range []*netNode{a, b} {
		got, ok := nd.hasUser(nw.Username)
		if !ok || !slices.Contains(got.Groups, groupNewAll) || !slices.Contains(got.Groups, "~mine") {
			t.Fatalf("%s: группы нового %+v", nd.host, got.Groups)
		}
		var clients []Client
		for _, u := range []string{old.Username, other.Username, nw.Username} {
			c, _ := nd.hasUser(u)
			clients = append(clients, c)
		}
		p := demoPolicy(buildPolicy(nd.sets.Get().Access, clients))
		for _, u := range []string{old.Username, other.Username} {
			if !p.allowed(nw.Username, u, 0) || !p.allowed(u, nw.Username, 0) {
				t.Fatalf("%s: новый и %s не видят друг друга", nd.host, u)
			}
		}
		// Старые галочки не поменялись: «Второй» к «Старому» как не мог, так и нет.
		if p.allowed(other.Username, old.Username, 0) || !p.allowed(old.Username, other.Username, 0) {
			t.Fatalf("%s: поменялись старые галочки", nd.host)
		}
	}

	// «Без доступа»: новое устройство без групп, права те же.
	if err := a.sets.Update(func(s *Settings) error { s.NewDevice = NewDeviceNone; return nil }); err != nil {
		t.Fatal(err)
	}
	before := a.sets.Get().Access
	quiet := a.client(t, "Тихий")
	a.srv.grantNewDevice(context.Background(), quiet)
	got, _ := a.hasUser(quiet.Username)
	if len(got.Groups) != 0 || !accessEqual(before, a.sets.Get().Access) {
		t.Fatalf("«без доступа»: группы %v, права менялись: %v", got.Groups, !accessEqual(before, a.sets.Get().Access))
	}
}

// Без выбора (сервер до ТЗ 28) и при выключенных правилах ничего не
// добавляется: существующие правила работают как раньше.
func TestНовоеУстройствоНеМеняетСтарыеПравила(t *testing.T) {
	base := AccessSettings{Enabled: true, Groups: []string{"семья"}, Rules: []AccessRule{{From: "семья", To: "*", Ports: "22"}}}
	for _, mode := range []string{"", NewDeviceNone} {
		a := base
		a.Groups = slices.Clone(base.Groups)
		a.Rules = slices.Clone(base.Rules)
		if g := newDeviceGrant(&a, mode, nil); len(g) != 0 || !accessEqual(a, base) {
			t.Fatalf("режим %q: группы %v, права %+v", mode, g, a)
		}
	}
	off := AccessSettings{Groups: []string{"~mine"}}
	if g := newDeviceGrant(&off, NewDeviceAll, nil); len(g) != 0 || len(off.Rules) != 0 || len(off.Groups) != 1 {
		t.Fatalf("правила выключены — всё равно добавлено: %v %+v", g, off)
	}
	// Повторная выдача не плодит правила.
	on := AccessSettings{Enabled: true}
	newDeviceGrant(&on, NewDeviceAll, nil)
	newDeviceGrant(&on, NewDeviceAll, nil)
	if len(on.Rules) != 2 || !slices.Equal(on.Groups, []string{groupNewAll}) {
		t.Fatalf("после двух выдач: %+v", on)
	}
	if err := on.clean(); err != nil {
		t.Fatalf("права с ~all не проходят проверку: %v", err)
	}
}
