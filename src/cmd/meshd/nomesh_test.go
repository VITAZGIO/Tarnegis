package main

// ТЗ 47: устройству, у которого в панели выключена «Сеть устройств»,
// meshd отказывает сам — не только программа не показывает сеть.

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestБезСетиУстройствОтказ(t *testing.T) {
	s := newServer("")
	var mu sync.Mutex
	users := map[string]string{}
	s.connUser = func(c net.Conn) string {
		mu.Lock()
		defer mu.Unlock()
		return users[c.RemoteAddr().String()]
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	open := func(user string, m msg) (net.Conn, *bufio.Reader) {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		mu.Lock()
		users[c.LocalAddr().String()] = user
		mu.Unlock()
		writeMsg(c, m)
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		return c, bufio.NewReader(c)
	}
	read := func(r *bufio.Reader) (msg, error) {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return msg{}, err
		}
		var m msg
		json.Unmarshal(line, &m)
		return m, nil
	}

	// Пока разрешено — входит как обычно.
	_, phR := open("tun_ph", msg{Op: "hello", Net: "k", Device: "ph", Name: "ph"})
	if m, _ := read(phR); m.Op != "welcome" {
		t.Fatalf("нет welcome: %+v", m)
	}
	_, pcR := open("tun_pc", msg{Op: "hello", Net: "k", Device: "pc", Name: "pc"})
	pcW, _ := read(pcR)

	// Панель выключила сеть устройств телефону: живое соединение рвётся.
	if err := s.setPolicy(policy{NoMesh: []string{"tun_ph"}}); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := read(phR); err != nil {
			break
		}
	}
	// Заново не войти.
	_, r := open("tun_ph", msg{Op: "hello", Net: "k", Device: "ph", Name: "ph"})
	m, _ := read(r)
	if m.Op != "error" || !strings.Contains(m.Error, "не разрешена") {
		t.Fatalf("пустили без права на сеть: %+v", m)
	}
	// И звонить по старой записи устройства — тоже нельзя.
	_, r = open("tun_ph", msg{Op: "dial", Net: "k", Device: "ph", To: pcW.IP, Port: 22})
	if m, _ := read(r); m.Op != "error" {
		t.Fatalf("пустили звонок без права на сеть: %+v", m)
	}
	// Остальным — как прежде (правила доступа выключены).
	_, r = open("tun_pc", msg{Op: "hello", Net: "k", Device: "pc2", Name: "pc2"})
	if m, _ := read(r); m.Op != "welcome" {
		t.Fatalf("другому устройству отказ: %+v", m)
	}
	if !s.policy.meshOff("tun_ph") || s.policy.meshOff("tun_pc") || s.policy.meshOff(serversGroup) {
		t.Fatal("meshOff")
	}
	if (policy{NoMesh: []string{""}}).validate() == nil {
		t.Fatal("пустой пользователь в nomesh принят")
	}
}
