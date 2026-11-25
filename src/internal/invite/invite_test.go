package invite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/hostkey"
)

func TestParseХорошее(t *testing.T) {
	f := newFakeServer(t)
	text := "Вот приглашение: " + f.invite("tok1", time.Now().Add(time.Hour).Unix()) + " — вставь в программу"
	if !IsInvite(text) {
		t.Fatal("не узнал приглашение")
	}
	inv, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Host != "127.0.0.1" || inv.Port != f.port() || inv.Tok != "tok1" || inv.Name != "Телефон" || inv.Expired(time.Now()) {
		t.Fatalf("%+v", inv)
	}
}

func TestParseИстёкшееИМусор(t *testing.T) {
	f := newFakeServer(t)
	inv, err := Parse(f.invite("tok1", 1000))
	if err != nil || !inv.Expired(time.Now()) {
		t.Fatalf("истёкшее: %v %v", inv, err)
	}
	for _, bad := range []string{"", "привет", "sshtun1:", "sshtun1:!!!", "sshtun1:e30", // {}
		Encode(Invite{V: 1, Host: "h", Port: 22, HK: "мусор", Tok: "t", Exp: 5}),
		Encode(Invite{V: 1, Host: "a b", Port: 22, HK: f.hkLine(), Tok: "t", Exp: 5}),
		Encode(Invite{V: 1, Host: "h", Port: 22, HK: f.hkLine(), Exp: 5})} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("принял мусор %q", bad)
		}
	}
	if _, err := Parse(Encode(Invite{V: 2, Host: "h", Port: 22, HK: f.hkLine(), Tok: "t", Exp: 5})); err == nil ||
		!strings.Contains(err.Error(), "обнови") {
		t.Errorf("версия 2: %v", err)
	}
}

func TestRedeemИстёкшееНеИдётНаСервер(t *testing.T) {
	f := newFakeServer(t)
	f.tokens["t"] = ""
	_, err := Redeem(context.Background(), f.invite("t", time.Now().Add(-time.Minute).Unix()), Options{KeyDir: t.TempDir()})
	if !errors.Is(err, ErrExpired) || f.joins != 0 {
		t.Fatalf("err=%v joins=%d", err, f.joins)
	}
}

func TestRedeemВходИПрофиль(t *testing.T) {
	f := newFakeServer(t)
	f.tokens["good"] = ""
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	r, err := Redeem(context.Background(), f.invite("good", time.Now().Add(time.Hour).Unix()),
		Options{KeyDir: dir, KnownHosts: kh, Name: "Мой телефон", Type: "phone"})
	if err != nil || r.ProfileErr != nil {
		t.Fatal(err, r.ProfileErr)
	}
	if f.lastReq.Name != "Мой телефон" || f.lastReq.Type != "phone" || f.lastReq.Tok != "good" {
		t.Fatalf("запрос: %+v", f.lastReq)
	}
	if r.Device.ID != "tun_0001" || r.Device.KeyPath != filepath.Join(dir, "device_tun_0001") {
		t.Fatalf("устройство: %+v", r.Device)
	}
	if fi, err := os.Stat(r.Device.KeyPath); err != nil || fi.Size() == 0 {
		t.Fatal("нет ключа устройства", err)
	}
	if _, err := LoadKey(r.Device.KeyPath); err != nil {
		t.Fatal(err)
	}
	p := r.Profile
	if p.Rev != 3 || !p.Mesh || len(p.Servers) != 2 || p.Servers[0].Flag != "\U0001F1E9\U0001F1EA" {
		t.Fatalf("профиль: %+v", p)
	}
	// Ключи серверов записаны — вопроса «доверять ли?» не будет.
	if len(hostkey.Known(kh, "127.0.0.1", f.port())) != 1 || len(hostkey.Known(kh, "198.51.100.7", 22)) != 1 {
		t.Fatal("ключи серверов не записаны")
	}
	// Тот же токен второй раз — «уже использовано», временный ключ убран.
	_, err = Redeem(context.Background(), f.invite("good", time.Now().Add(time.Hour).Unix()), Options{KeyDir: dir, KnownHosts: kh})
	if err == nil || !strings.Contains(err.Error(), "уже использовано") {
		t.Fatalf("повтор: %v", err)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "device_new_*"))
	if len(m) != 0 {
		t.Fatalf("остался временный ключ: %v", m)
	}
}

func TestRedeemЗаменяетСтарыйКлючСервера(t *testing.T) {
	f := newFakeServer(t)
	f.tokens["t"] = ""
	other := newFakeServer(t)
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	// Под этим адресом записан ключ прежней установки сервера.
	hostkey.Pin(kh, "127.0.0.1", f.port(), other.hkLine())
	if _, err := Redeem(context.Background(), f.invite("t", time.Now().Add(time.Hour).Unix()), Options{KeyDir: dir, KnownHosts: kh}); err != nil {
		t.Fatal(err)
	}
	k := hostkey.Known(kh, "127.0.0.1", f.port())
	if len(k) != 1 || k[0].Fingerprint != fingerprint(f.hostKey.PublicKey()) {
		t.Fatalf("%+v", k)
	}
}

func TestJoinОшибкиСервера(t *testing.T) {
	f := newFakeServer(t)
	f.tokens["e"], f.tokens["z"] = "expired", "frozen"
	for tok, want := range map[string]string{"e": "истекло", "z": "заморожено", "nope": "не знает"} {
		_, err := Redeem(context.Background(), f.invite(tok, time.Now().Add(time.Hour).Unix()), Options{KeyDir: t.TempDir()})
		var je *JoinError
		if !errors.As(err, &je) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", tok, err)
		}
	}
}

func TestJoinЧужойКлючСервера(t *testing.T) {
	f := newFakeServer(t)
	f.tokens["t"] = ""
	other := newFakeServer(t)
	inv, _ := Parse(f.invite("t", time.Now().Add(time.Hour).Unix()))
	inv.HK = other.hkLine() // приглашение с ключом другого сервера
	_, err := Redeem(context.Background(), Encode(inv), Options{KeyDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "не совпал") || f.joins != 0 {
		t.Fatalf("err=%v joins=%d", err, f.joins)
	}
}

func TestRedeemБезПрофиляСтарыйСервер(t *testing.T) {
	f := newFakeServer(t)
	f.tokens["t"] = ""
	f.profile = func(string) (int, string) { return 404, "404 page not found" }
	r, err := Redeem(context.Background(), f.invite("t", time.Now().Add(time.Hour).Unix()), Options{KeyDir: t.TempDir()})
	if err != nil || !errors.Is(r.ProfileErr, ErrNoProfile) {
		t.Fatal(err, r.ProfileErr)
	}
	if len(r.Profile.Servers) != 1 || r.Profile.Servers[0].Port != f.port() || !r.Profile.Mesh {
		t.Fatalf("%+v", r.Profile)
	}
}

func TestParseProfileСтарыйИНовый(t *testing.T) {
	p, err := ParseProfile([]byte(`{"v":1,"id":"tun_x","rev":2,"servers":[{"host":"h","port":0},{"host":"bad host"}]}`))
	if err != nil || !p.MeshAllowed() || len(p.Servers) != 1 || p.Servers[0].Port != 22 {
		t.Fatalf("%+v %v", p, err)
	}
	p, _ = ParseProfile([]byte(`{"v":1,"id":"tun_x","rev":2,"servers":[],"mesh":false,"frozen":true}`))
	if p.MeshAllowed() || !p.Frozen {
		t.Fatalf("%+v", p)
	}
	if _, err := ParseProfile([]byte(`{"v":1}`)); err == nil {
		t.Fatal("без id")
	}
}

func TestFlagEmoji(t *testing.T) {
	if FlagEmoji("gb") != "\U0001F1EC\U0001F1E7" || FlagEmoji("") != "" || FlagEmoji("1x") != "" {
		t.Fatal(FlagEmoji("gb"))
	}
}
