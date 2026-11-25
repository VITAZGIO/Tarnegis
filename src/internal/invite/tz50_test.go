package invite

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/config"
	"sshtunnel/internal/hostkey"
)

func ecdsaSigner(t *testing.T) ssh.Signer {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := ssh.NewSignerFromKey(k)
	return s
}

func keyLine(k ssh.PublicKey) string { return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) }

// ТЗ 50: сервер, как настоящий sshd, показывает и ECDSA, и ed25519, а в
// приглашении — ed25519. Чтение профиля должно попросить именно ed25519:
// раньше Go-клиент просил ECDSA первым, ключ «не совпадал с приглашением»,
// профиль не читался и у устройства оставался один сервер. Под адресом —
// старые ключи прежней установки обоих типов (переустановка).
func TestRedeemПрофильСДвумяКлючамиХоста(t *testing.T) {
	f := newFakeServer(t)
	f.extra = []ssh.Signer{ecdsaSigner(t)}
	f.tokens["t"] = ""
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	old := newFakeServer(t)
	hostkey.Pin(kh, "127.0.0.1", f.port(), old.hkLine())
	staleEC := ecdsaSigner(t)
	hostkey.Trust(kh, "127.0.0.1", f.port(), keyLine(staleEC.PublicKey())) // второй тип рядом
	if n := len(hostkey.Known(kh, "127.0.0.1", f.port())); n != 2 {
		t.Fatalf("стенд: %d ключей", n)
	}

	r, err := Redeem(context.Background(), f.invite("t", time.Now().Add(time.Hour).Unix()), Options{KeyDir: dir, KnownHosts: kh})
	if err != nil || r.ProfileErr != nil {
		t.Fatalf("вход: %v / профиль: %v", err, r.ProfileErr)
	}
	if len(r.Profile.Servers) != 2 || r.Profile.From == "" {
		t.Fatalf("профиль: %+v", r.Profile)
	}
	k := hostkey.Known(kh, "127.0.0.1", f.port())
	if len(k) != 1 || k[0].Fingerprint != fingerprint(f.hostKey.PublicKey()) {
		t.Fatalf("после входа под адресом: %+v", k)
	}

	// Следующая сверка — по known_hosts, как на телефоне (DeviceFetch).
	signer, err := LoadKey(r.Device.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	tg := Target{Host: "127.0.0.1", Port: f.port(), User: r.Device.ID, Signer: signer,
		HostKey: hostkey.Callback(kh, nil), KnownHosts: kh}
	if _, err := Fetch(context.Background(), tg); err != nil {
		t.Fatalf("сверка по known_hosts: %v", err)
	}
	// Без списка типов — та самая ошибка «ключ изменился» (ECDSA вместо
	// записанного ed25519).
	tg.KnownHosts = ""
	_, err = Fetch(context.Background(), tg)
	var ch *hostkey.ErrChanged
	if !errors.As(err, &ch) {
		t.Fatalf("без алгоритмов ждал «ключ изменился», а: %v", err)
	}
}

// Ключи из профиля — доверенный источник: устаревший ключ того же типа под
// адресом другого сервера заменяется, ключ другого типа дописывается рядом;
// ключ сервера, с которого профиль прочитан (его только что подтвердило
// соединение), чужими словами не перебивается.
func TestPinProfileДоверенныйИсточник(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	live := newFakeServer(t)
	b1, b2 := newFakeServer(t), newFakeServer(t) // старый и новый ed25519 сервера Б
	ec := ecdsaSigner(t)
	hostkey.Pin(kh, "127.0.0.1", 2201, live.hkLine())
	hostkey.Pin(kh, "198.51.100.7", 22, b1.hkLine())
	hostkey.Pin(kh, "198.51.100.8", 22, keyLine(ec.PublicKey()))
	other := newFakeServer(t)
	list := []config.DeviceServer{
		{Host: "127.0.0.1", Port: 2201, HostKey: other.hkLine()}, // сам live — не трогать
		{Host: "198.51.100.7", Port: 22, HostKey: b2.hkLine()},   // переустановлен — заменить
		{Host: "198.51.100.8", Port: 22, HostKey: b2.hkLine()},   // записан только ECDSA — дописать
		{Host: "198.51.100.9", Port: 22, HostKey: b2.hkLine()},   // нет — записать
	}
	PinProfile(kh, list, "127.0.0.1:2201")
	if k := hostkey.Known(kh, "127.0.0.1", 2201); len(k) != 1 || k[0].Fingerprint != fingerprint(live.hostKey.PublicKey()) {
		t.Fatalf("live перебит: %+v", k)
	}
	if k := hostkey.Known(kh, "198.51.100.7", 22); len(k) != 1 || k[0].Fingerprint != fingerprint(b2.hostKey.PublicKey()) {
		t.Fatalf("Б не заменён: %+v", k)
	}
	if k := hostkey.Known(kh, "198.51.100.8", 22); len(k) != 2 {
		t.Fatalf("другой тип не дописан: %+v", k)
	}
	if k := hostkey.Known(kh, "198.51.100.9", 22); len(k) != 1 {
		t.Fatalf("новый не записан: %+v", k)
	}
	// Сеть серверов (Pin) записанный ключ не меняет никогда.
	hostkey.Pin(kh, "198.51.100.7", 22, b1.hkLine())
	if k := hostkey.Known(kh, "198.51.100.7", 22); len(k) != 1 || k[0].Fingerprint != fingerprint(b2.hostKey.PublicKey()) {
		t.Fatalf("Pin перебил ключ: %+v", k)
	}
}
