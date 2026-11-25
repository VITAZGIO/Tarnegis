package mobile

// ТЗ 51: «всегда напрямую» на телефоне для сайта из готовой группы. Имя CDN
// ВКонтакте (его нет в списке словом, только группа «@vk») получает от DNS
// телефона настоящий адрес, резолвером сети телефона; соединение на этот
// адрес — напрямую, и его UDP (QUIC) не уходит в ретранслятор сервера.

import (
	"net"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	core2 "sshtunnel/internal/core"
	"sshtunnel/internal/events"
	"sshtunnel/internal/routing"
	"sshtunnel/internal/tunnel"

	"sshtunnel/android/core"
)

func TestГруппаВКонтактеНапрямуюНаТелефоне(t *testing.T) {
	direct := routing.NewDirectList(routing.SplitEntries("@vk"))
	tun := tunnel.New(tunnel.Config{Direct: direct, FakeIP: true}, events.NewBus())
	pool, err := core.NewFakePool("198.18.0.0/15")
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	cdn := net.IPv4(203, 0, 113, 10).To4() // «настоящий» адрес CDN от резолвера сети
	dns := &core.DNS{
		Pool:   pool,
		Direct: func(name string) bool { return core2.DecideDNS(tun.Rules(), name) == core2.DNSReal },
		Local: func(name string) ([]net.IP, error) {
			asked = append(asked, name)
			ips := []net.IP{cdn}
			tun.LearnDirect(name, ips)
			return ips, nil
		},
	}
	ip := askA(t, dns, "sun9-12.userapi.com")
	if !ip.Equal(cdn) || len(asked) != 1 {
		t.Fatalf("CDN группы: адрес %v, резолвер сети телефона спрошен %v", ip, asked)
	}
	if other := askA(t, dns, "example.org"); !pool.Contains(other.String()) {
		t.Fatalf("имя не из списка получило не подставной адрес: %v", other)
	}

	src := netip.MustParseAddrPort("10.111.0.2:40000")
	// Ретранслятора нет: путь «через сервер» для UDP — nil (отказ), а прямой
	// путь — не nil. Значит, UDP к адресу CDN идёт напрямую, а не в сервер.
	if tun.RouteUDP(src, net.JoinHostPort(cdn.String(), "443"), true, nil) == nil {
		t.Fatal("UDP к адресу CDN из группы ушёл бы через сервер")
	}
	if tun.RouteUDP(src, "93.184.216.34:443", true, nil) != nil {
		t.Fatal("UDP к чужому адресу пошёл бы напрямую")
	}
}

func askA(t *testing.T, d *core.DNS, name string) net.IP {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
	b.StartQuestions()
	b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name + "."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
	q, _ := b.Finish()
	out, err := d.Answer(q)
	if err != nil {
		t.Fatal(err)
	}
	var p dnsmessage.Parser
	if _, err := p.Start(out); err != nil {
		t.Fatal(err)
	}
	p.SkipAllQuestions()
	ans, err := p.AllAnswers()
	if err != nil || len(ans) == 0 {
		t.Fatalf("%s: нет ответа (%v)", name, err)
	}
	a := ans[0].Body.(*dnsmessage.AResource).A
	return net.IP(a[:])
}
