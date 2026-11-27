package tunnel

// ТЗ 57: звонки Discord висели на «подключении». Голос — UDP на высокие
// порты; сервер звонка может отвечать с другого порта и подолгу слать
// поток в одну сторону. Здесь — путь «адаптер → ретранслятор» без сети.

import (
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"sshtunnel/internal/core"
	"sshtunnel/internal/events"
)

// fakeRelay — ретранслятор в памяти: на каждую датаграмму отвечает с
// соседнего порта цели, а push шлёт поток «только к нам».
type fakeRelay struct {
	mu      sync.Mutex
	deliver map[uint32]func([]byte, string, uint16)
	next    uint32
	closed  map[uint32]bool
}

func (r *fakeRelay) Open(d func([]byte, string, uint16)) uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	if r.deliver == nil {
		r.deliver, r.closed = map[uint32]func([]byte, string, uint16){}, map[uint32]bool{}
	}
	r.deliver[r.next] = d
	return r.next
}

func (r *fakeRelay) Close(id uint32) {
	r.mu.Lock()
	r.closed[id] = true
	r.mu.Unlock()
}

func (r *fakeRelay) Send(id uint32, host string, port uint16, data []byte) error {
	r.mu.Lock()
	d := r.deliver[id]
	r.mu.Unlock()
	d(append([]byte("re:"), data...), host, port+1) // ответ с другого порта
	return nil
}

func (r *fakeRelay) push(id uint32, data []byte) {
	r.mu.Lock()
	d := r.deliver[id]
	r.mu.Unlock()
	d(data, "203.0.113.7", 50001)
}

func (r *fakeRelay) isClosed(id uint32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed[id]
}

// udpPair — два связанных UDP-сокета: app (как точка стека) и «программа».
func udpPair(t *testing.T) (app, prog *net.UDPConn) {
	t.Helper()
	a, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	b.Close()
	app, err = net.DialUDP("udp", a.LocalAddr().(*net.UDPAddr), b.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	prog, err = net.DialUDP("udp", b.LocalAddr().(*net.UDPAddr), a.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close(); prog.Close() })
	return app, prog
}

func TestUDPЧерезРетрансляторОтветСДругогоПортаИПотокВОднуСторону(t *testing.T) {
	old := udpIdle
	udpIdle = 300 * time.Millisecond
	t.Cleanup(func() { udpIdle = old })

	tun := &Tunnel{}
	relay := &fakeRelay{}
	app, prog := udpPair(t)
	done := make(chan struct{})
	go func() {
		tun.serveUDPRelay(app, core.Conn{Target: "203.0.113.7:50000"}, relay, "test")
		close(done)
	}()

	// Много датаграмм — ответ на каждую, хотя пришёл он «с другого порта».
	buf := make([]byte, 2048)
	for i := 0; i < 200; i++ {
		if _, err := prog.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		prog.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := prog.Read(buf)
		if err != nil || string(buf[:n]) != "re:ping" {
			t.Fatalf("ответ %d: %q %v", i, buf[:n], err)
		}
	}

	// Дальше программа молчит, а сервер шлёт поток втрое дольше срока
	// бездействия: поток должен жить (раньше закрывался по udpIdle).
	end := time.Now().Add(3 * udpIdle)
	for time.Now().Before(end) {
		relay.push(1, []byte("voice"))
		prog.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := prog.Read(buf); err != nil {
			t.Fatalf("поток в одну сторону оборвался: %v", err)
		}
		time.Sleep(udpIdle / 6)
	}
	if relay.isClosed(1) {
		t.Fatal("сессия закрыта, хотя ответы шли")
	}

	// Тишина в обе стороны — поток закрывается сам.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("тихий поток не закрылся")
	}
	if !relay.isClosed(1) {
		t.Fatal("сессия ретранслятора не закрыта")
	}
}

func TestUDPОтброшенПишетПричину(t *testing.T) {
	tun, _, _, _ := startTunnel(t, 1)
	if !tun.WaitReady(1, 5*time.Second) {
		t.Fatal("пул не поднялся")
	}
	ch, stop := tun.bus.Subscribe()
	defer stop()
	src := netip.MustParseAddrPort("10.0.0.2:40000")
	for i := 0; i < 3; i++ { // повтор не плодит записи
		if serve := tun.RouteUDP(src, "203.0.113.7:50000", true, nil); serve != nil {
			t.Fatal("ретранслятор выключен, а поток принят")
		}
	}
	got := 0
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == events.KindLog && strings.Contains(ev.Text, "203.0.113.7:50000") {
				if !strings.Contains(ev.Text, "выключен") {
					t.Fatalf("причина не та: %q", ev.Text)
				}
				got++
			}
			continue
		case <-timeout:
		}
		break
	}
	if got != 1 {
		t.Fatalf("записей об отказе %d, нужна одна", got)
	}
}
