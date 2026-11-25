package hostkey

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func keyLine(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

func hashedName(t *testing.T, name string) string {
	t.Helper()
	salt := make([]byte, 20)
	rand.Read(salt)
	m := hmac.New(sha1.New, salt)
	m.Write([]byte(name))
	return "|1|" + base64.StdEncoding.EncodeToString(salt) + "|" + base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// Замена убирает ключи ВСЕХ типов для имён сервера (в том числе из строк с
// несколькими именами и хэшированные), а чужие строки, другой порт того же
// адреса, комментарии — не трогает.
func TestReplaceТолькоСвоиСтроки(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	oldEd, oldEc, other, port2222, newKey := testKey(t), testKey(t), testKey(t), testKey(t), testKey(t)
	content := "# мой комментарий\n" +
		"srv.example,203.0.113.5 " + keyLine(oldEd) + "\n" +
		"srv.example " + keyLine(oldEc) + "\n" +
		hashedName(t, "srv.example") + " " + keyLine(oldEd) + "\n" +
		"[srv.example]:2222 " + keyLine(port2222) + "\n" +
		"other.example,203.0.113.5 " + keyLine(other) + "\n" +
		"@revoked * " + keyLine(other) + "\n"
	os.WriteFile(path, []byte(content), 0o600)

	if err := Replace(path, []string{"srv.example", "203.0.113.5"}, newKey); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "# мой комментарий\n" +
		"[srv.example]:2222 " + keyLine(port2222) + "\n" +
		"other.example " + keyLine(other) + "\n" +
		"@revoked * " + keyLine(other) + "\n" +
		"srv.example,203.0.113.5 " + keyLine(newKey) + "\n"
	if string(got) != want {
		t.Fatalf("known_hosts:\n%s\nждал:\n%s", got, want)
	}
	if k := Known(path, "srv.example", 22); len(k) != 1 || k[0].Fingerprint != ssh.FingerprintSHA256(newKey) {
		t.Errorf("Known: %+v", k)
	}
	if k := Known(path, "srv.example", 2222); len(k) != 1 || k[0].Fingerprint != ssh.FingerprintSHA256(port2222) {
		t.Errorf("Known :2222: %+v", k)
	}
}

func TestReplaceФормаСПортом(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	a, b, n := testKey(t), testKey(t), testKey(t)
	os.WriteFile(path, []byte("[h.example]:2222 "+keyLine(a)+"\nh.example "+keyLine(b)+"\n"), 0o600)
	if err := ReplaceLine(path, []string{HostName("h.example", 2222)}, keyLine(n)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "h.example " + keyLine(b) + "\n[h.example]:2222 " + keyLine(n) + "\n"
	if string(got) != want {
		t.Fatalf("known_hosts:\n%s\nждал:\n%s", got, want)
	}
}

// «Забыть» убирает и IP, записанный в одной строке с именем, — иначе
// следующее подключение снова сказало бы «ключ изменился».
func TestForgetВместеСIP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	k, o := testKey(t), testKey(t)
	os.WriteFile(path, []byte("srv.example,203.0.113.5 "+keyLine(k)+"\nother.example "+keyLine(o)+"\n"), 0o600)
	n, err := Forget(path, Aliases(path, "srv.example"))
	if err != nil || n != 1 {
		t.Fatalf("Forget: %d, %v", n, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "other.example "+keyLine(o)+"\n" {
		t.Fatalf("known_hosts:\n%s", got)
	}
	// После «забыть» новый ключ просто запоминается.
	cb := Callback(path, nil)
	ra, _ := net.ResolveTCPAddr("tcp", "203.0.113.5:22")
	if err := cb("srv.example:22", ra, testKey(t)); err != nil {
		t.Fatalf("после «забыть» подключение должно проходить: %v", err)
	}
}

func TestVouchesРешение(t *testing.T) {
	k, other := testKey(t), testKey(t)
	line := keyLine(k)
	cases := []struct {
		name string
		list []Witness
		want bool
	}{
		{"нет данных", nil, false},
		{"сосед подтвердил", []Witness{{Host: "s.example", Port: 22, Key: line, Via: "n.example:22"}}, true},
		{"порт по умолчанию", []Witness{{Host: "s.example", Key: line + " коммент", Via: "n.example:22"}}, true},
		{"только сам сервер", []Witness{{Host: "s.example", Port: 22, Key: line, Via: "s.example:22"}}, false},
		{"источник не указан", []Witness{{Host: "s.example", Port: 22, Key: line}}, false},
		{"сосед знает другой ключ", []Witness{{Host: "s.example", Port: 22, Key: keyLine(other), Via: "n.example:22"}}, false},
		{"соседи разошлись", []Witness{
			{Host: "s.example", Port: 22, Key: line, Via: "n.example:22"},
			{Host: "s.example", Port: 22, Key: keyLine(other), Via: "m.example:22"}}, false},
		{"другой порт", []Witness{{Host: "s.example", Port: 2222, Key: line, Via: "n.example:22"}}, false},
	}
	for _, c := range cases {
		if got := Vouches(c.list, "s.example:22", k); got != c.want {
			t.Errorf("%s: %v, ждал %v", c.name, got, c.want)
		}
	}
}

// Сеть серверов подтвердила новый ключ — он принимается сам и записывается,
// с одной строкой в журнал. Не подтвердила — ErrChanged с полями для экрана,
// и смена ждёт решения человека.
func TestCallbackСетьПодтвердила(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	cb := Callback(path, nil)
	if err := cb("87.58.210.143:22", addr(t), testKey(t)); err != nil {
		t.Fatal(err)
	}
	newKey := testKey(t)
	var logged string
	SetVouchLog(func(h, fp string) { logged = h + " " + fp })
	SetWitnesses([]Witness{{Host: "87.58.210.143", Port: 22, Key: keyLine(newKey), Via: "198.51.100.7:22"}})
	t.Cleanup(func() { SetWitnesses(nil); SetVouchLog(nil) })

	if err := cb("87.58.210.143:22", addr(t), newKey); err != nil {
		t.Fatalf("подтверждённый сетью ключ не принят: %v", err)
	}
	if !strings.Contains(logged, ssh.FingerprintSHA256(newKey)) {
		t.Errorf("нет строки в журнал: %q", logged)
	}
	SetWitnesses(nil)
	if err := cb("87.58.210.143:22", addr(t), newKey); err != nil {
		t.Fatalf("новый ключ не записался: %v", err)
	}
}

func TestCallbackПринятьИОтмена(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	cb := Callback(path, nil)
	oldKey := testKey(t)
	if err := cb("87.58.210.143:22", addr(t), oldKey); err != nil {
		t.Fatal(err)
	}
	newKey := testKey(t)
	err := cb("87.58.210.143:22", addr(t), newKey)
	var ch *ErrChanged
	if !errors.As(err, &ch) {
		t.Fatalf("ждал ErrChanged, получено %v", err)
	}
	if ch.Fingerprint != ssh.FingerprintSHA256(newKey) || ch.KeyType != "ssh-ed25519" ||
		len(ch.Old) != 1 || ch.Old[0].Fingerprint != ssh.FingerprintSHA256(oldKey) {
		t.Fatalf("поля ErrChanged: %+v", ch)
	}
	if strings.Contains(err.Error(), path) {
		t.Error("в тексте ошибки всё ещё путь к файлу")
	}

	// Экран видит смену через API.
	res, _ := HandleAPI(path, "GET", nil, nil)
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), ssh.FingerprintSHA256(newKey)) || !strings.Contains(string(b), "ssh_host_ed25519_key.pub") {
		t.Fatalf("GET /api/hostkey: %s", b)
	}

	// Отмена — файл не меняется, старый ключ на месте.
	before, _ := os.ReadFile(path)
	if _, err := HandleAPI(path, "POST", nil, []byte(`{"op":"dismiss","host":"87.58.210.143:22"}`)); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || PendingFor("87.58.210.143", 22) != nil {
		t.Fatal("«Отмена» что-то поменяла")
	}

	// Снова увидели смену; чужой отпечаток не принимается.
	cb("87.58.210.143:22", addr(t), newKey)
	if _, err := HandleAPI(path, "POST", nil, []byte(`{"op":"accept","host":"87.58.210.143:22","fingerprint":"SHA256:чужой"}`)); err == nil {
		t.Fatal("принят ключ с чужим отпечатком")
	}
	body, _ := json.Marshal(map[string]string{"op": "accept", "host": "87.58.210.143:22", "fingerprint": ssh.FingerprintSHA256(newKey)})
	if _, err := HandleAPI(path, "POST", nil, body); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := cb("87.58.210.143:22", addr(t), newKey); err != nil {
		t.Fatalf("после «принять» подключение не проходит: %v", err)
	}
	if err := cb("87.58.210.143:22", addr(t), oldKey); err == nil {
		t.Fatal("старый ключ всё ещё принимается")
	}
}

func TestCheckCommand(t *testing.T) {
	for typ, want := range map[string]string{
		"ssh-ed25519": "ed25519", "ecdsa-sha2-nistp256": "ecdsa", "ssh-rsa": "rsa", "rsa-sha2-512": "rsa",
	} {
		if got := CheckCommand(typ); got != "ssh-keygen -lf /etc/ssh/ssh_host_"+want+"_key.pub" {
			t.Errorf("%s: %s", typ, got)
		}
	}
}
