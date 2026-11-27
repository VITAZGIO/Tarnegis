package panel

// ТЗ 54: короткий код приглашения — панель (PAKE, одноразовость, срок,
// ограничение попыток, блокировка) и команда join в два раунда.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/invite"
)

func hostKeyPair(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	line := newJoinKey(t)
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(line), k
}

func selfJoin(t *testing.T, s *Server, req joinReq) joinResp {
	t.Helper()
	s.connUID = func(*http.Request) (int, bool) { return joinTestUID, true }
	s.joinUID = func() (int, bool) { return joinTestUID, true }
	data, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/v1/join", bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.SelfServiceHandler().ServeHTTP(rec, r)
	var out joinResp
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// codeJoin — вход по коду так, как его ведут программа и команда join.
// pakeErr — программа не приняла подтверждение панели.
func codeJoin(t *testing.T, s *Server, secret [invite.SecretLen]byte, hk ssh.PublicKey, key, from string) (joinResp, error) {
	t.Helper()
	tag := invite.CodeTag(secret)
	msgA, pc, err := invite.StartPake(secret, hk, tag)
	if err != nil {
		t.Fatal(err)
	}
	hkLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hk)))
	r := selfJoin(t, s, joinReq{V: invite.JoinV2, Tag: tag, HK: hkLine, Pake: msgA, Name: "Ноутбук", Type: "pc", Key: key, From: from})
	if !r.OK {
		return r, nil
	}
	if r.Sid == "" || r.ID != "" {
		t.Fatalf("первый ответ: %+v", r)
	}
	conf, err := pc.Finish(r.Pake, r.Conf)
	if err != nil {
		return joinResp{}, err
	}
	return selfJoin(t, s, joinReq{V: invite.JoinV2, Conf: conf, Sid: r.Sid, Key: key, From: from}), nil
}

func secretOfCode(t *testing.T, code string) [invite.SecretLen]byte {
	t.Helper()
	c, err := invite.ParseCode(code)
	if err != nil {
		t.Fatal(err)
	}
	return c.Secret
}

func TestКороткийКодВход(t *testing.T) {
	s, _ := newTestServer(t)
	s.WithClientDefaults("203.0.113.5", 22, "")
	hkLine, hk := hostKeyPair(t)
	s.hostKeysFn = func() []string { return []string{hkLine} }
	res, err := s.createInvite(inviteReq{Name: "Ноутбук", DeviceType: "windows"})
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.ReplaceAll(res.Code, "-", "")) != 20 || res.Text == "" || res.QR == "" {
		t.Fatalf("приглашение: %+v", res)
	}
	c, _ := invite.ParseCode(res.Code)
	if c.Host != "203.0.113.5" || c.Port != 22 {
		t.Fatalf("адрес в коде: %+v", c)
	}
	// В коде нет ни ключа хоста, ни срока, ни имени; в файле — секрет есть
	// (CPace симметричный), токена длинной строки — только хеш.
	key := newJoinKey(t)
	r, err := codeJoin(t, s, c.Secret, hk, key, "198.51.100.7")
	if err != nil || !r.OK || !ValidUsername(r.ID) || r.Name != "Ноутбук" || r.Sid != "" {
		t.Fatalf("вход: %+v %v", r, err)
	}
	if cl, ok := s.clients.Get(r.ID); !ok || cl.PublicKey != strings.TrimSpace(key) || cl.DeviceType != DeviceWindows {
		t.Fatalf("учётная запись: %+v", cl)
	}
	// Одноразовый: ни код, ни длинная строка второй раз не входят.
	if r, _ := codeJoin(t, s, c.Secret, hk, newJoinKey(t), "198.51.100.7"); r.OK {
		t.Fatalf("код второй раз: %+v", r)
	}
	if r := joinAs(t, s, tokOf(t, res.Text), newJoinKey(t), "198.51.100.7"); r.Err != joinUsed {
		t.Fatalf("длинная строка после кода: %+v", r)
	}
	// И наоборот: вошли длинной строкой — код уже не годен.
	res2, _ := s.createInvite(inviteReq{Name: "Б", DeviceType: "linux"})
	if r := joinAs(t, s, tokOf(t, res2.Text), newJoinKey(t), "198.51.100.8"); !r.OK {
		t.Fatalf("sshtun1: %+v", r)
	}
	if r, _ := codeJoin(t, s, secretOfCode(t, res2.Code), hk, newJoinKey(t), "198.51.100.8"); r.OK {
		t.Fatalf("код после длинной строки: %+v", r)
	}
}

func TestКороткийКодПодменаИНеверныйСекрет(t *testing.T) {
	s, _ := newTestServer(t)
	s.WithClientDefaults("203.0.113.5", 22, "")
	hkLine, hk := hostKeyPair(t)
	s.hostKeysFn = func() []string { return []string{hkLine} }
	res, _ := s.createInvite(inviteReq{Name: "Ноутбук", DeviceType: "windows"})
	sec := secretOfCode(t, res.Code)

	// Программе показали чужой ключ хоста — панель отказывает сразу.
	_, other := hostKeyPair(t)
	if r, err := codeJoin(t, s, sec, other, newJoinKey(t), "198.51.100.20"); err != nil || r.OK || r.Err != joinConfirm {
		t.Fatalf("чужой ключ: %+v %v", r, err)
	}

	// Неверный секрет с той же меткой: программа не принимает ответ панели.
	wrong := sameTagSecret(sec)
	for i := 0; i < maxCodeFails; i++ {
		s.limiter.Success("joincode|" + res.Invite.ID) // без задержек limiter'а
		if _, err := codeJoin(t, s, wrong, hk, newJoinKey(t), "198.51.100.2"+string(rune('0'+i))); err != invite.ErrPakeMismatch {
			t.Fatalf("попытка %d: %v", i, err)
		}
	}
	// После maxCodeFails — заблокирован, и верный секрет уже не войдёт.
	s.limiter.Success("joincode|" + res.Invite.ID)
	if r, _ := codeJoin(t, s, sec, hk, newJoinKey(t), "198.51.100.30"); r.Err != joinLocked {
		t.Fatalf("после неудач: %+v", r)
	}
	if v := s.inviteStore().open(); len(v) != 1 || !v[0].Locked {
		t.Fatalf("список: %+v", v)
	}

	// Подтверждение не то — отказ и без учётной записи.
	res2, _ := s.createInvite(inviteReq{Name: "В", DeviceType: "windows"})
	sec2 := secretOfCode(t, res2.Code)
	tag := invite.CodeTag(sec2)
	msgA, _, _ := invite.StartPake(sec2, hk, tag)
	key := newJoinKey(t)
	r := selfJoin(t, s, joinReq{V: invite.JoinV2, Tag: tag, HK: hkLine, Pake: msgA, Key: key, From: "198.51.100.40"})
	if !r.OK {
		t.Fatal(r)
	}
	if r := selfJoin(t, s, joinReq{V: invite.JoinV2, Conf: []byte("мимо"), Sid: r.Sid, Key: key, From: "198.51.100.40"}); r.OK || r.Err != joinConfirm {
		t.Fatalf("неверное подтверждение: %+v", r)
	}
	if len(s.clients.List()) != 0 {
		t.Fatal("учётная запись без PAKE")
	}
}

func sameTagSecret(sec [invite.SecretLen]byte) [invite.SecretLen]byte {
	tag := invite.CodeTag(sec)
	w := sec
	for i := 0; ; i++ {
		w[0], w[1] = byte(i), byte(i>>8)
		if w != sec && invite.CodeTag(w) == tag {
			return w
		}
	}
}

func TestКороткийКодСрокИЛимитПоАдресу(t *testing.T) {
	s, _ := newTestServer(t)
	s.WithClientDefaults("203.0.113.5", 22, "")
	hkLine, hk := hostKeyPair(t)
	s.hostKeysFn = func() []string { return []string{hkLine} }
	res, _ := s.createInvite(inviteReq{Name: "Ноутбук", DeviceType: "windows"})
	sec := secretOfCode(t, res.Code)

	// Подбор с одного адреса — limit, даже с верным кодом.
	for i := 0; i < lockAfter; i++ {
		s.limiter.Fail("join|203.0.113.66")
	}
	if r, _ := codeJoin(t, s, sec, hk, newJoinKey(t), "203.0.113.66"); r.Err != joinLimit {
		t.Fatalf("подбор: %+v", r)
	}
	// Срок вышел.
	st := s.inviteStore()
	st.now = func() time.Time { return time.Now().Add(devInviteTTL + time.Minute) }
	if r, _ := codeJoin(t, s, sec, hk, newJoinKey(t), "203.0.113.67"); r.OK {
		t.Fatalf("истёкший: %+v", r)
	}
}

func TestКомандаJoinДваРаунда(t *testing.T) {
	dir := t.TempDir()
	key := newJoinKey(t)
	auth := filepath.Join(dir, "auth")
	os.WriteFile(auth, []byte("publickey "+key+"\n"), 0o600)
	env := map[string]string{"SSH_ORIGINAL_COMMAND": "join", "SSH_USER_AUTH": auth,
		"SSH_CONNECTION": "198.51.100.7 51234 203.0.113.5 22"}
	var sent []joinReq
	post := func(b []byte) ([]byte, error) {
		var r joinReq
		json.Unmarshal(b, &r)
		sent = append(sent, r)
		if len(sent) == 1 {
			return []byte(`{"ok":true,"pake":"AQI=","conf":"AwQ=","sid":"s1"}`), nil
		}
		return []byte(`{"ok":true,"id":"tun_0123456789abcdef","name":"Ноутбук"}`), nil
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan int, 1)
	go func() { done <- RunJoin(inR, outW, func(k string) string { return env[k] }, post); outW.Close() }()
	rd := bufio.NewReader(outR)
	inW.Write([]byte(`{"v":2,"tag":"ab","hk":"x","pake":"AQI=","sid":"чужой"}` + "\n"))
	first, _ := rd.ReadString('\n')
	if strings.Contains(first, "sid") || !strings.Contains(first, `"pake"`) {
		t.Fatalf("первая строка программе: %q", first)
	}
	inW.Write([]byte(`{"v":2,"conf":"BQY=","sid":"подделка"}` + "\n"))
	last, _ := rd.ReadString('\n')
	if code := <-done; code != 0 || !strings.Contains(last, "tun_0123456789abcdef") {
		t.Fatalf("итог %d: %q", code, last)
	}
	if len(sent) != 2 || sent[0].Sid != "" || sent[1].Sid != "s1" || sent[1].Key != key || sent[1].From != "198.51.100.7" {
		t.Fatalf("переслано: %+v", sent)
	}
}
