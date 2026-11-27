package mesh

// ТЗ 57: клиент Linux под пользователем, которому правила доступа не дают
// видеть «@servers», показывал в «Устройствах» только клиентов, а карта —
// один сервер. Серверы, которые устройство знает само (свои соединения и
// сеть серверов), должны быть в списке со своим состоянием.

import (
	"net/netip"
	"testing"
)

func TestСвоиСерверыВСпискеДажеБезПрава(t *testing.T) {
	c := New(Config{Key: "k", DeviceID: "dev", Name: "linux-box", StateDir: t.TempDir()}, nil, nil)
	c.mu.Lock()
	c.status.Peers = []Peer{
		{ID: "p1", Name: "ноут", Host: "laptop", IP: netip.MustParseAddr("198.18.0.5"), Platform: "windows", Online: true},
		// Этот сервер meshd прислал сам — второго такого быть не должно.
		{ID: PeerID("server" + string(rune(0)) + "s3"), Name: "Берлин", Host: "berlin", IP: netip.MustParseAddr("198.19.0.3"), Platform: "server", Online: true},
	}
	c.links = []*srvLink{
		{key: "a.example:22", info: wireServer{ID: "s1", Name: "Москва", IP: "198.19.255.254"}, state: "online",
			ctrl: func(wire) error { return nil }, netSet: true,
			netAds: []wireAd{
				{ID: "s1", Name: "Москва", Host: "a.example", Sn: "n"},
				{ID: "s2", Name: "Лондон", Host: "b.example", Sn: "n"},
				{ID: "s3", Name: "Берлин", Host: "c.example", Sn: "n"},
			}},
		{key: "d.example:22", info: wireServer{ID: "s4", Name: "Париж", IP: "198.19.0.4"}, state: "error"},
	}
	c.mu.Unlock()

	st := c.Status()
	servers := map[string]Peer{}
	for _, p := range st.Peers {
		if p.IsServer() {
			if _, dup := servers[p.Name]; dup {
				t.Fatalf("сервер %s дважды", p.Name)
			}
			servers[p.Name] = p
		}
	}
	if p, ok := servers["Москва"]; !ok || !p.Online || p.IP.String() != "198.19.255.254" || p.ID != PeerID("server"+string(rune(0))+"s1") {
		t.Fatalf("свой сервер на связи: %+v (%v)", p, ok)
	}
	if p, ok := servers["Париж"]; !ok || p.Online {
		t.Fatalf("сервер без связи: %+v (%v)", p, ok)
	}
	if len(servers) != 3 {
		t.Fatalf("серверы в списке: %v", servers)
	}
	if len(st.KnownServers) != 1 || st.KnownServers[0].Name != "Лондон" || st.KnownServers[0].Online {
		t.Fatalf("известные серверы сети: %+v", st.KnownServers)
	}
	if !st.Peers[0].IsServer() {
		t.Fatal("серверы не первыми")
	}
}
