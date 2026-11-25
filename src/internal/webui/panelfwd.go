// «Открыть панель сервера»: временный проброс 127.0.0.1:<свободный порт> на
// этом компьютере → 127.0.0.1:47823 на сервере, по SSH тем ключом, что
// положил мастер настройки VPS.
//
// Зачем: панель (ssh_tunnel_panel) по умолчанию слушает только петлевой адрес
// сервера — наружу её не видно. Пока сеть устройств не настроена, другого
// пути к ней нет, кроме ssh -L руками. Здесь то же самое — одной кнопкой.
//
// Проброс идёт под администратором сервера (Profile.AdminUser, пусто — root),
// а не под пользователем туннеля: tunnel-user.sh закрывает ему доступ к
// самому серверу (127.0.0.0/8), в том числе к панели. Проброс один на всю
// программу; закрывается кнопкой «Закрыть», при «Выходе» и вместе с самой
// программой (он живёт в её процессе).
package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/config"
	"sshtunnel/internal/hostkey"
	"sshtunnel/internal/tunnel"
)

// panelForward — текущий проброс к панели. Нулевое значение — закрыт.
type panelForward struct {
	mu        sync.Mutex
	ln        net.Listener
	client    *ssh.Client
	profileID string
	name      string
	port      int
	// lastErr — почему проброс закрылся сам (оборвалась связь с сервером);
	// показывается один раз в ответе status.
	lastErr string
	// leaseUntil — проброс держит отдельное окно «Панель сервера»: окно раз в
	// несколько секунд продлевает аренду (op ping). Окно закрыли — продлений
	// нет, и проброс закрывается сам. Нулевое — аренды нет (полоска в окне
	// программы, закрывается кнопкой).
	leaseUntil time.Time
}

// panelLease — на сколько продлевает аренду одно сообщение окна.
var panelLease = 15 * time.Second

// ping продлевает аренду окна; false — проброса уже нет.
func (f *panelForward) ping() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ln == nil {
		return false
	}
	f.leaseUntil = time.Now().Add(panelLease)
	return true
}

// watchLease закрывает проброс, когда окно перестало продлевать аренду.
func (f *panelForward) watchLease(ln net.Listener) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		f.mu.Lock()
		if f.ln != ln {
			f.mu.Unlock()
			return
		}
		if !f.leaseUntil.IsZero() && time.Now().After(f.leaseUntil) {
			f.closeLocked("окно панели закрыто")
			f.mu.Unlock()
			return
		}
		f.mu.Unlock()
	}
}

// panelFwdState — то, что видит окно.
type panelFwdState struct {
	Open      bool   `json:"open"`
	ProfileID string `json:"profileId,omitempty"`
	Name      string `json:"name,omitempty"`
	Port      int    `json:"port,omitempty"`
	URL       string `json:"url,omitempty"`
	Closed    string `json:"closed,omitempty"` // почему закрылся сам
}

func (f *panelForward) stateLocked() panelFwdState {
	if f.ln == nil {
		st := panelFwdState{Closed: f.lastErr}
		f.lastErr = ""
		return st
	}
	return panelFwdState{Open: true, ProfileID: f.profileID, Name: f.name, Port: f.port,
		URL: "http://127.0.0.1:" + strconv.Itoa(f.port) + "/"}
}

func (f *panelForward) state() panelFwdState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stateLocked()
}

// close закрывает проброс. reason непустой — закрылся сам, окно об этом
// скажет.
func (f *panelForward) close(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeLocked(reason)
}

func (f *panelForward) closeLocked(reason string) {
	if f.ln != nil {
		f.ln.Close()
	}
	if f.client != nil {
		f.client.Close()
	}
	if f.ln != nil && reason != "" {
		f.lastErr = reason
	}
	f.ln, f.client, f.profileID, f.name, f.port = nil, nil, "", "", 0
	f.leaseUntil = time.Time{}
}

// panelDial — вход на сервер ключом профиля под администратором. Отдельная
// переменная, чтобы тест мог подставить свой сервер.
var panelDial = func(user, addr, keyPath string) (*ssh.Client, error) {
	signer, err := loadSigner(keyPath)
	if err != nil {
		return nil, fmt.Errorf("не прочитать ключ %s: %w", keyPath, err)
	}
	cfg := &ssh.ClientConfig{
		User:              user,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   hostkey.Callback(config.KnownHostsPath(), nil),
		HostKeyAlgorithms: hostkey.Algorithms(config.KnownHostsPath(), addr),
		Timeout:           vpsDialTimeout,
	}
	return ssh.Dial("tcp", addr, cfg)
}

// open поднимает проброс для профиля p. Уже открыт для него же — отдаёт
// текущий; для другого — закрывает старый.
func (f *panelForward) open(p config.Profile, adminUser string, window bool) (panelFwdState, error) {
	if strings.TrimSpace(p.Host) == "" {
		return panelFwdState{}, errors.New("у сервера не указан адрес")
	}
	user := strings.TrimSpace(adminUser)
	if user == "" {
		user = strings.TrimSpace(p.AdminUser)
	}
	if user == "" {
		user = "root"
	}
	keyPath := p.KeyPath
	if strings.TrimSpace(keyPath) == "" {
		keyPath = config.DetectKeyPath()
	}
	port := p.SSHPort
	if port <= 0 {
		port = 22
	}
	addr := net.JoinHostPort(strings.TrimSpace(p.Host), strconv.Itoa(port))

	f.mu.Lock()
	if f.ln != nil && f.profileID == p.ID {
		if window {
			f.leaseUntil = time.Now().Add(panelLease)
		}
		st := f.stateLocked()
		f.mu.Unlock()
		return st, nil
	}
	f.closeLocked("")
	f.mu.Unlock()

	client, err := panelDial(user, addr, keyPath)
	if err != nil {
		return panelFwdState{}, panelDialErr(err, user)
	}
	// Сразу проверяем, что панель на сервере отвечает: иначе браузер
	// открылся бы на пустую страницу без объяснений.
	probe, err := client.Dial("tcp", panelLocalAddr)
	if err != nil {
		client.Close()
		return panelFwdState{}, errors.New("на сервере не отвечает панель (" + panelLocalAddr + ") — " +
			"она не поставлена или остановлена; поставить — мастер настройки VPS, галочка «веб-панель»")
	}
	probe.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		client.Close()
		return panelFwdState{}, fmt.Errorf("не открыть порт на этом компьютере: %w", err)
	}

	f.mu.Lock()
	f.ln, f.client, f.profileID, f.name = ln, client, p.ID, p.Name
	f.port = ln.Addr().(*net.TCPAddr).Port
	if window {
		f.leaseUntil = time.Now().Add(panelLease)
	}
	st := f.stateLocked()
	f.mu.Unlock()

	go f.serve(ln, client)
	go f.watchLease(ln)
	go func() {
		// Связь с сервером оборвалась — проброс бесполезен, закрываем.
		client.Wait()
		f.mu.Lock()
		if f.client == client {
			f.closeLocked("связь с сервером оборвалась")
		}
		f.mu.Unlock()
	}()
	return st, nil
}

func (f *panelForward) serve(ln net.Listener, client *ssh.Client) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			remote, err := client.Dial("tcp", panelLocalAddr)
			if err != nil {
				return
			}
			defer remote.Close()
			done := make(chan struct{}, 2)
			go func() { io.Copy(remote, c); done <- struct{}{} }()
			go func() { io.Copy(c, remote); done <- struct{}{} }()
			<-done
		}()
	}
}

// panelDialErr — понятный текст, почему не зашли на сервер ключом.
func panelDialErr(err error, user string) error {
	if tunnel.IsAuthError(err) {
		return fmt.Errorf("сервер не пустил %s по ключу этого компьютера — кнопка работает для серверов, "+
			"настроенных мастером; если администратор там не %s, впиши его в поле «Администратор сервера»", user, user)
	}
	return classifySSHErr(err)
}

// handlePanelFwd — GET: состояние проброса; POST {"op":"open","id","adminUser"}
// или {"op":"close"}.
func (s *Server) handlePanelFwd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, s.panelFwd.state())
		return
	}
	var req struct {
		Op        string `json:"op"`
		ID        string `json:"id"`
		AdminUser string `json:"adminUser"`
		// Window — проброс держит отдельное окно (аренда, см. leaseUntil).
		Window bool `json:"window"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "не разобрал запрос: " + err.Error()})
		return
	}
	switch req.Op {
	case "close":
		s.panelFwd.close("")
		writeJSON(w, s.panelFwd.state())
	case "ping":
		s.panelFwd.ping()
		writeJSON(w, s.panelFwd.state())
	case "open":
		var prof *config.Profile
		cfg := s.app.Config()
		for i := range cfg.Profiles {
			if cfg.Profiles[i].ID == req.ID {
				prof = &cfg.Profiles[i]
				break
			}
		}
		if prof == nil {
			writeJSON(w, map[string]string{"error": "сервер не найден — сохрани его и попробуй ещё раз"})
			return
		}
		if req.AdminUser != "" && !usernameRe.MatchString(strings.TrimSpace(req.AdminUser)) {
			writeJSON(w, map[string]string{"error": "имя администратора: только латиница, цифры, «-» и «_», с буквы"})
			return
		}
		st, err := s.panelFwd.open(*prof, req.AdminUser, req.Window)
		if err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, st)
	default:
		writeJSON(w, map[string]string{"error": "op: open, ping или close"})
	}
}
