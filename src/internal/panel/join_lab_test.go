//go:build joinlab

package panel

// Стенд входа по приглашению на настоящем sshd (ТЗ 47): workflow join-lab,
// под root на одноразовой машине CI. Проверяет то, чего не видно в тестах
// в памяти:
//   - EnsureJoinAccess заводит tun_join и блок sshd, sshd -t его принимает;
//   - sshd пускает tun_join с любым новым ключом (AuthorizedKeysCommand);
//   - что бы ни попросили, выполняется только join (ForceCommand), и ключ,
//     которым вошли, доходит до панели из $SSH_USER_AUTH (ExposeAuthInfo), а
//     не со слов программы;
//   - /v1/join узнаёт tun_join по uid сокета (/proc/net/tcp);
//   - проброс портов и терминал tun_join не даются.
//
//	sudo env JOIN_LAB=1 ./panel.test -test.run TestJoinLab -test.v
//
// Учётные записи устройств заводит поддельный Provisioner: useradd устройств
// проверен давно, здесь проверяется только вход tun_join.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestJoinLab(t *testing.T) {
	if os.Getenv("JOIN_LAB") == "" {
		t.Skip("только на стенде (JOIN_LAB=1, root, sshd)")
	}
	if err := EnsureJoinAccess(22); err != nil {
		t.Fatalf("EnsureJoinAccess: %v", err)
	}
	cfg, _ := os.ReadFile(SSHDPath)
	if !strings.Contains(string(cfg), "Match User "+JoinUser) {
		t.Fatalf("нет блока в sshd_config:\n%s", cfg)
	}
	// Второй запуск ничего не меняет.
	if err := EnsureJoinAccess(22); err != nil {
		t.Fatalf("второй EnsureJoinAccess: %v", err)
	}

	// Настоящий вход самообслуживания панели на 127.0.0.1:47832.
	dir := t.TempDir()
	users, _ := OpenStore(filepath.Join(dir, "users.json"))
	cs, _ := OpenClientStore(filepath.Join(dir, "clients.json"))
	s := NewServer(users, NewClientManager(cs, newFakeProvisioner())).WithDataDir(dir)
	s.WithClientDefaults("203.0.113.5", 22, "")
	s.hostKeysFn = func() []string { return []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILab"} }
	var seen joinReq
	inner := s.SelfServiceHandler()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/join" {
			body, _ := readAllLimit(r, 16<<10)
			json.Unmarshal(body, &seen)
			r.Body = nopCloser{bytes.NewReader(body)}
		}
		inner.ServeHTTP(w, r)
	})
	ln, err := net.Listen("tcp", SelfServiceAddr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	defer srv.Close()

	res, err := s.createInvite(inviteReq{Name: "Стенд", DeviceType: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	tok := tokOf(t, res.Text)

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	myKey := pubLine(signer.PublicKey())
	dial := func() *ssh.Client {
		c, err := ssh.Dial("tcp", "127.0.0.1:22", &ssh.ClientConfig{User: JoinUser,
			Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout: 10 * time.Second})
		if err != nil {
			t.Fatalf("sshd не пустил tun_join с новым ключом: %v", err)
		}
		return c
	}
	run := func(c *ssh.Client, cmd, in string) string {
		sess, err := c.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()
		sess.Stdin = strings.NewReader(in)
		var out, errb bytes.Buffer
		sess.Stdout, sess.Stderr = &out, &errb
		sess.Run(cmd)
		t.Logf("%q → %q (stderr %q)", cmd, out.String(), errb.String())
		return out.String()
	}

	c := dial()
	defer c.Close()

	// Не join — отказ, оболочки нет.
	if out := run(c, "id; cat /etc/shadow", ""); strings.Contains(out, "uid=") || !strings.Contains(out, `"ok":false`) {
		t.Fatalf("tun_join выполнил чужую команду: %q", out)
	}
	c.Close()

	// join: ключ — тот, которым вошли, а не из запроса.
	c = dial()
	defer c.Close()
	req, _ := json.Marshal(map[string]string{"tok": tok, "name": "Стенд", "type": "linux", "key": "ssh-ed25519 ПОДДЕЛКА"})
	out := run(c, "join", string(req)+"\n")
	var r joinResp
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &r) != nil || !r.OK || !ValidUsername(r.ID) {
		t.Fatalf("join: %q", out)
	}
	if seen.Key != myKey || seen.From != "127.0.0.1" {
		t.Fatalf("панель получила ключ %q с %q, ждал %q", seen.Key, seen.From, myKey)
	}
	got, _ := s.clients.Get(r.ID)
	if got.PublicKey != myKey || got.PrivateKey != "" {
		t.Fatalf("учётная запись: %+v", got)
	}
	// Тот же секрет второй раз — used.
	c.Close()
	c = dial()
	defer c.Close()
	if out := run(c, "join", string(req)+"\n"); !strings.Contains(out, `"err":"used"`) {
		t.Fatalf("второй раз: %q", out)
	}

	// Проброс портов и терминал — нет.
	if conn, err := c.Dial("tcp", SelfServiceAddr); err == nil {
		conn.Close()
		t.Fatal("tun_join получил проброс порта")
	}
	sess, _ := c.NewSession()
	if err := sess.RequestPty("xterm", 40, 80, ssh.TerminalModes{}); err == nil {
		t.Fatal("tun_join получил терминал")
	}
	sess.Close()

	// Чужой uid (этот процесс — root) к /v1/join не допущен.
	resp, err := http.Post("http://"+SelfServiceAddr+"/v1/join", "application/json", strings.NewReader(`{"tok":"`+tok+`","key":"`+myKey+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("не tun_join, а /v1/join ответил %d", resp.StatusCode)
	}
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func readAllLimit(r *http.Request, n int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, n))
}
