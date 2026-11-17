package panel

// Пересылка между серверами со стороны панели (ТЗ 24, п. 14): вызов от meshd
// Германии уходит подписанным /srv/v1/relay в панель London и дальше в её
// meshd. Сами meshd — подделки (настоящая пересылка meshd проверяется в
// cmd/meshd/relay_test.go).

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"sshtunnel/internal/meshsvc"
)

type fakeRelay struct {
	*memAdmin
	mu     sync.Mutex
	remote map[string][]meshsvc.RemotePeer
	outs   chan net.Conn // «звонящее устройство» после ok
	fails  chan string   // отказ, отданный meshd
	in     func(q meshsvc.RelayAsk) (net.Conn, error)
	got    []meshsvc.RelayAsk
}

func newFakeRelay(a *memAdmin) *fakeRelay {
	return &fakeRelay{memAdmin: a, outs: make(chan net.Conn, 4), fails: make(chan string, 4)}
}

func (f *fakeRelay) SetRemote(_ context.Context, nets map[string][]meshsvc.RemotePeer) error {
	f.mu.Lock()
	f.remote = nets
	f.mu.Unlock()
	return nil
}
func (f *fakeRelay) RelayWait(ctx context.Context, _ func(meshsvc.RelayAsk)) error {
	<-ctx.Done()
	return ctx.Err()
}
func (f *fakeRelay) RelayOut(_ context.Context, _ string, ok bool, text string) (net.Conn, error) {
	if !ok {
		f.fails <- text
		return nil, nil
	}
	a, b := net.Pipe()
	f.outs <- a
	return b, nil
}
func (f *fakeRelay) RelayIn(_ context.Context, q meshsvc.RelayAsk) (net.Conn, error) {
	f.mu.Lock()
	f.got = append(f.got, q)
	f.mu.Unlock()
	return f.in(q)
}

// echoPipe — «устройство London»: отвечает эхом с приставкой.
func echoPipe(prefix string) net.Conn {
	a, b := net.Pipe()
	go func() {
		l, _ := bufio.NewReader(b).ReadString('\n')
		b.Write([]byte(prefix + l))
		b.Close()
	}()
	return a
}

func TestПересылкаЧерезПанели(t *testing.T) {
	_, de, lon := twoServers(t)
	fde, flon := newFakeRelay(de.adm), newFakeRelay(lon.adm)
	de.mesh.admin, lon.mesh.admin = fde, flon
	flon.in = func(q meshsvc.RelayAsk) (net.Conn, error) { return echoPipe("phone:"), nil }
	lonID := lon.n.SelfID()

	ask := meshsvc.RelayAsk{Call: "c1", Net: "N", To: "198.19.0.9", Port: 7, Srv: lonID,
		From: meshsvc.RelayFrom{IP: "198.19.0.1", Host: "noutbuk", User: "tun_x"}}
	go de.n.relayOut(context.Background(), fde, ask)
	var dev net.Conn
	select {
	case dev = <-fde.outs:
	case text := <-fde.fails:
		t.Fatalf("отказ: %s", text)
	case <-time.After(5 * time.Second):
		t.Fatal("вызов не дошёл")
	}
	dev.Write([]byte("привет\n"))
	dev.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, _ := bufio.NewReader(dev).ReadString('\n')
	if got != "phone:привет\n" {
		t.Fatalf("ответ: %q", got)
	}
	// London узнал, от какого сервера вызов; id вызова Германии не ушёл.
	flon.mu.Lock()
	q := flon.got[0]
	flon.mu.Unlock()
	if q.Srv != de.n.SelfID() || q.Call != "" || q.From.Host != "noutbuk" {
		t.Fatalf("на London пришло %+v", q)
	}

	// Отказ meshd London доходит до устройства словами.
	flon.in = func(meshsvc.RelayAsk) (net.Conn, error) {
		return nil, errors.New("нет доступа: правила")
	}
	go de.n.relayOut(context.Background(), fde, ask)
	select {
	case text := <-fde.fails:
		if !strings.Contains(text, "нет доступа") {
			t.Fatalf("отказ: %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("отказа нет")
	}
	// Сервер не на связи — понятный отказ, а не тишина.
	go de.n.relayOut(context.Background(), fde, meshsvc.RelayAsk{Call: "c2", Net: "N", To: "x", Port: 7, Srv: "нет-такого"})
	if text := <-fde.fails; !strings.Contains(text, "нет такого сервера") {
		t.Fatalf("неизвестный сервер: %q", text)
	}
}

func TestMeshdУзнаётУстройстваДругихСерверов(t *testing.T) {
	_, de, lon := twoServers(t)
	fde := newFakeRelay(de.adm)
	de.mesh.admin = fde
	settle(de, lon)
	fde.mu.Lock()
	list := fde.remote["N"]
	fde.mu.Unlock()
	found := false
	for _, p := range list {
		if p.Host == "telefon" && p.Srv == lon.n.SelfID() && p.SrvName == "London" && p.Online {
			found = true
		}
		if p.Host == "noutbuk" {
			t.Fatal("своё устройство отдано как чужое")
		}
	}
	if !found {
		t.Fatalf("телефона London нет: %+v", list)
	}
}

func TestПересылкаТолькоПоПодписи(t *testing.T) {
	tn, _, _ := twoServers(t)
	if c := postPath(t, tn.addrs["de.example"], "/srv/v1/relay"); c != 403 {
		t.Fatalf("без подписи: %d", c)
	}
}
