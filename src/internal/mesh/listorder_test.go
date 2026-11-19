package mesh

import (
	"net/netip"
	"strings"
	"testing"
)

func TestSortForListСерверыПервыми(t *testing.T) {
	ip := func(s string) netip.Addr { return netip.MustParseAddr(s) }
	peers := []Peer{
		{Name: "ноутбук", Host: "laptop", IP: ip("198.18.0.5")},
		{Name: "Germany", Host: "germany", IP: ip("198.19.255.254"), Platform: "server", Online: true},
		{Name: "телефон", Host: "phone", IP: ip("198.18.0.7"), Online: true},
		{Name: "Amsterdam", Host: "amsterdam", IP: ip("198.19.255.250"), Platform: "server"},
		{Name: "ПК", Host: "pc", IP: ip("198.18.0.2"), Online: true, Self: true},
		{Name: "London", Host: "london", IP: ip("198.19.255.251"), Platform: "server", Online: true},
		{Name: "архив", Host: "archive", IP: ip("198.18.0.9")},
		{Name: "", Host: "box", IP: ip("198.18.0.3"), Online: true},
	}
	SortForList(peers)
	var got []string
	for _, p := range peers {
		got = append(got, p.Host)
	}
	want := "germany london amsterdam box pc phone archive laptop"
	if s := strings.Join(got, " "); s != want {
		t.Fatalf("порядок %q, ждал %q", s, want)
	}
}

func TestSortForListРегистрИРавныеИмена(t *testing.T) {
	ip := func(s string) netip.Addr { return netip.MustParseAddr(s) }
	peers := []Peer{
		{Name: "pc", Host: "pc-2", IP: ip("198.18.0.4")},
		{Name: "PC", Host: "pc", IP: ip("198.18.0.3")},
		{Name: "Alpha", Host: "alpha", IP: ip("198.18.0.8")},
	}
	SortForList(peers)
	if peers[0].Host != "alpha" || peers[1].Host != "pc" || peers[2].Host != "pc-2" {
		t.Fatalf("порядок %v %v %v", peers[0].Host, peers[1].Host, peers[2].Host)
	}
}
