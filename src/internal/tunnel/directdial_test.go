package tunnel

import (
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"sshtunnel/internal/core"
	"sshtunnel/internal/events"
	"sshtunnel/internal/routing"
)

// fakePins — PinDirect для тестов: считает маршруты и помнит, стоят ли они.
type fakePins struct {
	mu    sync.Mutex
	calls int
	live  map[netip.Addr]int
}

func (p *fakePins) pin(a netip.Addr) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.live == nil {
		p.live = map[netip.Addr]int{}
	}
	p.live[a]++
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.live[a]--
			p.mu.Unlock()
		})
	}
}

func (p *fakePins) pinned(a netip.Addr) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live[a] > 0
}

func (p *fakePins) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// listenEcho — локальный сервер, принимающий соединения.
func listenEcho(t *testing.T) (*net.TCPListener, netip.AddrPort) {
	t.Helper()
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { c.Write([]byte("ok")); c.Close() }()
		}
	}()
	return l, l.Addr().(*net.TCPAddr).AddrPort()
}

// timeoutErr — «ответа нет» (как SYN без ответа).
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// ТЗ 52: прямое соединение — привязанный сокет, без маршрута к адресу. Так
// делают sing-box, Xray и mihomo; маршрут на каждое соединение открывал
// адрес мимо VPN всем программам.
func TestПрямоеБезМаршрутаНаСоединение(t *testing.T) {
	_, addr := listenEcho(t)
	pins := &fakePins{}
	var protected atomic.Int32
	tun := New(Config{
		PinDirect: pins.pin,
		ProtectSocket: func(network, address string, c syscall.RawConn) error {
			protected.Add(1)
			return nil
		},
	}, events.NewBus())
	for i := 0; i < 20; i++ {
		c, st, err := tun.dialDirectTimed("tcp", addr.String(), 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if st.pinned {
			t.Fatal("соединению поставлен маршрут")
		}
		c.Close()
	}
	if n := pins.count(); n != 0 {
		t.Fatalf("маршрутов к адресу: %d, ждали 0", n)
	}
	if protected.Load() != 20 {
		t.Fatalf("сокет привязан %d раз из 20", protected.Load())
	}
}

// Запасной ход: привязанный сокет не дождался ответа (Windows с точкой
// доступа) — ставится маршрут, соединение повторяется, и до смены сети
// маршруты ставятся сразу. Kick (смена сети) — снова без маршрутов.
func TestПрямоеЗапасноеСМаршрутом(t *testing.T) {
	_, addr := listenEcho(t)
	pins := &fakePins{}
	bus := events.NewBus()
	tun := New(Config{
		PinDirect: pins.pin,
		ProtectSocket: func(network, address string, c syscall.RawConn) error {
			if !pins.pinned(addr.Addr()) {
				return timeoutErr{} // без маршрута ответы «выкидываются»
			}
			return nil
		},
	}, bus)

	c, st, err := tun.dialDirectTimed("tcp", addr.String(), 5*time.Second)
	if err != nil {
		t.Fatalf("запасной ход не сработал: %v", err)
	}
	c.Close()
	if !st.pinned || pins.count() != 1 || !tun.pinsNeeded.Load() {
		t.Fatalf("pinned=%v маршрутов=%d нужны=%v", st.pinned, pins.count(), tun.pinsNeeded.Load())
	}
	// Предупреждение в журнале — одно.
	warned := 0
	for _, e := range bus.History() {
		if e.Kind == events.KindLog && e.Level == "warn" {
			warned++
		}
	}
	if warned != 1 {
		t.Fatalf("предупреждений: %d", warned)
	}

	// Дальше на этой сети — сразу с маршрутом (без лишнего ожидания).
	t0 := time.Now()
	c, st, err = tun.dialDirectTimed("tcp", addr.String(), 5*time.Second)
	if err != nil || !st.pinned {
		t.Fatalf("второе соединение: %v pinned=%v", err, st.pinned)
	}
	c.Close()
	if time.Since(t0) > time.Second {
		t.Fatalf("второе соединение ждало %v", time.Since(t0))
	}
	if pins.pinned(addr.Addr()) {
		t.Fatal("маршрут не снят после закрытия")
	}

	tun.Kick()
	if tun.pinsNeeded.Load() {
		t.Fatal("после смены сети маршруты всё ещё нужны")
	}
}

// Отказ (не тайм-аут) — не повод ставить маршрут: адрес ответил.
func TestПрямоеОтказБезМаршрута(t *testing.T) {
	l, addr := listenEcho(t)
	l.Close()
	pins := &fakePins{}
	tun := New(Config{PinDirect: pins.pin}, events.NewBus())
	if _, _, err := tun.dialDirectTimed("tcp", addr.String(), 2*time.Second); err == nil {
		t.Fatal("соединение с закрытым портом прошло")
	}
	if pins.count() != 0 {
		t.Fatalf("на отказ поставлен маршрут: %d", pins.count())
	}
}

// «Страница ВКонтакте» (ТЗ 52): 60 соединений к 12 адресам по 6 сразу — ни
// одного маршрута, и в журнале у каждого соединения причина и замер.
func TestСтраницаНапрямуюБезМаршрутов(t *testing.T) {
	_, addr := listenEcho(t)
	pins := &fakePins{}
	bus := events.NewBus()
	tun := New(Config{PinDirect: pins.pin, Direct: routing.NewDirectList([]string{"@vk"})}, bus)

	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			route, _, why := tun.decideWhy("", "sun9-1.userapi.com:443", "", false)
			if route != core.RouteDirect || why != core.WhyList {
				t.Errorf("userapi.com: %v %q", route, why)
				return
			}
			c, st, err := tun.dialDirectTimed("tcp", addr.String(), 5*time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			bus.Publish(timed(eventConn("", 0, "sun9-1.userapi.com:443", "tun", true, true, nil), why, st))
			c.Close()
		}()
	}
	wg.Wait()
	if pins.count() != 0 {
		t.Fatalf("маршрутов: %d", pins.count())
	}
	got := 0
	for _, e := range bus.History() {
		if e.Kind == events.KindConn {
			got++
			if e.Why != "list" || !e.Direct || e.Pinned {
				t.Fatalf("строка журнала без причины: %+v", e)
			}
		}
	}
	if got == 0 {
		t.Fatal("нет строк журнала")
	}
}
