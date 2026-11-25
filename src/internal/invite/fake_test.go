package invite

// Поддельный сервер по контракту ТЗ 47 (части B и C): SSH в памяти,
// tun_join пускает с любым ключом только к exec «join», учётная запись —
// только со своим ключом и только к 127.0.0.1:47832, где отвечает профиль.

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

type fakeServer struct {
	t       *testing.T
	ln      net.Listener
	hostKey ssh.Signer
	// extra — ещё ключи хоста (настоящий sshd показывает ECDSA, ed25519 и
	// RSA — какой, решает клиент своим списком алгоритмов).
	extra []ssh.Signer

	mu       sync.Mutex
	tokens   map[string]string // токен → "" (свободен) | "used" | "expired" | "frozen"
	accounts map[string]string // id → открытый ключ (Marshal)
	profile  func(id string) (int, string)
	joins    int
	lastReq  JoinRequest
	n        int
}

func newFakeServer(t *testing.T) *fakeServer {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	hk, _ := ssh.NewSignerFromKey(priv)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{t: t, ln: ln, hostKey: hk, tokens: map[string]string{}, accounts: map[string]string{}}
	f.profile = func(id string) (int, string) {
		return 200, fmt.Sprintf(`{"v":1,"id":%q,"name":"Телефон","rev":3,"servers":[{"name":"А","host":"127.0.0.1","port":%d,"hk":%q,"flag":"de"},{"name":"Б","host":"198.51.100.7","port":22,"hk":%q}],"mesh":true,"frozen":false,"future":{"x":1}}`,
			id, f.port(), f.hkLine(), f.hkLine())
	}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeServer) port() int { return f.ln.Addr().(*net.TCPAddr).Port }
func (f *fakeServer) hkLine() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(f.hostKey.PublicKey())))
}

func (f *fakeServer) invite(tok string, exp int64) string {
	return Encode(Invite{V: 1, Host: "127.0.0.1", Port: f.port(), HK: f.hkLine(), Tok: tok, Exp: exp, Name: "Телефон"})
}

func (f *fakeServer) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(c)
	}
}

func (f *fakeServer) handle(c net.Conn) {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(m ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if m.User() == JoinUser {
				return &ssh.Permissions{Extensions: map[string]string{"key": string(key.Marshal())}}, nil
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if k, ok := f.accounts[m.User()]; ok && k == string(key.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("нет")
		},
	}
	cfg.AddHostKey(f.hostKey)
	for _, k := range f.extra {
		cfg.AddHostKey(k)
	}
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		switch {
		case nc.ChannelType() == "session" && sc.User() == JoinUser:
			ch, rq, _ := nc.Accept()
			go f.join(ch, rq, sc.Permissions.Extensions["key"])
		case nc.ChannelType() == "direct-tcpip" && sc.User() != JoinUser:
			var d struct {
				Host  string
				Port  uint32
				OHost string
				OPort uint32
			}
			ssh.Unmarshal(nc.ExtraData(), &d)
			if d.Host != "127.0.0.1" || d.Port != 47832 {
				nc.Reject(ssh.Prohibited, "только 47832")
				continue
			}
			ch, rq, _ := nc.Accept()
			go ssh.DiscardRequests(rq)
			go f.http(ch, sc.User())
		default:
			nc.Reject(ssh.Prohibited, "нельзя")
		}
	}
}

func (f *fakeServer) join(ch ssh.Channel, reqs <-chan *ssh.Request, key string) {
	defer ch.Close()
	for r := range reqs {
		if r.Type != "exec" {
			r.Reply(false, nil)
			continue
		}
		cmd := string(r.Payload[4:])
		r.Reply(cmd == "join", nil)
		if cmd != "join" {
			return
		}
		line, _ := bufio.NewReader(ch).ReadBytes('\n')
		var req JoinRequest
		json.Unmarshal(line, &req)
		f.mu.Lock()
		f.joins++
		f.lastReq = req
		st, ok := f.tokens[req.Tok]
		var resp JoinResponse
		switch {
		case !ok:
			resp = JoinResponse{Err: "unknown"}
		case st != "":
			resp = JoinResponse{Err: st}
		default:
			f.n++
			id := fmt.Sprintf("tun_%04d", f.n)
			f.accounts[id] = key
			f.tokens[req.Tok] = "used"
			resp = JoinResponse{OK: true, ID: id, Name: req.Name}
		}
		f.mu.Unlock()
		out, _ := json.Marshal(resp)
		ch.Write(append(out, '\n'))
		ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
		return
	}
}

func (f *fakeServer) http(ch ssh.Channel, user string) {
	defer ch.Close()
	req, err := http.ReadRequest(bufio.NewReader(ch))
	if err != nil {
		return
	}
	code, body := 404, `{"error":"нет"}`
	if req.URL.Path == ProfilePath {
		code, body = f.profile(user)
	}
	fmt.Fprintf(ch, "HTTP/1.1 %d X\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, len(body), body)
}
