package panel

// ТЗ 47: устройства выдаются из панели — приглашение, вход tun_join,
// серверы и сеть устройств у учётной записи, профиль.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const joinTestUID = 4242

func newJoinKey(t *testing.T) string {
	t.Helper()
	pub, _, err := generateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func selfGet(t *testing.T, h http.Handler, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// joinAs — вход по приглашению так, как его пересылает команда join.
func joinAs(t *testing.T, s *Server, tok, key, from string) joinResp {
	t.Helper()
	prev := s.connUID
	s.connUID = func(*http.Request) (int, bool) { return joinTestUID, true }
	defer func() { s.connUID = prev }()
	s.joinUID = func() (int, bool) { return joinTestUID, true }
	data, _ := json.Marshal(joinReq{Tok: tok, Name: "Телефон", Type: "phone", Key: key, From: from})
	req := httptest.NewRequest(http.MethodPost, "/v1/join", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.SelfServiceHandler().ServeHTTP(rec, req)
	var r joinResp
	json.Unmarshal(rec.Body.Bytes(), &r)
	return r
}

func tokOf(t *testing.T, text string) string {
	t.Helper()
	d, err := parseDevInvite(text)
	if err != nil {
		t.Fatal(err)
	}
	return d.Tok
}

func TestПриглашениеСтрокаТудаИОбратно(t *testing.T) {
	d := inviteDoc{V: 1, Host: "203.0.113.5", Port: 2222, HK: "ssh-ed25519 AAAA", Tok: "tok-tok-tok-tok-tok", Exp: 1764100000, Name: "Телефон"}
	text := encodeDevInvite(d)
	if !strings.HasPrefix(text, "sshtun1:") || strings.ContainsAny(text, "=+/ \n") {
		t.Fatalf("не base64url без дополнения: %q", text)
	}
	got, err := parseDevInvite("  " + text + "\n")
	if err != nil || got != d {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []string{"", "sshtun2:abc", "sshtun1:!!!", encodeDevInvite(inviteDoc{V: 2, Host: "h", Tok: "t"})} {
		if _, err := parseDevInvite(bad); err == nil {
			t.Errorf("принято %q", bad)
		}
	}
}

func TestСекретПриглашенияОдинРазСуткиДвадцать(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	st := openInviteStore(filepath.Join(t.TempDir(), "inv.json"))
	st.now = func() time.Time { return now }
	tok, d, err := st.add(devInvite{Name: "a", Type: DeviceWindows})
	if err != nil {
		t.Fatal(err)
	}
	if d.Expires-d.Created != int64(24*time.Hour/time.Second) || len(tok) != 43 || d.Hash == tok {
		t.Fatalf("срок %d, длина %d", d.Expires-d.Created, len(tok))
	}
	// На диске — только хеш.
	raw, _ := os.ReadFile(st.path)
	if strings.Contains(string(raw), tok) {
		t.Fatal("секрет лежит на диске открытым")
	}
	if _, code := st.take("нет-такого"); code != joinUnknown {
		t.Fatalf("чужой: %q", code)
	}
	if _, code := st.take(tok); code != "" {
		t.Fatalf("первый раз: %q", code)
	}
	if _, code := st.take(tok); code != joinUsed {
		t.Fatalf("второй раз: %q", code)
	}
	// Истекает через сутки.
	tok2, _, _ := st.add(devInvite{Name: "b"})
	now = now.Add(24*time.Hour - time.Second)
	tok3, _, _ := st.add(devInvite{Name: "c"})
	now = now.Add(2 * time.Second)
	if _, code := st.take(tok2); code != joinUnknown && code != joinExpired {
		t.Fatalf("истёкший: %q", code)
	}
	if _, code := st.take(tok3); code != "" {
		t.Fatalf("живой: %q", code)
	}
	// Не больше 20 открытых; отозванный место освобождает.
	for i := 0; i < maxDevInvites; i++ {
		if _, _, err := st.add(devInvite{Name: "x"}); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if _, _, err := st.add(devInvite{Name: "лишний"}); err == nil {
		t.Fatal("21-е приглашение выдано")
	}
	open := st.open()
	if len(open) != maxDevInvites || !st.revoke(open[0].ID) {
		t.Fatalf("открытых %d", len(open))
	}
	if _, _, err := st.add(devInvite{Name: "после отзыва"}); err != nil {
		t.Fatal(err)
	}
	// Перечитанный с диска — тот же.
	again := openInviteStore(st.path)
	again.now = st.now
	if len(again.open()) != maxDevInvites {
		t.Fatal("не пережило перезапуск")
	}
}

func TestВходПоПриглашению(t *testing.T) {
	s, _ := newTestServer(t)
	s.WithClientDefaults("vpn.example", 2222, "")
	s.hostKeysFn = func() []string { return []string{"ecdsa-sha2-nistp256 AAAAE", "ssh-ed25519 AAAAC3"} }
	res, err := s.createInvite(inviteReq{Name: "Телефон мамы", DeviceType: "android"})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := parseDevInvite(res.Text)
	if doc.Host != "vpn.example" || doc.Port != 2222 || doc.HK != "ssh-ed25519 AAAAC3" || doc.Name != "Телефон мамы" ||
		doc.Exp != res.Invite.Expires || res.QR == "" {
		t.Fatalf("приглашение: %+v", doc)
	}
	key := newJoinKey(t)

	// Не от tun_join — отказ без разговоров.
	s.joinUID = func() (int, bool) { return joinTestUID, true }
	s.connUID = func(*http.Request) (int, bool) { return 1000, true }
	if code, _ := selfPost(t, s.SelfServiceHandler(), "/v1/join", joinReq{Tok: doc.Tok, Key: key}); code != http.StatusForbidden {
		t.Fatalf("чужой uid: %d", code)
	}
	if r := joinAs(t, s, "неверный-секрет-неверный", key, "198.51.100.7"); r.OK || r.Err != joinUnknown {
		t.Fatalf("неверный: %+v", r)
	}
	r := joinAs(t, s, doc.Tok, key, "198.51.100.7")
	if !r.OK || !ValidUsername(r.ID) || r.Name != "Телефон" {
		t.Fatalf("вход: %+v", r)
	}
	c, ok := s.clients.Get(r.ID)
	if !ok || c.PrivateKey != "" || c.PublicKey != strings.TrimSpace(key) || c.DeviceType != DeviceAndroid {
		t.Fatalf("учётная запись: %+v", c)
	}
	if r := joinAs(t, s, doc.Tok, newJoinKey(t), "198.51.100.7"); r.Err != joinUsed {
		t.Fatalf("второй раз: %+v", r)
	}
	if len(s.inviteStore().open()) != 0 {
		t.Fatal("использованное приглашение в списке открытых")
	}

	// Новое приглашение тому же устройству: ключ добавляется, старый остаётся.
	res2, err := s.createInvite(inviteReq{User: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	key2 := newJoinKey(t)
	if r := joinAs(t, s, tokOf(t, res2.Text), key2, "198.51.100.8"); !r.OK || r.ID != c.ID {
		t.Fatalf("новое приглашение: %+v", r)
	}
	c, _ = s.clients.Get(c.ID)
	if len(c.keys()) != 2 {
		t.Fatalf("ключи: %v", c.keys())
	}
	if err := s.clients.RemoveKey(c.ID, key); err != nil {
		t.Fatal(err)
	}
	if err := s.clients.RemoveKey(c.ID, key2); err == nil {
		t.Fatal("убран последний ключ")
	}

	// Подбор: после череды неверных — limit, даже с верным секретом.
	res3, _ := s.createInvite(inviteReq{Name: "ПК", DeviceType: "windows"})
	joinAs(t, s, "мимо-мимо-мимо-мимо-мимо", newJoinKey(t), "203.0.113.66")
	for i := 1; i < lockAfter; i++ { // остальные неудачи — без задержек limiter'а
		s.limiter.Fail("join|203.0.113.66")
	}
	if r := joinAs(t, s, tokOf(t, res3.Text), newJoinKey(t), "203.0.113.66"); r.Err != joinLimit {
		t.Fatalf("подбор: %+v", r)
	}
	// С другого адреса — можно.
	if r := joinAs(t, s, tokOf(t, res3.Text), newJoinKey(t), "203.0.113.67"); !r.OK {
		t.Fatalf("другой адрес: %+v", r)
	}
}

func TestКомандаJoinБерётКлючУsshd(t *testing.T) {
	dir := t.TempDir()
	key := newJoinKey(t)
	auth := filepath.Join(dir, "auth")
	// Так пишет sshd при ExposeAuthInfo.
	os.WriteFile(auth, []byte("publickey "+key+"\n"), 0o600)
	env := map[string]string{"SSH_ORIGINAL_COMMAND": "join", "SSH_USER_AUTH": auth,
		"SSH_CONNECTION": "198.51.100.7 51234 203.0.113.5 22"}
	var sent joinReq
	post := func(b []byte) ([]byte, error) {
		json.Unmarshal(b, &sent)
		return []byte(`{"ok":true,"id":"tun_0123456789abcdef","name":"Телефон"}`), nil
	}
	var out bytes.Buffer
	in := strings.NewReader(`{"tok":"секрет","name":"Телефон","type":"phone","key":"ssh-ed25519 ПОДДЕЛКА","from":"1.2.3.4"}` + "\n")
	if code := RunJoin(in, &out, func(k string) string { return env[k] }, post); code != 0 {
		t.Fatalf("код %d: %s", code, out.String())
	}
	if sent.Key != key || sent.From != "198.51.100.7" || sent.Tok != "секрет" || sent.Type != "phone" {
		t.Fatalf("переслано: %+v", sent)
	}
	if !strings.HasSuffix(out.String(), "\n") || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("не одна строка: %q", out.String())
	}
	// Другая команда — отказ, панель не спрашивается.
	env["SSH_ORIGINAL_COMMAND"] = "sh"
	out.Reset()
	called := false
	if RunJoin(strings.NewReader("{}\n"), &out, func(k string) string { return env[k] },
		func([]byte) ([]byte, error) { called = true; return nil, nil }) == 0 || called {
		t.Fatal("не join, а пустили")
	}

	// AuthorizedKeysCommand: годный ключ → restrict; мусор → ничего.
	f := strings.Fields(key)
	out.Reset()
	if RunJoinKey(f, &out) != 0 || out.String() != "restrict "+key+"\n" {
		t.Fatalf("join-key: %q", out.String())
	}
	out.Reset()
	if RunJoinKey([]string{"ssh-ed25519", "мусор"}, &out) == 0 || out.Len() != 0 {
		t.Fatal("мусорный ключ принят")
	}
	if userAuthKey("password\npublickey "+key+"\n") != key {
		t.Fatal("SSH_USER_AUTH")
	}
}

func TestБлокSSHDДляJoin(t *testing.T) {
	cfg := "Port 22\n"
	out, changed := ensureJoinBlock(cfg, "/usr/local/bin/ssh_tunnel_panel")
	if !changed || !strings.Contains(out, "Match User tun_join") || !strings.Contains(out, "ForceCommand /usr/local/bin/ssh_tunnel_panel join\n") ||
		!strings.Contains(out, "ExposeAuthInfo yes") || !strings.Contains(out, "AllowTcpForwarding no") || !strings.Contains(out, "PermitTTY no") {
		t.Fatalf("%s", out)
	}
	if again, changed := ensureJoinBlock(out, "/usr/local/bin/ssh_tunnel_panel"); changed || again != out {
		t.Fatal("второй раз меняет")
	}
	if moved, changed := ensureJoinBlock(out, "/opt/p"); !changed || strings.Count(moved, "Match User tun_join") != 1 {
		t.Fatal("новый путь")
	}
	if ValidUsername(JoinUser) {
		t.Fatal("tun_join похож на пользователя устройства")
	}
}

// Сценарий стенда в памяти: два сервера A и B одной сети.
func TestУстройствоИзПанелиНаДвухСерверах(t *testing.T) {
	tn := newTestNet()
	a := tn.node(t, "a.example", "A")
	b := tn.node(t, "b.example", "B")
	b.join(t, a.invite(t))
	settle(a, b)
	idA, idB := a.n.SelfID(), b.n.SelfID()
	if got := a.srv.devServers(); len(got) != 2 || !got[0].Self || got[0].ID != idA {
		t.Fatalf("серверы для галочек: %+v", got)
	}

	// На панели A: устройство с A и B.
	res, err := a.srv.createInvite(inviteReq{Name: "Телефон", DeviceType: "android", Servers: []string{idB}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Invite.Servers, idA) {
		t.Fatalf("A не попал в список: %v", res.Invite.Servers)
	}
	r := joinAs(t, a.srv, tokOf(t, res.Text), newJoinKey(t), "198.51.100.7")
	if !r.OK {
		t.Fatalf("вход: %+v", r)
	}
	settle(a, b)
	if _, ok := b.hasUser(r.ID); !ok {
		t.Fatal("учётной записи нет на B")
	}
	ca, _ := a.store.Get(r.ID)
	p1 := a.srv.profileOf(ca)
	if len(p1.Servers) != 2 || !p1.Mesh || p1.ID != r.ID || p1.Servers[0].HK == "" {
		t.Fatalf("профиль: %+v", p1)
	}
	cb, _ := b.store.Get(r.ID)
	if pb := b.srv.profileOf(cb); len(pb.Servers) != 2 || pb.Rev != p1.Rev {
		t.Fatalf("профиль на B: %+v / rev A %d", pb, p1.Rev)
	}

	// Снять B на панели A: на B учётной записи нет, rev вырос.
	time.Sleep(5 * time.Millisecond)
	if err := a.srv.setDeviceScope(context.Background(), r.ID, []string{idA}, true); err != nil {
		t.Fatal(err)
	}
	settle(a, b)
	if _, ok := b.hasUser(r.ID); ok {
		t.Fatal("учётная запись осталась на B")
	}
	if provHas(b.prov, r.ID) {
		t.Fatal("пользователь остался в системе B")
	}
	ca, _ = a.store.Get(r.ID)
	p2 := a.srv.profileOf(ca)
	if len(p2.Servers) != 1 || p2.Servers[0].Host != "a.example" || p2.Rev <= p1.Rev {
		t.Fatalf("после снятия B: %+v (rev был %d)", p2, p1.Rev)
	}
	// meshd не говорит старой программе «учётная запись есть на всех»: её
	// серверы — в профиле.
	a.n.lastKnwn = ""
	a.n.reconcile(context.Background())
	a.adm.mu.Lock()
	shared := slices.Clone(a.adm.shared)
	a.adm.mu.Unlock()
	if slices.Contains(shared, r.ID) {
		t.Fatalf("запись со своим списком серверов в shared: %v", shared)
	}
	// На B устройство видно как «не здесь» — можно вернуть оттуда же.
	if el := b.srv.elsewhereClients(); len(el) != 1 || el[0].ID != r.ID {
		t.Fatalf("на B в списке: %+v", el)
	}
	if err := b.srv.setDeviceScope(context.Background(), r.ID, []string{idA, idB}, false); err != nil {
		t.Fatal(err)
	}
	settle(a, b)
	if _, ok := b.hasUser(r.ID); !ok {
		t.Fatal("с панели B не вернулось")
	}
	ca, _ = a.store.Get(r.ID)
	if ca.NoMesh != true || !slices.Contains(a.adm.policy.NoMesh, r.ID) || !slices.Contains(b.adm.policy.NoMesh, r.ID) {
		t.Fatalf("сеть устройств не выключилась везде: %+v / %v / %v", ca, a.adm.policy.NoMesh, b.adm.policy.NoMesh)
	}

	// «Удалить устройство» — нет ни на одном.
	if err := a.srv.deleteDevice(r.ID); err != nil {
		t.Fatal(err)
	}
	settle(a, b)
	if _, ok := a.hasUser(r.ID); ok {
		t.Fatal("осталась на A")
	}
	if _, ok := b.hasUser(r.ID); ok {
		t.Fatal("осталась на B")
	}
}

func TestПрофильСтаройУчётнойЗаписи(t *testing.T) {
	_, de, _ := twoServers(t)
	var old Client
	for _, c := range de.store.List() {
		old = c
	}
	if old.Servers != nil || old.NoMesh {
		t.Fatalf("старая запись: %+v", old)
	}
	p := de.srv.profileOf(old)
	raw, _ := json.Marshal(p)
	var m map[string]any
	json.Unmarshal(raw, &m)
	for _, k := range []string{"v", "id", "name", "rev", "servers", "mesh", "frozen"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("нет %s в %s", k, raw)
		}
	}
	if len(p.Servers) != 2 || !p.Mesh || p.Frozen || p.Rev == 0 {
		t.Fatalf("старая запись = все серверы, сеть есть: %s", raw)
	}
	// Через вход самообслуживания, без пароля, только про себя.
	de.srv.connUID = func(*http.Request) (int, bool) { return old.UID, true }
	code, body := selfGet(t, de.srv.SelfServiceHandler(), "/api/me/profile")
	if code != http.StatusOK || !strings.Contains(string(body), `"id":"`+old.ID+`"`) {
		t.Fatalf("%d %s", code, body)
	}
	de.srv.connUID = func(*http.Request) (int, bool) { return 1, true }
	if code, _ := selfGet(t, de.srv.SelfServiceHandler(), "/api/me/profile"); code != http.StatusForbidden {
		t.Fatalf("чужой: %d", code)
	}
	// Один сервер без сети: профиль — этот сервер, rev растёт от правок.
	s, _ := newTestServer(t)
	s.WithClientDefaults("solo.example", 22, "")
	s.hostKeysFn = func() []string { return []string{"ssh-ed25519 SOLO"} }
	c, _ := s.clients.CreateClient("ПК", DeviceWindows)
	c, _ = s.clients.Get(c.ID)
	p1 := s.profileOf(c)
	time.Sleep(2 * time.Millisecond)
	s.clients.Update(c.ID, func(c *Client) { c.Name = "ПК 2" })
	c, _ = s.clients.Get(c.ID)
	p2 := s.profileOf(c)
	if len(p1.Servers) != 1 || p1.Servers[0].Host != "solo.example" || p2.Rev <= p1.Rev || p2.Name != "ПК 2" {
		t.Fatalf("один сервер: %+v → %+v", p1, p2)
	}
}

func TestПолитикаБезСетиУстройств(t *testing.T) {
	p := buildPolicy(AccessSettings{}, []Client{{Username: "tun_a", NoMesh: true}, {Username: "tun_b"}})
	if !slices.Equal(p.NoMesh, []string{"tun_a"}) {
		t.Fatalf("%v", p.NoMesh)
	}
	data, _ := json.Marshal(p)
	if !strings.Contains(string(data), `"nomesh":["tun_a"]`) {
		t.Fatalf("%s", data)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(newJoinKey(t))); err != nil {
		t.Fatal(err)
	}
}

func provHas(f *fakeProvisioner, user string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.users[user]
	return ok
}
