package udprelay

// ТЗ 57: стенд «как звонок Discord» — настоящий бинарник ретранслятора,
// цели на высоких портах, ответ с другого порта, много целей из одной
// сессии, много датаграмм и пауза дольше срока бездействия.

import (
	"net"
	"sync"
	"testing"
	"time"
)

// voiceServer — цель звонка: отвечает не с того порта, на который пришёл
// запрос (reply), и запоминает, с какого адреса ретранслятора её достали.
type voiceServer struct {
	in, reply *net.UDPConn
	mu        sync.Mutex
	seen      []string
}

func newVoiceServer(t *testing.T) *voiceServer {
	t.Helper()
	in, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	v := &voiceServer{in: in, reply: reply}
	t.Cleanup(func() { in.Close(); reply.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := in.ReadFromUDP(buf)
			if err != nil {
				return
			}
			v.mu.Lock()
			v.seen = append(v.seen, from.String())
			v.mu.Unlock()
			reply.WriteToUDP(buf[:n], from)
		}
	}()
	return v
}

func (v *voiceServer) port() uint16      { return uint16(v.in.LocalAddr().(*net.UDPAddr).Port) }
func (v *voiceServer) replyPort() uint16 { return uint16(v.reply.LocalAddr().(*net.UDPAddr).Port) }
func (v *voiceServer) sources() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.seen...)
}

type reply struct {
	data string
	port uint16
}

func TestЗвонокКакDiscordЧерезРетранслятор(t *testing.T) {
	const idle = 1500 * time.Millisecond
	relayAddr := startRelay(t, "-idle", idle.String())
	conn, err := net.Dial("tcp", relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := NewClient(conn)

	servers := []*voiceServer{newVoiceServer(t), newVoiceServer(t), newVoiceServer(t)}
	got := make(chan reply, 4096)
	id := client.Open(func(data []byte, _ string, port uint16) { got <- reply{string(data), port} })

	exchange := func(v *voiceServer, msg string) {
		t.Helper()
		if err := client.Send(id, "127.0.0.1", v.port(), []byte(msg)); err != nil {
			t.Fatal(err)
		}
		select {
		case r := <-got:
			if r.data != msg {
				t.Fatalf("ответ %q вместо %q", r.data, msg)
			}
			if r.port != v.replyPort() {
				t.Fatalf("ответ с порта %d, ожидался %d (другой порт цели)", r.port, v.replyPort())
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("нет ответа на %q от порта %d", msg, v.port())
		}
	}

	// Много датаграмм на несколько целей из одной сессии.
	for i := 0; i < 300; i++ {
		exchange(servers[i%len(servers)], "pkt")
	}
	// Все цели видят один и тот же адрес ретранслятора — как одна «дырка»
	// NAT у звонка (по нему сервер звонка узнаёт внешний адрес).
	first := servers[0].sources()[0]
	for _, v := range servers {
		for _, s := range v.sources() {
			if s != first {
				t.Fatalf("адрес сессии сменился: %s и %s", first, s)
			}
		}
	}

	// Пауза короче срока бездействия — адрес тот же.
	time.Sleep(idle / 2)
	exchange(servers[0], "after-short-gap")
	if s := servers[0].sources(); s[len(s)-1] != first {
		t.Fatalf("после короткой паузы адрес сменился: %s", s[len(s)-1])
	}

	// Пауза дольше срока — ретранслятор закрыл сокет, но следующая
	// датаграмма по той же сессии снова проходит (новый сокет).
	time.Sleep(idle + idle/2)
	exchange(servers[1], "after-long-gap")
}
