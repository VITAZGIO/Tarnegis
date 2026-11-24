package panel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"sshtunnel/internal/meshsvc"
)

// updMemAdmin — memAdmin, который умеет обновление сети (meshd 8).
type updMemAdmin struct {
	*memAdmin
	mu      sync.Mutex
	netVer  string
	reqs    []meshsvc.NetUpdateReq
	answers []meshsvc.NetUpdateAnswer
	devUpd  []string
}

func (a *updMemAdmin) SetNetVersion(_ context.Context, v string) error {
	a.mu.Lock()
	a.netVer = v
	a.mu.Unlock()
	return nil
}

func (a *updMemAdmin) DevUpdate(_ context.Context, netID, dev string) error {
	a.mu.Lock()
	a.devUpd = append(a.devUpd, netID+"/"+dev)
	a.mu.Unlock()
	return nil
}

func (a *updMemAdmin) SetAutoUpdate(_ context.Context, netID, dev string, on bool) (int64, error) {
	v := "off"
	if on {
		v = "on"
	}
	return 1, a.withDev(netID, dev, func(d *meshsvc.Device) { d.AutoUpd = v })
}

func (a *updMemAdmin) NetUpdates(_ context.Context, ans []meshsvc.NetUpdateAnswer) ([]meshsvc.NetUpdateReq, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.answers = append(a.answers, ans...)
	out := a.reqs
	a.reqs = nil
	return out, nil
}

func (a *updMemAdmin) ask(q meshsvc.NetUpdateReq) {
	a.mu.Lock()
	a.reqs = append(a.reqs, q)
	a.mu.Unlock()
}

func (a *updMemAdmin) answer(call string) (meshsvc.NetUpdateAnswer, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, x := range a.answers {
		if x.Call == call {
			return x, true
		}
	}
	return meshsvc.NetUpdateAnswer{}, false
}

// updNode — одиночный сервер с подделанной версией панели и релизом.
func updNode(t *testing.T, ver, latest string) (*netNode, *updMemAdmin, *int) {
	t.Helper()
	tn := newTestNet()
	nd := tn.node(t, "a.example", "А")
	ua := &updMemAdmin{memAdmin: nd.adm}
	nd.mesh.admin = ua
	cur := ver
	var mu sync.Mutex
	calls := 0
	nd.n.updLoopOff = true
	nd.n.panelVer = func() string { mu.Lock(); defer mu.Unlock(); return cur }
	nd.n.startUpdate = func() error { mu.Lock(); calls++; mu.Unlock(); return nil }
	nd.n.nu.latest = func(context.Context) (string, error) { return latest, nil }
	for _, c := range []Client{{ID: "c1", Username: "tun_pc", Groups: []string{groupMine}},
		{ID: "c2", Username: "tun_guest", Groups: []string{"~guest"}}} {
		if err := nd.store.Put(c); err != nil {
			t.Fatal(err)
		}
	}
	return nd, ua, &calls
}

func TestВерсияСетиУходитВMeshd(t *testing.T) {
	nd, ua, _ := updNode(t, "1.10.6", "1.10.7")
	if err := nd.n.netUpdTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ua.netVer != "1.10.6" {
		t.Fatalf("версия сети: %q", ua.netVer)
	}
}

func TestОбновитьСетьМоёУстройствоДаГостьНет(t *testing.T) {
	nd, ua, calls := updNode(t, "1.10.6", "1.10.7")
	ctx := context.Background()
	ua.ask(meshsvc.NetUpdateReq{Call: "g", User: "tun_guest", Version: "1.10.7"})
	ua.ask(meshsvc.NetUpdateReq{Call: "old", User: "tun_pc", Version: "1.10.6"})
	ua.ask(meshsvc.NetUpdateReq{Call: "fake", User: "tun_pc", Version: "1.10.9"})
	nd.n.netUpdTick(ctx)
	nd.n.netUpdTick(ctx) // ответы уходят следующим кругом
	for call, want := range map[string]string{"g": "Моё устройство", "old": "уже на версии", "fake": "не последний"} {
		a, ok := ua.answer(call)
		if !ok || !strings.Contains(a.Error, want) {
			t.Errorf("%s: %+v (ждали «%s»)", call, a, want)
		}
	}
	if *calls != 0 || nd.n.NetTarget() != "1.10.6" {
		t.Fatalf("отказ всё-таки обновил: %d %s", *calls, nd.n.NetTarget())
	}
	ua.ask(meshsvc.NetUpdateReq{Call: "ok", User: "tun_pc", Version: "v1.10.7"})
	nd.n.netUpdTick(ctx)
	nd.n.netUpdTick(ctx)
	if a, ok := ua.answer("ok"); !ok || a.Error != "" {
		t.Fatalf("моё устройство: %+v %v", a, ok)
	}
	if *calls != 1 || nd.n.NetTarget() != "1.10.7" || ua.netVer != "1.10.7" {
		t.Fatalf("обновление сети: calls=%d target=%s meshd=%s", *calls, nd.n.NetTarget(), ua.netVer)
	}
}

func TestОбновитьСетьВСетиСерверов(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "А")
	b := tn.node(t, "b.example", "Б")
	b.join(t, a.invite(t))
	settle(a, b)
	ua := &updMemAdmin{memAdmin: a.adm}
	a.mesh.admin = ua
	a.n.updLoopOff = true
	a.n.panelVer = func() string { return "1.10.6" }
	a.n.startUpdate = func() error { return nil }
	a.n.nu.latest = func(context.Context) (string, error) { return "1.10.7", nil }
	a.n.srv = nil // учётных записей не смотрим: роль проверил meshd
	if err := a.n.netUpdateRequest(context.Background(), meshsvc.NetUpdateReq{User: "x", Version: "1.10.7"}); err != nil {
		t.Fatal(err)
	}
	a.n.mu.Lock()
	target, _, _ := a.n.updTargetLocked()
	a.n.mu.Unlock()
	if target != "1.10.7" {
		t.Fatalf("запись upd: %q", target)
	}
}

func TestСписокУстройствИКнопки(t *testing.T) {
	nd, ua, _ := updNode(t, "1.10.7", "1.10.7")
	ua.addDevice("n1", "pc", "tun_pc", false)
	ua.addDevice("n1", "old", "tun_pc", false)
	ua.addDevice("n1", "off", "tun_pc", false)
	ua.addDevice("n1", "away", "tun_pc", false)
	set := func(id, app, au string, online bool) {
		ua.withDev("n1", id, func(d *meshsvc.Device) { d.App, d.AutoUpd, d.Online = app, au, online })
	}
	set("pc", "v1.10.7", "on", true)
	set("old", "1.10.5", "on", true)
	set("off", "1.10.5", "off", true)
	set("away", "1.10.5", "on", false)
	got := map[string]NetDev{}
	for _, d := range nd.n.NetDevices(context.Background()).Devices {
		got[d.ID] = d
	}
	for id, want := range map[string]string{"pc": devUpdOK, "old": devUpdBehind, "off": devUpdOff, "away": devUpdOffline} {
		if got[id].State != want {
			t.Errorf("%s: %q, ждали %q", id, got[id].State, want)
		}
	}
	if !got["old"].CanUpdate || got["pc"].CanUpdate || got["away"].CanUpdate {
		t.Errorf("кнопки: %+v", got)
	}
	sent, err := nd.n.UpdateDevices(context.Background())
	if err != nil || sent != 2 {
		t.Fatalf("обновить все: %d %v %v", sent, err, ua.devUpd)
	}
	if err := nd.n.SetDeviceAutoUpdate(context.Background(), "n1", "off", true); err != nil {
		t.Fatal(err)
	}
}

func TestСтарыйMeshdБезОбновленияСети(t *testing.T) {
	tn := newTestNet()
	nd := tn.node(t, "a.example", "А")
	if err := nd.n.netUpdTick(context.Background()); !errors.Is(err, meshsvc.ErrUnsupported) {
		t.Fatalf("старый meshd: %v", err)
	}
	if !nd.n.NetDevices(context.Background()).Old {
		t.Error("не видно, что meshd старый")
	}
}
