package webui

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/events"
)

func mustEd25519(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// ---------- поддельный SSH-сервер администратора ----------
//
// Настоящих apt-get/systemctl/ufw в тестах, конечно, нет — сервер просто
// узнаёт по содержимому команды, какой из шагов (положить ключ, harden.sh,
// tunnel-user.sh, cockpit) до него дошёл, и отвечает заранее заданным
// построчным выводом и кодом выхода. Ключевая проверка — не то, что делает
// настоящий bash, а то, что runVpsSetup обращается к серверу в правильном
// порядке, с правильно подставленными параметрами, и не идёт дальше, если
// предыдущий шаг не удался.

type execCall struct {
	cmd   string
	stdin string
}

type fakeAdminServer struct {
	addr     string
	password string

	mu           sync.Mutex
	keyLine      string
	keyInstalled bool
	acceptKey    bool // разрешить ли вход по ключу вообще (проверка шага 6 ТЗ)
	calls        []execCall
	hardenExit   uint32
	tunnelExit   uint32
	// sudoMode — как ведёт себя sudo у не-root пользователя: "password"
	// (спрашивает пароль входа), "nopass" (NOPASSWD), "none" (нет в sudoers).
	sudoMode string
}

func newFakeAdminServer(t *testing.T, password string) *fakeAdminServer {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(mustEd25519(t))
	if err != nil {
		t.Fatal(err)
	}

	s := &fakeAdminServer{password: password, acceptKey: true}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) != s.password {
				return nil, fmt.Errorf("неверный пароль")
			}
			return &ssh.Permissions{}, nil
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.acceptKey || !s.keyInstalled {
				return nil, fmt.Errorf("ключ не подходит")
			}
			line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
			if line != s.keyLine {
				return nil, fmt.Errorf("чужой ключ")
			}
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.addr = ln.Addr().String()
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handleConn(c, cfg)
		}
	}()
	return s
}

func (s *fakeAdminServer) handleConn(c net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ch, requests)
	}
}

func (s *fakeAdminServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	for req := range requests {
		if req.Type != "exec" {
			if req.WantReply {
				req.Reply(false, nil)
			}
			continue
		}
		if req.WantReply {
			req.Reply(true, nil)
		}
		cmd := decodeExecCommand(req.Payload)
		s.runExec(ch, cmd)
		return
	}
}

func decodeExecCommand(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	n := binary.BigEndian.Uint32(payload[:4])
	if int(n) > len(payload)-4 {
		return ""
	}
	return string(payload[4 : 4+n])
}

func sendExit(ch ssh.Channel, code uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], code)
	ch.SendRequest("exit-status", false, b[:])
	ch.Close()
}

var keyLineRe = regexp.MustCompile(`'([^']*)'`)

// drainStdin сливает стандартный вход команды в фоне и закрывает возвращённый
// канал, когда клиент дописал и закрыл запись (или канал оборвался). Кому
// это важно (chpasswd) — дожидается закрытия канала перед ответом; кому нет
// — просто не ждёт, поведение то же самое, что и раньше.
func drainStdin(ch ssh.Channel) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		io.Copy(io.Discard, ch)
		close(done)
	}()
	return done
}

func (s *fakeAdminServer) runExec(ch ssh.Channel, cmd string) {
	defer ch.Close()

	if cmd == "bash -s" || cmd == "sudo -n bash -s" {
		data, _ := io.ReadAll(ch)
		s.handleScript(ch, string(data))
		return
	}
	// sudo с паролем: первая строка стандартного входа — пароль, дальше скрипт.
	if cmd == "sudo -S -k -p '' bash -s" {
		data, _ := io.ReadAll(ch)
		pass, script, _ := strings.Cut(string(data), "\n")
		if pass != s.password || s.sudoMode != "password" {
			fmt.Fprintln(ch.Stderr(), "Sorry, try again.")
			sendExit(ch, 1)
			return
		}
		s.handleScript(ch, script)
		return
	}
	switch cmd {
	case "sudo -n true":
		drainStdin(ch)
		s.mu.Lock()
		s.calls = append(s.calls, execCall{cmd: cmd})
		s.mu.Unlock()
		if s.sudoMode == "nopass" {
			sendExit(ch, 0)
			return
		}
		fmt.Fprintln(ch.Stderr(), "sudo: a password is required")
		sendExit(ch, 1)
		return
	case "sudo -S -k -p '' true":
		data, _ := io.ReadAll(ch)
		s.mu.Lock()
		s.calls = append(s.calls, execCall{cmd: cmd})
		s.mu.Unlock()
		if s.sudoMode == "none" {
			fmt.Fprintln(ch.Stderr(), "user is not in the sudoers file.  This incident will be reported.")
			sendExit(ch, 1)
			return
		}
		if strings.TrimSuffix(string(data), "\n") != s.password {
			fmt.Fprintln(ch.Stderr(), "Sorry, try again.")
			sendExit(ch, 1)
			return
		}
		sendExit(ch, 0)
		return
	}

	// Большинство команд стандартный вход не читают — не ждём его закрытия,
	// просто сливаем в фоне, если что-то всё же придёт. Исключение —
	// chpasswd ниже: там клиент правда пишет и явно закрывает запись, и это
	// важно дождаться перед ответом (см. комментарий там же).
	stdinDrained := drainStdin(ch)

	s.mu.Lock()
	s.calls = append(s.calls, execCall{cmd: cmd})
	s.mu.Unlock()

	switch {
	case strings.Contains(cmd, "authorized_keys"):
		if m := keyLineRe.FindStringSubmatch(cmd); len(m) > 1 {
			s.mu.Lock()
			s.keyLine = m[1]
			s.keyInstalled = true
			s.mu.Unlock()
		}
		sendExit(ch, 0)
	case strings.Contains(cmd, "journalctl -u ssh_tunnel_panel"):
		fmt.Fprintln(ch, `Первый запуск: создан пользователь "admin" с одноразовым паролем.`)
		fmt.Fprintln(ch, `Логин:  admin`)
		fmt.Fprintln(ch, `Пароль: TESTPASS`)
		sendExit(ch, 0)
	case strings.HasPrefix(cmd, "chpasswd"):
		// В отличие от остальных команд здесь клиент действительно пишет в
		// stdin (пароль) и сам закрывает запись, когда дописал. Раньше эта
		// ветка сразу слала exit-status и закрывала канал (общий фоновый
		// io.Copy выше просто отбрасывал байты параллельно) — оттого
		// изредка обгоняла ещё не дописанные клиентом данные, и
		// session.Run на его стороне возвращал ошибку записи вместо кода
		// выхода. Дожидаемся EOF по стандартному входу явно, прежде чем
		// отвечать, — тот самый фоновый io.Copy выше это и делает, просто
		// нужно на него дождаться, а не гонять его в фоне бесконтрольно.
		<-stdinDrained
		sendExit(ch, 0)
	default:
		fmt.Fprintf(ch.Stderr(), "неизвестная команда: %s\n", cmd)
		sendExit(ch, 1)
	}
}

func (s *fakeAdminServer) handleScript(ch ssh.Channel, script string) {
	s.mu.Lock()
	s.calls = append(s.calls, execCall{cmd: "bash -s", stdin: script})
	hardenExit, tunnelExit := s.hardenExit, s.tunnelExit
	s.mu.Unlock()

	switch {
	case strings.Contains(script, "NEW_USER="):
		fmt.Fprintln(ch, "harden: строка первая")
		fmt.Fprintln(ch, "harden: строка вторая")
		sendExit(ch, hardenExit)
	case strings.Contains(script, "TUNNEL_USER="):
		fmt.Fprintln(ch, "tunnel-user: строка первая")
		sendExit(ch, tunnelExit)
	case strings.Contains(script, "ключ для root записан"):
		fmt.Fprintln(ch, "ключ для root записан")
		sendExit(ch, 0)
	case strings.Contains(script, "ssh_tunnel_panel"):
		fmt.Fprintln(ch, "панель запущена")
		sendExit(ch, 0)
	case strings.Contains(script, "udprelay"):
		fmt.Fprintln(ch, "udprelay собран")
		sendExit(ch, 0)
	default:
		fmt.Fprintln(ch.Stderr(), "неизвестный скрипт")
		sendExit(ch, 1)
	}
}

func (s *fakeAdminServer) callsSnapshot() []execCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]execCall(nil), s.calls...)
}

func (s *fakeAdminServer) hasScriptContaining(sub string) bool {
	for _, c := range s.callsSnapshot() {
		if strings.HasSuffix(c.cmd, "bash -s") && strings.Contains(c.stdin, sub) {
			return true
		}
	}
	return false
}

func (s *fakeAdminServer) hostPort(t *testing.T) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return host, port
}

// ---------- сами тесты ----------

func testKeyPath(t *testing.T) string {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return filepath.Join(t.TempDir(), "id_ed25519")
}

// collectEvents собирает все события шины за время работы fn.
func collectEvents(t *testing.T, bus *events.Bus, fn func()) []events.Event {
	t.Helper()
	ch, unsub := bus.Subscribe()
	defer unsub()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	<-done
	var out []events.Event
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestRunVpsSetupFullFlow(t *testing.T) {
	srv := newFakeAdminServer(t, "root-pass")
	host, port := srv.hostPort(t)
	bus := events.NewBus()

	params := vpsSetupParams{
		Host: host, Port: port, User: "root",
		Password: "root-pass", KeyPath: testKeyPath(t), InstallPanel: true, InstallUDPRelay: true,
	}

	var runErr error
	evs := collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr != nil {
		t.Fatalf("runVpsSetup вернул ошибку: %v", runErr)
	}

	if !srv.hasScriptContaining(fmt.Sprintf("SSH_PORT=%d", port)) {
		t.Error("harden.sh не получил правильный SSH_PORT")
	}
	// NEW_USER пустой: отдельного sudo-пользователя автоматический мастер не
	// заводит, вход и так уже под root по ключу.
	if !srv.hasScriptContaining(`NEW_USER=""`) {
		t.Error("harden.sh должен получить пустой NEW_USER")
	}
	if !srv.hasScriptContaining(`TUNNEL_USER="tunnel"`) {
		t.Error("tunnel-user.sh не запустился")
	}
	if !srv.hasScriptContaining("ssh_tunnel_panel") {
		t.Error("установка панели не запросилась, хотя InstallPanel=true")
	}
	// Ставим свою панель, а не постороннюю системную консоль: та открывала бы
	// наружу вход по паролю к учётке с полным sudo — ровно то, что harden.sh
	// только что закрыл.
	if srv.hasScriptContaining("cockpit") || srv.hasScriptContaining("vpsadmin") {
		t.Error("мастер ставит постороннюю панель вместо ssh_tunnel_panel")
	}
	if !srv.hasScriptContaining("udprelay") {
		t.Error("установка ретранслятора UDP не запросилась, хотя InstallUDPRelay=true")
	}

	var sawDone bool
	var panelLineFound bool
	for _, e := range evs {
		if e.Kind != events.KindVpsSetup {
			continue
		}
		if strings.Contains(e.Text, params.Password) || strings.Contains(e.Error, params.Password) {
			t.Fatalf("пароль root попал в событие: %+v", e)
		}
		if e.Stage == "panel" && strings.Contains(e.Text, "TESTPASS") {
			panelLineFound = true
		}
		if e.Done {
			sawDone = true
			if e.Failed {
				t.Fatalf("итоговое событие сообщает о неудаче: %s", e.Error)
			}
		}
	}
	if !sawDone {
		t.Fatal("не пришло итоговое событие VpsSetupDone")
	}
	if !panelLineFound {
		t.Error("одноразовый пароль панели не пришёл в поток событий")
	}
}

func TestRunVpsSetupWrongPassword(t *testing.T) {
	srv := newFakeAdminServer(t, "correct-pass")
	host, port := srv.hostPort(t)
	bus := events.NewBus()

	params := vpsSetupParams{
		Host: host, Port: port, User: "root",
		Password: "wrong-pass", KeyPath: testKeyPath(t),
	}

	var runErr error
	evs := collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr == nil {
		t.Fatal("ожидалась ошибка при неверном пароле")
	}
	if len(srv.callsSnapshot()) != 0 {
		t.Error("сервер получил команды, хотя пароль не подошёл")
	}
	for _, e := range evs {
		if strings.Contains(e.Text, params.Password) || strings.Contains(e.Error, params.Password) {
			t.Fatalf("пароль root попал в событие: %+v", e)
		}
	}
}

// Ключ положен, но вход по нему почему-то не работает (сервер отклоняет) —
// harden.sh не должен запуститься вовсе: иначе пароли отключились бы, а
// подтверждённого рабочего входа так и не появилось бы.
func TestRunVpsSetupKeyLoginFailsStopsBeforeHarden(t *testing.T) {
	srv := newFakeAdminServer(t, "root-pass")
	srv.acceptKey = false
	host, port := srv.hostPort(t)
	bus := events.NewBus()

	params := vpsSetupParams{
		Host: host, Port: port, User: "root",
		Password: "root-pass", KeyPath: testKeyPath(t),
	}

	var runErr error
	collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr == nil {
		t.Fatal("ожидалась ошибка проверки входа по ключу")
	}
	if srv.hasScriptContaining("NEW_USER=") {
		t.Fatal("harden.sh запустился, хотя вход по ключу не подтверждён")
	}
}

// harden.sh закончился с ошибкой — tunnel-user.sh запускать нельзя.
func TestRunVpsSetupHardenFailureStopsBeforeTunnelUser(t *testing.T) {
	srv := newFakeAdminServer(t, "root-pass")
	srv.hardenExit = 1
	host, port := srv.hostPort(t)
	bus := events.NewBus()

	params := vpsSetupParams{
		Host: host, Port: port, User: "root",
		Password: "root-pass", KeyPath: testKeyPath(t),
	}

	var runErr error
	collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr == nil {
		t.Fatal("ожидалась ошибка harden.sh")
	}
	if srv.hasScriptContaining("TUNNEL_USER=") {
		t.Fatal("tunnel-user.sh запустился после неудачного harden.sh")
	}
}

func TestLineWriterSplitsAcrossWrites(t *testing.T) {
	var lines []string
	lw := &lineWriter{emit: func(s string) { lines = append(lines, s) }}
	lw.Write([]byte("perva"))
	lw.Write([]byte("ya line\nvtoraya\nchast' tret'"))
	lw.Write([]byte("yey stroki\n"))
	lw.Write([]byte("bez perevoda stroki v kontce"))
	lw.Flush()

	want := []string{"pervaya line", "vtoraya", "chast' tret'yey stroki", "bez perevoda stroki v kontce"}
	if len(lines) != len(want) {
		t.Fatalf("получилось %v, ожидалось %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("получилось %v, ожидалось %v", lines, want)
		}
	}
}

// Обычный пользователь с sudo по паролю (ТЗ 45): всё под root идёт через
// sudo, тот же ключ кладётся и root'у (иначе harden.sh не отключит пароли),
// панель открыта в интернет — только потому, что так выбрали, — а итог
// приходит в событии с одноразовым паролем панели и добавленным сервером.
func TestRunVpsSetupSudoUser(t *testing.T) {
	srv := newFakeAdminServer(t, "user-pass")
	srv.sudoMode = "password"
	host, port := srv.hostPort(t)
	bus := events.NewBus()

	var added *events.VpsResult
	params := vpsSetupParams{
		Host: host, Port: port, User: "admin2", Password: "user-pass", KeyPath: testKeyPath(t),
		InstallPanel: true, PanelPublicPort: 8443,
		AddProfile: func(res *events.VpsResult) {
			added = res
			res.ProfileID, res.ProfileName = "p9", host
		},
	}
	var runErr error
	evs := collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr != nil {
		t.Fatalf("runVpsSetup: %v", runErr)
	}
	if !srv.hasScriptContaining("ключ для root записан") {
		t.Error("ключ не положен root'у — harden.sh откажется отключать пароли")
	}
	if !srv.hasScriptContaining("NEW_USER=") || !srv.hasScriptContaining(`TUNNEL_USER="tunnel"`) {
		t.Error("harden.sh или tunnel-user.sh не запустились через sudo")
	}
	if !srv.hasScriptContaining("-listen 0.0.0.0:8443") || !srv.hasScriptContaining("ufw allow 8443/tcp") {
		t.Error("панель не открыта в интернет, хотя выбран порт 8443")
	}
	for _, c := range srv.callsSnapshot() {
		if strings.Contains(c.cmd, "user-pass") {
			t.Fatalf("пароль попал в командную строку: %q", c.cmd)
		}
	}
	if added == nil {
		t.Fatal("сервер не добавлен в список")
	}
	var done *events.VpsResult
	for _, e := range evs {
		if strings.Contains(e.Text, "user-pass") || strings.Contains(e.Error, "user-pass") {
			t.Fatalf("пароль попал в событие: %+v", e)
		}
		if e.Kind == events.KindVpsSetup && e.Done {
			done = e.Vps
		}
	}
	if done == nil {
		t.Fatal("нет итога в событии Done")
	}
	if done.PanelPass != "TESTPASS" || done.PanelLogin != "admin" || !done.Panel {
		t.Errorf("итог панели: %+v", done)
	}
	if done.AdminUser != "admin2" || done.ProfileID != "p9" || done.PanelPublic != 8443 {
		t.Errorf("итог: %+v", done)
	}
}

func TestRunVpsSetupSudoNopass(t *testing.T) {
	srv := newFakeAdminServer(t, "user-pass")
	srv.sudoMode = "nopass"
	host, port := srv.hostPort(t)
	bus := events.NewBus()
	params := vpsSetupParams{Host: host, Port: port, User: "ops", Password: "user-pass", KeyPath: testKeyPath(t)}
	var runErr error
	collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr != nil {
		t.Fatalf("runVpsSetup: %v", runErr)
	}
	if !srv.hasScriptContaining(`TUNNEL_USER="tunnel"`) {
		t.Error("tunnel-user.sh не запустился")
	}
}

// Пользователь без sudo — понятная ошибка, и на сервере ничего не тронуто:
// ни ключа, ни harden.sh.
func TestRunVpsSetupUserWithoutSudo(t *testing.T) {
	srv := newFakeAdminServer(t, "user-pass")
	srv.sudoMode = "none"
	host, port := srv.hostPort(t)
	bus := events.NewBus()
	params := vpsSetupParams{Host: host, Port: port, User: "guest", Password: "user-pass", KeyPath: testKeyPath(t)}
	var runErr error
	collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr == nil || !strings.Contains(runErr.Error(), "нет права sudo") {
		t.Fatalf("ожидалась ошибка «нет права sudo», получено: %v", runErr)
	}
	for _, c := range srv.callsSnapshot() {
		if strings.Contains(c.cmd, "authorized_keys") || strings.HasSuffix(c.cmd, "bash -s") {
			t.Fatalf("на сервере что-то менялось до проверки sudo: %q", c.cmd)
		}
	}
}

// Панель по умолчанию — только локально: ни -listen наружу, ни порта в firewall.
func TestRunVpsSetupPanelLocalByDefault(t *testing.T) {
	srv := newFakeAdminServer(t, "root-pass")
	host, port := srv.hostPort(t)
	bus := events.NewBus()
	params := vpsSetupParams{Host: host, Port: port, User: "root", Password: "root-pass",
		KeyPath: testKeyPath(t), InstallPanel: true}
	var runErr error
	collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr != nil {
		t.Fatalf("runVpsSetup: %v", runErr)
	}
	if !srv.hasScriptContaining("ssh_tunnel_panel") {
		t.Fatal("панель не ставилась")
	}
	// «ufw allow» есть и в harden.sh (порт SSH) — ищем именно порт панели.
	if srv.hasScriptContaining("-listen 0.0.0.0") || srv.hasScriptContaining("comment 'ssh_tunnel_panel'") {
		t.Error("панель открыта наружу, хотя выбрано «только локально»")
	}
	if srv.hasScriptContaining("ключ для root записан") {
		t.Error("под root ключ кладётся второй раз отдельным шагом")
	}
}

func TestParsePanelPassword(t *testing.T) {
	journal := "2026/11/25 10:00:00 Первый запуск: создан пользователь \"admin\" с одноразовым паролем.\n" +
		"2026/11/25 10:00:00 Логин:  admin\n2026/11/25 10:00:00 Пароль: aB3-xY9\n" +
		"2026/11/25 10:00:00 Панель потребует сменить его сразу после входа. Пароль больше нигде\n"
	if l, p, n := parsePanelPassword(journal); l != "admin" || p != "aB3-xY9" || n != "" {
		t.Errorf("новый формат: %q %q %q", l, p, n)
	}
	if _, p, _ := parsePanelPassword(`создан пользователь "admin" с одноразовым паролем: OLD1`); p != "OLD1" {
		t.Errorf("старый формат: %q", p)
	}
	if _, p, n := parsePanelPassword(""); p != "" || n == "" {
		t.Errorf("пустой журнал: пароль %q, пояснение %q", p, n)
	}
}

func TestPanelInstallScriptParse(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("нет bash")
	}
	for _, pub := range []int{0, 8443} {
		script := panelInstallScript("203.0.113.10", 22, pub)
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("порт %d: bash -n: %v\n%s", pub, err, out)
		}
		if (pub == 0) == strings.Contains(script, "0.0.0.0") {
			t.Errorf("порт %d: адрес панели не тот:\n%s", pub, script)
		}
	}
}

// Повторный запуск после обрыва: ключ этого компьютера уже на сервере, а вход
// по паролю harden.sh успел выключить — мастер входит по ключу и доделывает.
func TestRunVpsSetupRerunByKeyAfterPasswordsOff(t *testing.T) {
	srv := newFakeAdminServer(t, "root-pass")
	host, port := srv.hostPort(t)
	key := testKeyPath(t)
	params := vpsSetupParams{Host: host, Port: port, User: "root", Password: "root-pass", KeyPath: key}

	bus := events.NewBus()
	var runErr error
	collectEvents(t, bus, func() { runErr = runVpsSetup(bus, params) })
	if runErr != nil {
		t.Fatalf("первый запуск: %v", runErr)
	}

	srv.mu.Lock()
	srv.password = "пароли-выключены"
	srv.mu.Unlock()

	bus2 := events.NewBus()
	collectEvents(t, bus2, func() { runErr = runVpsSetup(bus2, params) })
	if runErr != nil {
		t.Fatalf("повторный запуск по ключу: %v", runErr)
	}
}
