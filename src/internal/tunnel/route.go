package tunnel

// Здесь туннель спрашивает ядро, куда вести соединение (core.Decide), и
// исполняет ответ. Само решение в туннеле не принимается: правило пишется
// один раз в core и одинаково для прокси, для стека режима VPN и для
// Android.
//
// Кроме того, туннель помнит все живые соединения вместе с принятым
// решением — чтобы при смене режима закрыть открытые по старым правилам
// (core.Hygiene): состояние после переключения должно совпадать с чистым
// запуском в новом режиме.

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"sshtunnel/internal/core"
	"sshtunnel/internal/procinfo"
	"sshtunnel/internal/traffic"
)

// rules — правила решения прямо сейчас: режим кнопок и настройки.
func (t *Tunnel) rules() core.Rules {
	t.mu.RLock()
	policy, direct, tunneled := t.cfg.Policy, t.cfg.Direct, t.cfg.Tunneled
	t.mu.RUnlock()
	return core.Rules{
		Bypass:         !t.bypassOff.Load(),
		Mesh:           !t.meshPaused.Load() && (t.cfg.Mesh != nil || !t.ownMesh),
		Draining:       t.draining.Load(),
		LocalViaTunnel: t.localViaTunnel.Load(),
		Policy:         policy,
		Direct:         direct,
		Tunneled:       tunneled,
		FakeIP:         t.fakeIP.Load(),
		RealIP:         t.realIP.Load(),
	}
}

// Rules — те же правила для DNS стека (core.DecideDNS): чем отвечать на имя.
func (t *Tunnel) Rules() core.Rules { return t.rules() }

// decide спрашивает ядро, куда вести соединение программы process к target.
// Адрес, выданный нашим DNS «настоящим» под имя из «всегда напрямую»,
// сверяется со списками по этому имени (см. LearnDirect).
func (t *Tunnel) decide(process, target string) (core.Route, core.Conn) {
	route, c, _ := t.decideWhy(process, target, "", false)
	return route, c
}

// forced — источник из стека обязан идти только через сервер
// (Config.ForcedSource).
func (t *Tunnel) forced(src netip.Addr) bool {
	f := t.cfg.ForcedSource
	return f != nil && src.IsValid() && f(src.Unmap())
}

// decideNamed — то же, но имя соединения уже известно (из его первых байтов,
// см. sniff.go): оно точнее таблицы, где на один адрес CDN бывает много имён.
func (t *Tunnel) decideNamed(process, target, name string, forced bool) (core.Route, core.Conn) {
	route, c, _ := t.decideWhy(process, target, name, forced)
	return route, c
}

// decideWhy — то же вместе с причиной решения (для строки журнала).
func (t *Tunnel) decideWhy(process, target, name string, forced bool) (core.Route, core.Conn, core.Why) {
	c := core.Conn{Process: process, Target: target, Name: name, Forced: forced}
	if c.Name == "" {
		if learned, ok := t.learnedName(target); ok {
			c.Name = learned
		}
	}
	route, why := core.DecideWhy(t.rules(), c)
	return route, c, why
}

// dialRoute открывает TCP-соединение тем путём, который выбрало ядро.
func (t *Tunnel) dialRoute(route core.Route, target string) (net.Conn, error) {
	c, _, err := t.dialRouteTimed(route, target)
	return c, err
}

// dialRouteTimed — то же с замером: сколько ушло на DNS и на само
// соединение (строка журнала, ТЗ 52: человек видит, что тормозит).
func (t *Tunnel) dialRouteTimed(route core.Route, target string) (net.Conn, dialStats, error) {
	switch route {
	case core.RouteServer:
		t0 := time.Now()
		c, err := t.Dial("tcp", target)
		return c, dialStats{connect: time.Since(t0)}, err
	case core.RouteDirect:
		return t.dialDirectTimed("tcp", target, 15*time.Second)
	case core.RouteMesh:
		m := t.Mesh()
		if m == nil {
			return nil, dialStats{}, errMeshNotReady
		}
		t0 := time.Now()
		c, err := m.DialPeer(target)
		if err != nil {
			// В файл журнала: события соединений видны только в окне, а по
			// отказу сети устройств потом ищут причину (ТЗ 55).
			t.bus.Warnf("Сеть устройств: соединение с %s не удалось: %v", target, err)
		}
		return c, dialStats{connect: time.Since(t0)}, err
	}
	return nil, dialStats{}, errBlocked
}

// DialApp — соединение от имени самой программы (плагины:
// SSH, «Файлы устройства»): путь выбирает ядро, как для любой программы.
func (t *Tunnel) DialApp(target string) (net.Conn, error) {
	route, _ := t.decide("", target)
	c, err := t.dialRoute(route, target)
	if err != nil {
		return nil, err
	}
	return metered(c, selfName, route), nil
}

var (
	errMeshNotReady = errors.New("сеть устройств ещё не подключилась")
	errBlocked      = errors.New("сеть устройств выключена")
)

// entry — откуда пришло соединение приложения.
type entry int

const (
	entryProxy entry = iota // локальный SOCKS/HTTP-прокси
	entryStack              // адаптер режима VPN или Android
)

// liveConn — живое соединение и решение, по которому оно открыто.
type liveConn struct {
	conn  core.Conn
	route core.Route
	from  entry
	close func()
}

type liveConns struct {
	mu sync.Mutex
	m  map[*liveConn]struct{}
}

// track запоминает соединение до его закрытия (вызвать возвращённую
// функцию).
func (t *Tunnel) track(c core.Conn, route core.Route, from entry, closeFn func()) func() {
	lc := &liveConn{conn: c, route: route, from: from, close: closeFn}
	t.live.mu.Lock()
	if t.live.m == nil {
		t.live.m = make(map[*liveConn]struct{})
	}
	t.live.m[lc] = struct{}{}
	t.live.mu.Unlock()
	return func() {
		t.live.mu.Lock()
		delete(t.live.m, lc)
		t.live.mu.Unlock()
	}
}

// ApplyHygiene закрывает соединения, открытые по правилам старого режима
// (см. core.Transition). Возвращает, сколько закрыто.
//
// Без этого браузер держал бы открытое в «Обходе» соединение (HTTP/2,
// keep-alive) и после выключения обхода: страницы продолжали бы ходить через
// сервер, а сайты проверки адреса показывали бы адрес сервера.
func (t *Tunnel) ApplyHygiene(h core.Hygiene) int {
	rules := t.rules()
	var victims []*liveConn
	t.live.mu.Lock()
	for lc := range t.live.m {
		if (h.CloseMesh && lc.route == core.RouteMesh) ||
			(h.CloseStackInternet && lc.from == entryStack && lc.route != core.RouteMesh) ||
			(h.Reevaluate && core.Decide(rules, lc.conn) != lc.route) {
			victims = append(victims, lc)
		}
	}
	t.live.mu.Unlock()
	if h.ResetFakeIP {
		t.resetServerDNS()
	}
	for _, v := range victims {
		v.close()
	}
	if len(victims) > 0 {
		t.bus.Infof("Закрыто соединений, открытых по прежним правилам: %d — приложения переподключатся по новым", len(victims))
	}
	return len(victims)
}

// LiveConns — сколько соединений сейчас открыто по каждому пути (для
// диагностики и тестов).
func (t *Tunnel) LiveConns() map[core.Route]int {
	out := map[core.Route]int{}
	t.live.mu.Lock()
	for lc := range t.live.m {
		out[lc.route]++
	}
	t.live.mu.Unlock()
	return out
}

// dialDirect — соединение мимо сервера. Сокет привязан к настоящей сети
// (ProtectSocket: IP_UNICAST_IF на Windows, SO_BINDTODEVICE на Linux,
// VpnService.protect на Android) — так прямой трафик обходят свой адаптер
// sing-box (auto_detect_interface), Xray (sockopt.interface) и mihomo
// (auto-detect-interface). Таблица маршрутов при этом не трогается.
//
// Раньше (до ТЗ 52) на Windows к адресу каждого прямого соединения ставился
// маршрут через настоящую сеть (PinDirect). Пока он стоял, к этому адресу
// мимо адаптера шли все программы, а не только наше соединение: их не видел
// ни журнал, ни правила; брандмауэр, который выпускает программы только
// через VPN, их отбивал, и страница грузилась кусками. Теперь маршрут —
// только запасной ход: если привязанный сокет не получил ответа за
// directPinTry (Windows с точкой доступа сверяет ответы с таблицей маршрутов
// и выкидывает их — ноутбук, 02.11), ставится маршрут и соединение
// повторяется; получилось — маршруты включаются для всех прямых соединений
// до смены сети (Kick).
func (t *Tunnel) dialDirect(network, target string, timeout time.Duration) (net.Conn, error) {
	c, _, err := t.dialDirectTimed(network, target, timeout)
	return c, err
}

// directPinTry — сколько ждать ответа привязанному сокету, прежде чем
// поставить маршрут к адресу (см. dialDirect). Обычное соединение в своей
// стране — десятки миллисекунд, Windows повторяет SYN через 1 и 2 с.
const directPinTry = 3 * time.Second

// directAttempt — сколько ждать один адрес, когда за ним есть другие: один
// мёртвый адрес не съедает всё время соединения (как Happy Eyeballs,
// RFC 8305).
const directAttempt = 3 * time.Second

// dialStats — замеры соединения для журнала.
type dialStats struct {
	dns, connect time.Duration
	pinned       bool // понадобился маршрут к адресу (запасной ход)
}

func (t *Tunnel) dialDirectTimed(network, target string, timeout time.Duration) (_ net.Conn, st dialStats, _ error) {
	d := t.directDialer(timeout)
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, st, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, st, err
	}
	deadline := time.Now().Add(timeout)
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a.Unmap()}
	} else {
		t0 := time.Now()
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		ips, err := t.resolver().LookupNetIP(ctx, "ip", host)
		cancel()
		st.dns = time.Since(t0)
		if err != nil {
			return nil, st, err
		}
		addrs = preferIPv4(ips)
	}
	pin := t.cfg.PinDirect
	t0 := time.Now()
	defer func() { st.connect = time.Since(t0) }()
	var lastErr error
	for i, a := range addrs {
		if i == 4 || time.Now().After(deadline) {
			break
		}
		to := netip.AddrPortFrom(a, uint16(port)).String()
		// Маршрут сразу — только если на этой сети без него уже не вышло.
		if pin != nil && t.pinsNeeded.Load() {
			release := pin(a)
			d.Deadline = deadline
			c, err := d.Dial(network, to)
			if err == nil {
				st.pinned = true
				return &pinnedConn{Conn: c, release: release}, st, nil
			}
			release()
			lastErr = err
			continue
		}
		// Привязанный сокет без маршрута — путь sing-box/Xray/mihomo.
		try := deadline
		if pin != nil && network == "tcp" {
			try = minTime(try, time.Now().Add(directPinTry))
		}
		if i+1 < len(addrs) && i+1 < 4 {
			try = minTime(try, time.Now().Add(directAttempt))
		}
		d.Deadline = try
		c, err := d.Dial(network, to)
		if err == nil {
			return c, st, nil
		}
		lastErr = err
		if pin == nil || network != "tcp" || !isTimeout(err) || time.Now().After(deadline) {
			continue
		}
		// Ответа нет — пробуем с маршрутом к адресу (запасной ход).
		release := pin(a)
		d.Deadline = deadline
		c, err = d.Dial(network, to)
		if err != nil {
			release()
			lastErr = err
			continue
		}
		st.pinned = true
		if !t.pinsNeeded.Swap(true) && t.bus != nil {
			t.bus.Warnf("Прямые соединения на этой сети отвечают только с маршрутом к адресу " +
				"(так бывает с включённой точкой доступа) — ставлю маршруты до смены сети")
		}
		return &pinnedConn{Conn: c, release: release}, st, nil
	}
	if lastErr == nil {
		lastErr = errors.New("нет адресов для " + host)
	}
	return nil, st, lastErr
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func (t *Tunnel) resolver() *net.Resolver {
	if t.cfg.Resolver != nil {
		return t.cfg.Resolver
	}
	return net.DefaultResolver
}

// preferIPv4 ставит адреса IPv4 первыми: прямой путь по IPv6 чаще сломан
// (у сети его нет, а у адаптера есть), и ждать его тайм-аута незачем.
func preferIPv4(ips []netip.Addr) []netip.Addr {
	out := make([]netip.Addr, 0, len(ips))
	for _, a := range ips {
		if a = a.Unmap(); a.Is4() {
			out = append(out, a)
		}
	}
	for _, a := range ips {
		if a = a.Unmap(); !a.Is4() {
			out = append(out, a)
		}
	}
	return out
}

// pinnedConn снимает маршрут к адресу, когда соединение закрыто.
type pinnedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *pinnedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// CloseWrite — полузакрытие, как у самого TCP-соединения (см. closeWrite).
func (c *pinnedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.SetReadDeadline(time.Now().Add(30 * time.Second))
}

// RouteUDP решает, что делать с UDP-потоком приложения из стека: src —
// откуда (по нему находится программа), target — куда (имя, если адрес был
// подставным), first — первая датаграмма (из пакета Initial QUIC берётся
// имя, см. core.SniffQUIC). nil — не брать: стек ответит «порт недоступен»,
// и браузер сразу перейдёт с QUIC на TCP.
//
// Напрямую — для исключённых программ и целей «всегда напрямую»: им UDP
// нужен как без программы (NetBird и WireGuard, звонки, игры). Через сервер
// — только через ретранслятор UDP, если он есть. В сеть устройств UDP не
// ходит.
func (t *Tunnel) RouteUDP(src netip.AddrPort, target string, byIP bool, first []byte) func(app net.Conn) {
	var proc string
	var pid int
	forced := t.forced(src.Addr())
	if t.cfg.TunProcessRules && !forced {
		proc, pid = procinfo.LookupUDP(src.Port())
	}
	var name string
	if byIP && t.sniffWanted(target) {
		name = core.SniffQUIC(first)
	}
	route, c, why := t.decideWhy(proc, target, name, forced)
	switch route {
	case core.RouteDirect:
		return func(app net.Conn) { t.serveUDPDirect(app, c, why, proc, pid, byIP, t.udpOwner(proc, src)) }
	case core.RouteServer:
		relay := t.UDPRelay()
		if relay == nil {
			t.udpDropped(c.Target, t.udpRelayWhyNot())
			return nil
		}
		return func(app net.Conn) { t.serveUDPRelay(app, c, relay, t.udpOwner(proc, src)) }
	}
	if route == core.RouteMesh {
		t.udpDropped(c.Target, "в сеть устройств UDP не ходит")
	} else {
		t.udpDropped(c.Target, "запрещено правилами")
	}
	return nil
}

// udpRelayWhyNot — короткая причина, почему UDP через сервер сейчас не идёт
// (для журнала, ТЗ 57: звонок Discord висел на «подключении» молча).
func (t *Tunnel) udpRelayWhyNot() string {
	switch {
	case !t.udpRelayOn():
		return "UDP через сервер выключен (галочка «Пробрасывать UDP» в настройках)"
	case !t.viaServer():
		return "связи с сервером сейчас нет"
	}
	return "ретранслятор UDP на сервере не отвечает (ставится мастером настройки VPS)"
}

// udpDropped пишет в журнал, что UDP-поток отброшен и почему: по разу на
// цель и причину, не больше udpDropMax записей за жизнь туннеля.
func (t *Tunnel) udpDropped(target, why string) {
	if t.bus == nil {
		return
	}
	key := why + "|" + target
	t.udpDropMu.Lock()
	if t.udpDropSeen == nil {
		t.udpDropSeen = map[string]bool{}
	}
	fresh := !t.udpDropSeen[key] && len(t.udpDropSeen) < udpDropMax
	if fresh {
		t.udpDropSeen[key] = true
	}
	t.udpDropMu.Unlock()
	if fresh {
		t.bus.Infof("UDP %s отброшен: %s", target, why)
	}
}

// udpDropMax — сколько разных записей об отброшенном UDP за жизнь туннеля.
const udpDropMax = 40

// udpIdle — сколько поток UDP живёт без единой датаграммы в любую сторону.
// Переменная — для тестов.
var udpIdle = 60 * time.Second

// serveUDPDirect ведёт UDP-поток приложения напрямую мимо сервера.
func (t *Tunnel) serveUDPDirect(app net.Conn, c core.Conn, why core.Why, proc string, pid int, byIP bool, owner string) {
	defer app.Close()
	remote, st, err := t.dialDirectTimed("udp", c.Target, 10*time.Second)
	if err != nil {
		t.bus.Publish(timed(eventConn(proc, pid, c.Target, "udp", byIP, true, err), why, st))
		return
	}
	remote = metered(remote, owner, core.RouteDirect)
	defer remote.Close()
	untrack := t.track(c, core.RouteDirect, entryStack, func() { app.Close(); remote.Close() })
	defer untrack()
	t.bus.Publish(timed(eventConn(proc, pid, c.Target, "udp", byIP, true, nil), why, st))
	pumpDatagrams(app, remote)
}

// pumpDatagrams перекладывает датаграммы в обе стороны, пока поток не
// замолчит на udpIdle (в обе стороны сразу: звонок, где говорит один,
// живёт) или одна из сторон не закроется.
func pumpDatagrams(a, b net.Conn) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	copyLoop := func(dst, src net.Conn, done chan<- struct{}) {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 65535)
		for {
			src.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, err := src.Read(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() &&
					time.Since(time.Unix(0, last.Load())) < udpIdle {
					continue
				}
				return
			}
			last.Store(time.Now().UnixNano())
			if _, err := dst.Write(buf[:n]); err != nil {
				return
			}
		}
	}
	done := make(chan struct{}, 2)
	go copyLoop(b, a, done)
	go copyLoop(a, b, done)
	<-done
	a.Close()
	b.Close()
	<-done
}

// serveUDPRelay ведёт UDP-поток через ретранслятор на сервере.
func (t *Tunnel) serveUDPRelay(app net.Conn, c core.Conn, relay udpRelay, owner string) {
	defer app.Close()
	host, portStr, err := net.SplitHostPort(c.Target)
	if err != nil {
		return
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return
	}
	// Срок бездействия считается в обе стороны (ТЗ 57): поток, по которому
	// только приходят ответы, живёт, как и в pumpDatagrams. Ответ с другого
	// порта цели (так делают серверы звонков) отдаётся тому же потоку.
	idle := udpIdle
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	sessionID := relay.Open(func(data []byte, _ string, _ uint16) {
		last.Store(time.Now().UnixNano())
		traffic.Count(owner, traffic.RouteServer, 0, int64(len(data)))
		app.Write(data)
	})
	defer relay.Close(sessionID)
	untrack := t.track(c, core.RouteServer, entryStack, func() { app.Close() })
	defer untrack()

	buf := make([]byte, 65535)
	step := min(idle, 5*time.Second)
	for {
		app.SetReadDeadline(time.Now().Add(step))
		n, err := app.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() &&
				time.Since(time.Unix(0, last.Load())) < idle {
				continue
			}
			return
		}
		last.Store(time.Now().UnixNano())
		if err := relay.Send(sessionID, host, uint16(port), buf[:n]); err != nil {
			return
		}
		traffic.Count(owner, traffic.RouteServer, int64(n), 0)
	}
}

// udpRelay — то, что нужно от клиента ретранслятора (udprelay.Client).
type udpRelay interface {
	Open(func(data []byte, fromHost string, fromPort uint16)) uint32
	Close(sessionID uint32)
	Send(sessionID uint32, host string, port uint16, data []byte) error
}

// DialSelf — соединение самой программы (календарь к Nextcloud и т. п.):
// куда вести — решает ядро, как для любой программы (имя .mesh — в сеть
// устройств, домашний адрес — напрямую).
func (t *Tunnel) DialSelf(target string) (net.Conn, error) {
	route, _ := t.decide("", target)
	c, err := t.dialRoute(route, target)
	if err != nil {
		return nil, err
	}
	return metered(c, selfName, route), nil
}
