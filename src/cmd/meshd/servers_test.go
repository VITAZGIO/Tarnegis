package main

import (
	"net"
	"testing"
)

// ТЗ 22: список серверов сети — отдельной строкой op servers, с отметкой
// «учётная запись есть» по SSH-пользователю устройства; в welcome — прежний
// короткий список без этого сервера (его понимают старые устройства).
func TestСписокСерверовСетиСУчётнойЗаписью(t *testing.T) {
	s, _ := testServer(t)
	users := map[string]string{}
	s.connUser = func(c net.Conn) string { return users[c.LocalAddr().String()+c.RemoteAddr().String()] }
	shared := []string{"tun_a"}
	if err := s.setKnown([]serverAd{
		{Name: "Сам", Host: "self.example", ID: s.self.ID, Sn: "N"},
		{Name: "Англия", Host: "uk.example", Port: 2222, ID: "m2", HK: "ssh-ed25519 AAAA", Sn: "N"},
	}, &shared); err != nil {
		t.Fatal(err)
	}
	addr := serve(t, s)
	s.connUser = func(net.Conn) string { return "tun_a" }
	wa, welcome := helloConn(t, addr, msg{Net: "k", Device: "a", Name: "a"})
	if len(welcome.Servers) != 1 || welcome.Servers[0].Host != "uk.example" || welcome.Servers[0].HK != "" {
		t.Fatalf("welcome: %+v", welcome.Servers)
	}
	m := wa.next(t, "servers")
	if len(m.Servers) != 2 || !m.Servers[1].Acct || m.Servers[1].HK == "" || m.Servers[1].Sn != "N" {
		t.Fatalf("servers для своего: %+v", m.Servers)
	}
	s.connUser = func(net.Conn) string { return "root" }
	wb, _ := helloConn(t, addr, msg{Net: "k", Device: "b", Name: "b"})
	if m := wb.next(t, "servers"); m.Servers[1].Acct {
		t.Fatalf("у чужого пользователя отметка учётной записи: %+v", m.Servers)
	}
	// Новый список уходит устройствам на связи сразу.
	if err := s.setKnown([]serverAd{{Host: "fr.example", ID: "m3", Sn: "N"}}, nil); err != nil {
		t.Fatal(err)
	}
	if m := wa.next(t, "servers"); len(m.Servers) != 1 || m.Servers[0].ID != "m3" || !m.Servers[0].Acct {
		t.Fatalf("после смены: %+v", m.Servers)
	}
}
