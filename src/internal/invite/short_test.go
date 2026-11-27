package invite

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/hostkey"
)

// joinV2 — поддельная панель для короткого кода (как devinvite_code.go):
// true — строка была v2 и обработана.
func (f *fakeServer) joinV2(ch ssh.Channel, rd *bufio.Reader, line []byte, key string) bool {
	var req joinV2Req
	if json.Unmarshal(line, &req) != nil || req.V != JoinV2 {
		return false
	}
	write := func(v any) {
		out, _ := json.Marshal(v)
		ch.Write(append(out, '\n'))
	}
	f.mu.Lock()
	f.joins++
	sec, ok := f.codes[req.Tag]
	hk := f.shownHK
	f.mu.Unlock()
	if hk == nil {
		hk = f.hostKey.PublicKey()
	}
	if !ok {
		write(JoinResponse{Err: "unknown"})
		return true
	}
	msgB, conf, want, err := PakeRespond(sec, hk, req.Tag, req.Pake)
	if err != nil {
		write(JoinResponse{Err: "unknown"})
		return true
	}
	write(joinV2Resp{JoinResponse: JoinResponse{OK: true}, Pake: msgB, Conf: conf})
	line, _ = rd.ReadBytes('\n')
	var c joinV2Req
	json.Unmarshal(line, &c)
	if string(c.Conf) != string(want) {
		write(JoinResponse{Err: "confirm"})
		return true
	}
	f.mu.Lock()
	f.n++
	id := fmt.Sprintf("tun_%04d", f.n)
	f.accounts[id] = key
	delete(f.codes, req.Tag) // один раз
	f.mu.Unlock()
	write(JoinResponse{OK: true, ID: id, Name: req.Name})
	return true
}

func (f *fakeServer) code(t *testing.T, sec [SecretLen]byte) string {
	f.mu.Lock()
	f.codes[CodeTag(sec)] = sec
	f.mu.Unlock()
	code, err := EncodeCode("127.0.0.1", f.port(), sec)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestRedeemКороткимКодом(t *testing.T) {
	f := newFakeServer(t)
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	code := f.code(t, secretOf(11))
	r, err := Redeem(context.Background(), "tarnegis:"+strings.ToLower(code), Options{KeyDir: dir, KnownHosts: kh, Name: "Ноутбук", Type: "pc"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Device.ID != "tun_0001" || r.Device.Join != f.ln.Addr().String() || r.ProfileErr != nil {
		t.Fatalf("%+v", r)
	}
	// Ключ хоста подтверждён PAKE и записан — дальше строго, как раньше.
	known := hostkey.Known(kh, "127.0.0.1", f.port())
	if len(known) != 1 || known[0].Fingerprint != ssh.FingerprintSHA256(f.hostKey.PublicKey()) {
		t.Fatalf("known_hosts: %+v", known)
	}
	// Второй раз тот же код — сервер его уже не знает.
	_, err = Redeem(context.Background(), code, Options{KeyDir: dir, KnownHosts: kh, Type: "pc"})
	var je *JoinError
	if !errors.As(err, &je) || je.Code != "unknown" || !je.Short {
		t.Fatalf("повтор: %v", err)
	}
}

func TestКороткийКодНеверныйСекретИПодмена(t *testing.T) {
	f := newFakeServer(t)
	dir := t.TempDir()
	sec := secretOf(12)
	f.code(t, sec)
	// Тот же тег, другой секрет: код с той же меткой подобрать нельзя, но
	// проверяем саму PAKE — сервер с настоящим секретом, программа с другим.
	wrong := sec
	wrong[0] ^= 0x80
	f.mu.Lock()
	f.codes[CodeTag(wrong)] = sec
	f.mu.Unlock()
	bad, _ := EncodeCode("127.0.0.1", f.port(), wrong)
	if _, err := Redeem(context.Background(), bad, Options{KeyDir: dir, KnownHosts: filepath.Join(dir, "kh")}); !errors.Is(err, ErrPakeMismatch) {
		t.Fatalf("неверный секрет: %v", err)
	}
	// Подмена: сервер в PAKE ставит другой ключ хоста, чем показал в SSH.
	f.mu.Lock()
	f.shownHK = newHostKey(t)
	f.mu.Unlock()
	good, _ := EncodeCode("127.0.0.1", f.port(), sec)
	if _, err := Redeem(context.Background(), good, Options{KeyDir: dir, KnownHosts: filepath.Join(dir, "kh")}); !errors.Is(err, ErrPakeMismatch) {
		t.Fatalf("другой ключ хоста: %v", err)
	}
	f.mu.Lock()
	n := len(f.accounts)
	f.mu.Unlock()
	if n != 0 {
		t.Fatal("учётная запись заведена без PAKE")
	}
	if hostkey.Known(filepath.Join(dir, "kh"), "127.0.0.1", f.port()) != nil {
		t.Fatal("ключ хоста записан без PAKE")
	}
}

func TestСтароеПриглашениеРаботает(t *testing.T) {
	f := newFakeServer(t)
	f.tokens["старый-токен-0123456789"] = ""
	dir := t.TempDir()
	r, err := Redeem(context.Background(), f.invite("старый-токен-0123456789", 1<<40), Options{KeyDir: dir, KnownHosts: filepath.Join(dir, "kh")})
	if err != nil || r.Device.ID == "" {
		t.Fatalf("sshtun1: %+v %v", r, err)
	}
}
