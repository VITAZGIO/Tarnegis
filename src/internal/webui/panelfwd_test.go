package webui

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/config"
)

// fakeForwardServer — SSH-сервер, который на direct-tcpip к 127.0.0.1:47823
// (панель) подключает к target; к любому другому адресу — отказ. target
// пустой — «панель не поставлена».
func fakeForwardServer(t *testing.T, target string) string {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(mustEd25519(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					c.Close()
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					var dest struct {
						Host  string
						Port  uint32
						OHost string
						OPort uint32
					}
					if nc.ChannelType() != "direct-tcpip" || ssh.Unmarshal(nc.ExtraData(), &dest) != nil ||
						dest.Host != "127.0.0.1" || dest.Port != 47823 || target == "" {
						nc.Reject(ssh.ConnectionFailed, "connect failed")
						continue
					}
					ch, rq, err := nc.Accept()
					if err != nil {
						continue
					}
					go ssh.DiscardRequests(rq)
					go func() {
						defer ch.Close()
						up, err := net.Dial("tcp", target)
						if err != nil {
							return
						}
						defer up.Close()
						go io.Copy(up, ch)
						io.Copy(ch, up)
					}()
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func usePanelDial(t *testing.T, srvAddr string, gotUser *string) {
	t.Helper()
	old := panelDial
	panelDial = func(user, addr, keyPath string) (*ssh.Client, error) {
		*gotUser = user
		return ssh.Dial("tcp", srvAddr, &ssh.ClientConfig{
			User: user, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
		})
	}
	t.Cleanup(func() { panelDial = old })
}

func TestPanelForwardOpensAndCloses(t *testing.T) {
	panelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "panel-ok")
	}))
	defer panelSrv.Close()
	srvAddr := fakeForwardServer(t, strings.TrimPrefix(panelSrv.URL, "http://"))
	var user string
	usePanelDial(t, srvAddr, &user)

	var f panelForward
	st, err := f.open(config.Profile{ID: "p1", Name: "Тест", Host: "server.invalid", SSHPort: 22, AdminUser: "ops"}, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if user != "ops" {
		t.Errorf("проброс под %q, ожидался администратор ops", user)
	}
	if !st.Open || st.Port == 0 || !strings.HasPrefix(st.URL, "http://127.0.0.1:") {
		t.Fatalf("состояние: %+v", st)
	}
	resp, err := http.Get(st.URL)
	if err != nil {
		t.Fatalf("панель через проброс: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "panel-ok" {
		t.Fatalf("ответ панели: %q", body)
	}

	// Повторное открытие того же сервера — тот же порт, не второй проброс.
	st2, err := f.open(config.Profile{ID: "p1", Host: "server.invalid"}, "")
	if err != nil || st2.Port != st.Port {
		t.Errorf("повторное открытие: %+v, %v", st2, err)
	}

	f.close("")
	if f.state().Open {
		t.Fatal("после закрытия проброс всё ещё открыт")
	}
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(st.Port), time.Second); err == nil {
		c.Close()
		t.Fatal("порт проброса всё ещё принимает соединения")
	}
}

func TestPanelForwardDefaultsToRoot(t *testing.T) {
	panelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer panelSrv.Close()
	srvAddr := fakeForwardServer(t, strings.TrimPrefix(panelSrv.URL, "http://"))
	var user string
	usePanelDial(t, srvAddr, &user)
	var f panelForward
	defer f.close("")
	if _, err := f.open(config.Profile{ID: "p1", Host: "server.invalid"}, ""); err != nil {
		t.Fatal(err)
	}
	if user != "root" {
		t.Errorf("без администратора в профиле — %q, ожидался root", user)
	}
}

// Панели на сервере нет — понятная ошибка, а не пустая страница в браузере.
func TestPanelForwardNoPanel(t *testing.T) {
	srvAddr := fakeForwardServer(t, "")
	var user string
	usePanelDial(t, srvAddr, &user)
	var f panelForward
	_, err := f.open(config.Profile{ID: "p1", Host: "server.invalid"}, "admin")
	if err == nil || !strings.Contains(err.Error(), "не отвечает панель") {
		t.Fatalf("ожидалась ошибка «не отвечает панель», получено %v", err)
	}
	if f.state().Open {
		t.Error("проброс открыт, хотя панели нет")
	}
}
