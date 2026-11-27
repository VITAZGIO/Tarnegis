package invite

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func secretOf(b byte) (s [SecretLen]byte) {
	for i := range s {
		s[i] = b + byte(i)*37
	}
	return
}

func TestКодТудаИОбратноВсеТипы(t *testing.T) {
	cases := []struct {
		host  string
		port  int
		chars int // 0 — не проверять
	}{
		{"203.0.113.5", 22, 20},
		{"203.0.113.5", 2222, 24},
		{"0.0.0.0", 22, 20},
		{"vpn.example.com", 22, 0},
		{"vpn.example.com", 443, 0},
		{"a", 22, 0},
		{strings.Repeat("x", 63), 65535, 0},
		{"2001:db8::1", 22, 40},
		{"2001:db8::1", 8022, 44},
	}
	for _, c := range cases {
		for k := 0; k < 20; k++ {
			var sec [SecretLen]byte
			rand.Read(sec[:])
			code, err := EncodeCode(c.host, c.port, sec)
			if err != nil {
				t.Fatalf("%s: %v", c.host, err)
			}
			norm, _ := NormalizeCode(code)
			if c.chars != 0 && len(norm) != c.chars {
				t.Fatalf("%s:%d — %d символов (%s), ждал %d", c.host, c.port, len(norm), code, c.chars)
			}
			if len(norm)%4 != 0 || strings.Count(code, "-") != len(norm)/4-1 {
				t.Fatalf("группы: %s", code)
			}
			if n, err := CodeLength(norm[:14]); err != nil || (n != 0 && n != len(norm)) {
				t.Fatalf("длина по началу %s: %d %v", code, n, err)
			}
			got, err := ParseCode(code)
			if err != nil {
				t.Fatalf("%s: %v", code, err)
			}
			if got.Host != c.host || got.Port != c.port || got.Secret != sec {
				t.Fatalf("%s → %+v", code, got)
			}
		}
	}
	if _, err := EncodeCode(strings.Repeat("x", 64), 22, secretOf(1)); err == nil {
		t.Fatal("домен длиннее 63 влез в код")
	}
	if _, err := EncodeCode("сервер.рф", 22, secretOf(1)); err == nil {
		t.Fatal("не ASCII влез в код")
	}
}

func TestКодПервыйСимволЗнаетДлину(t *testing.T) {
	code, _ := EncodeCode("198.51.100.7", 22, secretOf(9))
	if n, err := CodeLength(code[:1]); err != nil || n != 20 {
		t.Fatalf("IPv4: %d %v", n, err)
	}
	code, _ = EncodeCode("2001:db8::7", 22, secretOf(9))
	if n, _ := CodeLength(code[:1]); n != 40 {
		t.Fatalf("IPv6: %d", n)
	}
	code, _ = EncodeCode("example.org", 22, secretOf(9))
	if n, _ := CodeLength(code[:1]); n != 0 {
		t.Fatalf("домен по первому символу: %d", n)
	}
	if _, err := CodeLength("Z"); err == nil {
		t.Fatal("тип из запаса принят")
	}
}

func TestКодЛовитОпечаткиИПерестановки(t *testing.T) {
	for _, host := range []string{"203.0.113.5", "vpn.example.com", "2001:db8::1"} {
		code, _ := EncodeCode(host, 22, secretOf(3))
		norm, _ := NormalizeCode(code)
		b := []byte(norm)
		for i := range b {
			for _, c := range []byte(codeAlphabet) {
				if c == norm[i] {
					continue
				}
				x := append([]byte(nil), b...)
				x[i] = c
				if got, err := ParseCode(string(x)); err == nil {
					t.Fatalf("%s: замена %d на %c прошла: %+v", host, i, c, got)
				}
			}
		}
		for i := range b {
			for j := i + 1; j < len(b); j++ {
				if b[i] == b[j] {
					continue
				}
				x := append([]byte(nil), b...)
				x[i], x[j] = x[j], x[i]
				if _, err := ParseCode(string(x)); err == nil {
					t.Fatalf("%s: перестановка %d↔%d прошла", host, i, j)
				}
			}
		}
	}
	code, _ := EncodeCode("203.0.113.5", 22, secretOf(3))
	bad := []byte(code)
	bad[3] = map[bool]byte{true: 'A', false: 'B'}[bad[3] != 'A']
	if _, err := ParseCode(string(bad)); !errors.Is(err, ErrCodeTypo) {
		t.Fatalf("ошибка: %v", err)
	}
	if _, err := ParseCode(code + "-0000"); !errors.Is(err, ErrCodeTypo) {
		t.Fatalf("лишние символы: %v", err)
	}
	if _, err := ParseCode(code[:10]); err == nil || !strings.Contains(err.Error(), "ещё") {
		t.Fatalf("неполный: %v", err)
	}
}

func TestКодПрячетАдрес(t *testing.T) {
	// Тот же адрес с разными секретами — тело кода каждый раз другое, и
	// биты адреса как есть на своём месте не стоят.
	seen := map[string]bool{}
	plain := fmt.Sprintf("%08b%08b%08b%08b", 203, 0, 113, 5)
	for k := 0; k < 50; k++ {
		var sec [SecretLen]byte
		rand.Read(sec[:])
		code, _ := EncodeCode("203.0.113.5", 22, sec)
		norm, _ := NormalizeCode(code)
		vals := make([]byte, len(norm))
		for i := range norm {
			vals[i] = byte(strings.IndexByte(codeAlphabet, norm[i]))
		}
		r := bitReader{chars: vals}
		var body strings.Builder
		for i := 0; i < 32; i++ {
			body.WriteByte('0' + byte(r.get(headBits+i, 1)))
		}
		if body.String() == plain {
			t.Fatalf("адрес в коде без маски: %s", code)
		}
		seen[body.String()] = true
	}
	if len(seen) < 45 {
		t.Fatalf("тело кода почти не меняется от секрета: %d разных из 50", len(seen))
	}
}

func TestКодНормализация(t *testing.T) {
	code, _ := EncodeCode("203.0.113.5", 22, secretOf(5))
	norm, _ := NormalizeCode(code)
	variants := []string{
		code,
		norm,
		strings.ToLower(code),
		strings.ReplaceAll(code, "-", " "),
		"  tarnegis:" + code + "\n",
		"TARNEGIS:" + strings.ToLower(norm),
		strings.NewReplacer("0", "O", "1", "I").Replace(code),
		strings.NewReplacer("1", "l").Replace(strings.ToLower(code)),
		"Вот код: tarnegis:" + code + " — на сутки",
	}
	for _, v := range variants {
		inv, err := Parse(v)
		if err != nil || !inv.Short || inv.Host != "203.0.113.5" || inv.Secret != secretOf(5) {
			t.Fatalf("%q: %+v %v", v, inv, err)
		}
		if !IsInvite(v) {
			t.Fatalf("IsInvite(%q) = false", v)
		}
	}
	for _, v := range []string{"", "привет", "HELLO", "sshtunnel://import?x", "U000-0000-0000-0000-0000"} {
		if _, err := Parse(v); err == nil {
			t.Fatalf("%q разобран", v)
		}
	}
	if IsInvite("привет") || IsInvite(`{"host":"x"}`) {
		t.Fatal("не приглашение принято за код")
	}
}

func TestInspectДляПоля(t *testing.T) {
	code, _ := EncodeCode("203.0.113.5", 22, secretOf(5))
	m, err := Inspect(code[:7], time.Now())
	if err != nil || m["partial"] != true || m["have"] != 6 || m["need"] != 20 {
		t.Fatalf("начало: %v %v", m, err)
	}
	m, err = Inspect(code, time.Now())
	if err != nil || m["short"] != true || m["host"] != "203.0.113.5" || m["expired"] != false {
		t.Fatalf("целый: %v %v", m, err)
	}
	bad := strings.Replace(code, code[2:3], map[bool]string{true: "A", false: "B"}[code[2] != 'A'], 1)
	if _, err := Inspect(bad, time.Now()); !errors.Is(err, ErrCodeTypo) {
		t.Fatalf("опечатка: %v", err)
	}
}

func newHostKey(t *testing.T) ssh.PublicKey {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	sg, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return sg.PublicKey()
}

func TestPAKE(t *testing.T) {
	sec := secretOf(7)
	tag := CodeTag(sec)
	hk := newHostKey(t)
	run := func(devSecret [SecretLen]byte, devHK ssh.PublicKey) error {
		msgA, pc, err := StartPake(devSecret, devHK, tag)
		if err != nil {
			t.Fatal(err)
		}
		msgB, conf, want, err := PakeRespond(sec, hk, tag, msgA)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pc.Finish(msgB, conf)
		if err != nil {
			return err
		}
		if string(got) != string(want) {
			t.Fatal("подтверждения программы и панели разные")
		}
		return nil
	}
	if err := run(sec, hk); err != nil {
		t.Fatalf("верный секрет: %v", err)
	}
	wrong := sec
	wrong[6] ^= 1
	if err := run(wrong, hk); !errors.Is(err, ErrPakeMismatch) {
		t.Fatalf("неверный секрет: %v", err)
	}
	if err := run(sec, newHostKey(t)); !errors.Is(err, ErrPakeMismatch) {
		t.Fatalf("чужой ключ хоста (подмена): %v", err)
	}
}
