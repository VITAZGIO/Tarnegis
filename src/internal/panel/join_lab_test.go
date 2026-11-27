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
//   - проброс портов и терминал tun_join не даются;
//   - (ТЗ 49) два устройства по двум приглашениям получают в профиле один
//     и тот же ключ сети панели — одна сеть (один id сети), ключа нет ни в
//     приглашении, ни в ответе join.
//
//	sudo env JOIN_LAB=1 ./panel.test -test.run TestJoinLab -test.v
//
// Учётные записи устройств заводит поддельный Provisioner: useradd устройств
// проверен давно, здесь проверяется только вход tun_join.

import (
	"bytes"
	"context"
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

	"sshtunnel/internal/config"
	"sshtunnel/internal/hostkey"
	"sshtunnel/internal/invite"
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
	// Сеть устройств на сервере — с поддельным meshd (ТЗ 49: сеть панели).
	sets, err := OpenSettings(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	sets.Update(func(st *Settings) error { st.Mesh.Role, st.Mesh.Name = MeshRoleMain, "Стенд"; return nil })
	msys := newFakeMeshSystem()
	msys.units[meshdUnit] = UnitState{Installed: true, Active: true, Enabled: true}
	s.WithSettings(sets).WithMesh(NewMeshManager(sets, msys, newMemAdmin(randomHex(8)), dir, "9.9.9").WithSSH("203.0.113.5", 22))
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

	newSigner := func() ssh.Signer {
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		sg, _ := ssh.NewSignerFromKey(priv)
		return sg
	}
	signer := newSigner()
	myKey := pubLine(signer.PublicKey())
	dialAs := func(sg ssh.Signer) *ssh.Client {
		c, err := ssh.Dial("tcp", "127.0.0.1:22", &ssh.ClientConfig{User: JoinUser,
			Auth: []ssh.AuthMethod{ssh.PublicKeys(sg)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout: 10 * time.Second})
		if err != nil {
			t.Fatalf("sshd не пустил tun_join с новым ключом: %v", err)
		}
		return c
	}
	dial := func() *ssh.Client { return dialAs(signer) }
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

	// ТЗ 49: сеть панели. Первое устройство — ключ сети в профиле, но не в
	// приглашении и не в ответе join.
	pn, _ := s.mesh.net.PanelNet(false)
	if !pn.valid() {
		t.Fatal("приглашение с сетью устройств не завело сеть панели")
	}
	if strings.Contains(res.Text, pn.Key) || strings.Contains(out, pn.Key) {
		t.Fatal("ключ сети в приглашении или в ответе join")
	}
	p1 := s.profileOf(got)
	if p1.MeshKey != pn.Key {
		t.Fatalf("профиль первого устройства без ключа сети панели: %+v", p1)
	}
	// Второе устройство по другому приглашению, своим ключом — та же сеть.
	res2, err := s.createInvite(inviteReq{Name: "Стенд 2", DeviceType: "android"})
	if err != nil {
		t.Fatal(err)
	}
	sg2 := newSigner()
	c2 := dialAs(sg2)
	defer c2.Close()
	req2, _ := json.Marshal(map[string]string{"tok": tokOf(t, res2.Text), "name": "Стенд 2", "type": "phone"})
	out2 := run(c2, "join", string(req2)+"\n")
	var r2 joinResp
	if json.Unmarshal([]byte(strings.TrimSpace(out2)), &r2) != nil || !r2.OK || r2.ID == r.ID {
		t.Fatalf("второй join: %q", out2)
	}
	got2, _ := s.clients.Get(r2.ID)
	p2 := s.profileOf(got2)
	if p2.MeshKey != p1.MeshKey || meshNetID(p2.MeshKey) != meshNetID(p1.MeshKey) {
		t.Fatalf("второе устройство в другой сети: %q / %q", p2.MeshKey, p1.MeshKey)
	}
	if st := s.mesh.State(context.Background()); st.PanelNet != meshNetID(pn.Key) {
		t.Fatalf("panelNet %q, ждал %q", st.PanelNet, meshNetID(pn.Key))
	}
	// Файл ключа — только root.
	if fi, err := os.Stat(filepath.Join(dir, "panel-net.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("panel-net.json: %v %v", fi, err)
	}
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func readAllLimit(r *http.Request, n int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, n))
}

// TestJoinLabReinstall — ТЗ 50 на настоящем sshd (у него, как на настоящем
// сервере, ключи хоста ECDSA, ed25519 и RSA) и с настоящими учётными
// записями (useradd). «Переустановка»: в known_hosts под адресом сервера
// лежат ключи прежней установки обоих типов, приглашение несёт новый
// ed25519. Вход и чтение профиля должны пройти, профиль — со всеми
// серверами (здесь сервер один), следующая сверка по known_hosts — тоже.
func TestJoinLabReinstall(t *testing.T) {
	if os.Getenv("JOIN_LAB") == "" {
		t.Skip("только на стенде (JOIN_LAB=1, root, sshd)")
	}
	if err := EnsureJoinAccess(22); err != nil {
		t.Fatalf("EnsureJoinAccess: %v", err)
	}
	dir := t.TempDir()
	users, _ := OpenStore(filepath.Join(dir, "users.json"))
	cs, _ := OpenClientStore(filepath.Join(dir, "clients.json"))
	s := NewServer(users, NewClientManager(cs, NewSystemProvisioner())).WithDataDir(dir)
	s.WithClientDefaults("127.0.0.1", 22, "") // ключи хоста — настоящие, /etc/ssh
	ln, err := net.Listen("tcp", SelfServiceAddr)
	if err != nil {
		t.Fatal(err)
	}
	inner := s.SelfServiceHandler()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t0 := time.Now()
		rec := &statusRec{ResponseWriter: w, code: 200}
		inner.ServeHTTP(rec, r)
		t.Logf("самообслуживание: %s %s → %d за %v", r.Method, r.URL.Path, rec.code, time.Since(t0))
	})}
	go srv.Serve(ln)
	defer srv.Close()

	res, err := s.createInvite(inviteReq{Name: "Переустановка", DeviceType: "android"})
	if err != nil {
		t.Fatal(err)
	}
	cdir := t.TempDir()
	kh := filepath.Join(cdir, "known_hosts")
	_, oldEd, _ := ed25519.GenerateKey(rand.Reader)
	oldSigner, _ := ssh.NewSignerFromKey(oldEd)
	oldLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(oldSigner.PublicKey())))
	os.WriteFile(kh, []byte("127.0.0.1 "+oldLine+"\n"), 0o600)

	r, err := invite.Redeem(context.Background(), res.Text, invite.Options{KeyDir: cdir, KnownHosts: kh, Name: "Переустановка", Type: "phone"})
	if err != nil {
		t.Fatalf("вход: %v", err)
	}
	defer s.clients.DeleteClient(r.Device.ID)
	if r.ProfileErr != nil || len(r.Profile.Servers) != 1 {
		t.Fatalf("профиль после входа: %v %+v", r.ProfileErr, r.Profile)
	}
	if strings.Contains(mustRead(t, kh), oldLine) {
		t.Fatal("старый ключ остался в known_hosts")
	}
	signer, err := invite.LoadKey(r.Device.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := invite.Fetch(context.Background(), invite.Target{Host: "127.0.0.1", Port: 22, User: r.Device.ID, Signer: signer,
		HostKey: hostkey.Callback(kh, nil), KnownHosts: kh})
	if err != nil || p.ID != r.Device.ID {
		t.Fatalf("сверка по known_hosts: %v", err)
	}

	// ТЗ 51: телефон v1.10.23 (знал только сервер приглашения, rev 0)
	// после сверки получает все серверы профиля; удалённый у себя сервер
	// следующая сверка с настоящего sshd не возвращает.
	cfg := config.Default()
	cfg.Profiles = nil
	cfg.SetDevice(config.Device{ID: r.Device.ID, KeyPath: r.Device.KeyPath}, config.DeviceProfile{ID: r.Device.ID, Mesh: true,
		Servers: []config.DeviceServer{{Name: "127.0.0.1", Host: "127.0.0.1", Port: 22}}})
	if res := cfg.ApplyDevice(p.Config()); !res.Changed || len(cfg.Profiles) != len(p.Servers) {
		t.Fatalf("после сверки: %+v / профиль %+v", cfg.Profiles, p.Servers)
	}
	cfg.RemoveProfile(cfg.Profiles[0].ID)
	p2, err := invite.Fetch(context.Background(), invite.Target{Host: "127.0.0.1", Port: 22, User: r.Device.ID, Signer: signer,
		HostKey: hostkey.Callback(kh, nil), KnownHosts: kh})
	if err != nil {
		t.Fatalf("вторая сверка: %v", err)
	}
	dp := p2.Config()
	dp.Rev = cfg.Device.Rev + 1 // панель что-то поменяла
	cfg.ApplyDevice(dp)
	if len(cfg.Profiles) != len(p.Servers)-1 {
		t.Fatalf("удалённый вернулся после сверки: %+v", cfg.Profiles)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type statusRec struct {
	http.ResponseWriter
	code int
}

func (r *statusRec) WriteHeader(c int) { r.code = c; r.ResponseWriter.WriteHeader(c) }

// TestJoinLabShortCode — ТЗ 54 на настоящем sshd: короткий код (20
// символов) входит целиком — PAKE через команду join в два раунда, ключ
// хоста из /etc/ssh подтверждён и записан, профиль читается; второй раз
// тот же код не входит, код с опечаткой не уходит на сервер.
func TestJoinLabShortCode(t *testing.T) {
	if os.Getenv("JOIN_LAB") == "" {
		t.Skip("только на стенде (JOIN_LAB=1, root, sshd)")
	}
	if err := EnsureJoinAccess(22); err != nil {
		t.Fatalf("EnsureJoinAccess: %v", err)
	}
	dir := t.TempDir()
	users, _ := OpenStore(filepath.Join(dir, "users.json"))
	cs, _ := OpenClientStore(filepath.Join(dir, "clients.json"))
	s := NewServer(users, NewClientManager(cs, NewSystemProvisioner())).WithDataDir(dir)
	s.WithClientDefaults("127.0.0.1", 22, "")
	ln, err := net.Listen("tcp", SelfServiceAddr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.SelfServiceHandler()}
	go srv.Serve(ln)
	defer srv.Close()

	res, err := s.createInvite(inviteReq{Name: "Код", DeviceType: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := invite.NormalizeCode(res.Code); len(n) != 20 {
		t.Fatalf("код %q", res.Code)
	}
	cdir := t.TempDir()
	kh := filepath.Join(cdir, "known_hosts")
	r, err := invite.Redeem(context.Background(), "tarnegis:"+strings.ToLower(strings.ReplaceAll(res.Code, "-", "")),
		invite.Options{KeyDir: cdir, KnownHosts: kh, Name: "Код", Type: "pc"})
	if err != nil {
		t.Fatalf("вход по коду: %v", err)
	}
	defer s.clients.DeleteClient(r.Device.ID)
	if r.ProfileErr != nil || len(r.Profile.Servers) != 1 {
		t.Fatalf("профиль после входа: %v %+v", r.ProfileErr, r.Profile)
	}
	// В known_hosts — один из настоящих ключей хоста.
	known := hostkey.Known(kh, "127.0.0.1", 22)
	ok := false
	for _, line := range (systemMesh{}).HostKeys() {
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		for _, kn := range known {
			if err == nil && kn.Fingerprint == ssh.FingerprintSHA256(k) {
				ok = true
			}
		}
	}
	if !ok {
		t.Fatalf("known_hosts: %+v", known)
	}
	// Дальше — строго по known_hosts, как раньше.
	signer, _ := invite.LoadKey(r.Device.KeyPath)
	if p, err := invite.Fetch(context.Background(), invite.Target{Host: "127.0.0.1", Port: 22, User: r.Device.ID, Signer: signer,
		HostKey: hostkey.Callback(kh, nil), KnownHosts: kh}); err != nil || p.ID != r.Device.ID {
		t.Fatalf("сверка по known_hosts: %v", err)
	}
	// Второй раз — нет.
	if _, err := invite.Redeem(context.Background(), res.Code, invite.Options{KeyDir: cdir, KnownHosts: kh, Type: "pc"}); err == nil {
		t.Fatal("код вошёл второй раз")
	}
	// Неверный секрет (то же приглашение по метке нельзя подобрать —
	// берём новое и портим секрет с сохранением метки): PAKE не сходится.
	res2, _ := s.createInvite(inviteReq{Name: "Код 2", DeviceType: "linux"})
	c2, _ := invite.ParseCode(res2.Code)
	wrong := c2.Secret
	tag := invite.CodeTag(wrong)
	for i := 0; ; i++ {
		wrong[0], wrong[1] = byte(i), byte(i>>8)
		if wrong != c2.Secret && invite.CodeTag(wrong) == tag {
			break
		}
	}
	bad, _ := invite.EncodeCode("127.0.0.1", 22, wrong)
	if _, err := invite.Redeem(context.Background(), bad, invite.Options{KeyDir: cdir, KnownHosts: filepath.Join(cdir, "kh2"), Type: "pc"}); err == nil ||
		!strings.Contains(err.Error(), "не подтвердил") {
		t.Fatalf("неверный секрет: %v", err)
	}
}
