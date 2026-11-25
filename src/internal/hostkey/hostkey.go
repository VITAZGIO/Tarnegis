// Package hostkey — проверка ключа SSH-сервера вместо InsecureIgnoreHostKey.
//
// Используется схема TOFU (trust on first use), та же, что у обычного ssh:
// первый увиденный ключ сервера запоминается в known_hosts, дальше сверяется.
// Это защищает от подмены сервера по дороге: кто-то в сети между клиентом и
// сервером может попытаться встать посередине, и без этой проверки
// подмена прошла бы незаметно — клиент подключился бы к чужому серверу и отдал
// ему весь трафик.
//
// Сменившийся ключ молча не принимается никогда. Принять его можно двумя
// путями (ТЗ 46): человек подтвердил в интернете («Я переустановил сервер»,
// Accept), или тот же новый ключ сообщили ДРУГИЕ серверы сети устройств
// (witness.go) — источник, независимый от соединения с самим сервером.
package hostkey

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// KeyInfo — тип и отпечаток ключа для экрана.
type KeyInfo struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
}

// ErrChanged возвращается, когда ключ сервера отличается от запомненного.
// Это либо переустановка сервера, либо попытка подмены — молча принимать
// такое нельзя, поэтому решение остаётся за человеком. Единственный сигнал
// о смене ключа: экран берёт поля отсюда, а не разбирает текст.
type ErrChanged struct {
	// Host — адрес, как к нему подключались ("host:port").
	Host string
	// Names — все имена known_hosts, под которыми этот сервер записан
	// (адрес подключения и, если отличается, фактический IP): «принять»
	// заменяет строки для всех, иначе старый ключ под вторым именем
	// снова дал бы «ключ изменился».
	Names       []string
	KnownHosts  string
	Fingerprint string // новый ключ, SHA256:…
	KeyType     string // тип нового ключа (ssh-ed25519, ecdsa-sha2-nistp256, …)
	Key         string // новый ключ "тип base64" — его и записывает Accept
	Old         []KeyInfo
}

func (e *ErrChanged) Error() string {
	old := ""
	if len(e.Old) > 0 {
		old = ", был " + e.Old[0].Fingerprint
	}
	return fmt.Sprintf(
		"ключ сервера %s изменился (сейчас %s%s). Если ты переустановил сервер — прими новый ключ "+
			"в программе; если нет — соединение могут перехватывать, подключаться нельзя.",
		e.Host, e.Fingerprint, old)
}

// Callback возвращает HostKeyCallback, который сверяет ключ с known_hosts,
// а при первом подключении запоминает его. onLearn (может быть nil)
// вызывается, когда ключ запоминается впервые — чтобы показать это в интерфейсе.
func Callback(knownHostsPath string, onLearn func(host, fingerprint string)) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)

		if _, err := os.Stat(knownHostsPath); err == nil {
			check, err := knownhosts.New(knownHostsPath)
			if err != nil {
				return fmt.Errorf("не могу прочитать %s: %w", knownHostsPath, err)
			}
			err = check(hostname, remote, key)
			if err == nil {
				clearPending(hostname)
				return nil
			}
			var kerr *knownhosts.KeyError
			// KeyError с непустым Want означает "хост known, но ключ другой" —
			// это тревожный случай. Пустой Want — хост просто ещё не записан.
			if ok := asKeyError(err, &kerr); ok && len(kerr.Want) == 0 {
				return learn(knownHostsPath, hostname, remote, key, fp, onLearn)
			}
			if ok := asKeyError(err, &kerr); ok {
				old := make([]ssh.PublicKey, 0, len(kerr.Want))
				for _, w := range kerr.Want {
					old = append(old, w.Key)
				}
				return changed(knownHostsPath, hostname, namesFor(hostname, remote), key, old)
			}
			return err
		}

		return learn(knownHostsPath, hostname, remote, key, fp, onLearn)
	}
}

// changed — ключ не совпал. Если новый ключ подтвердила сеть серверов —
// запоминается он, и подключение идёт дальше; иначе ErrChanged, который
// заодно ложится в список ожидающих решения человека (Pending).
func changed(path, hostname string, names []string, key ssh.PublicKey, old []ssh.PublicKey) error {
	if vouched(hostname, key) {
		if err := Replace(path, names, key); err == nil {
			clearPending(hostname)
			logVouched(hostname, ssh.FingerprintSHA256(key))
			return nil
		}
	}
	e := &ErrChanged{
		Host:        hostname,
		Names:       names,
		KnownHosts:  path,
		Fingerprint: ssh.FingerprintSHA256(key),
		KeyType:     key.Type(),
		Key:         strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
	}
	seen := map[string]bool{}
	for _, k := range old {
		fp := ssh.FingerprintSHA256(k)
		if !seen[fp] {
			seen[fp] = true
			e.Old = append(e.Old, KeyInfo{Type: k.Type(), Fingerprint: fp})
		}
	}
	addPending(e)
	return e
}

// namesFor — имена known_hosts для подключения: адрес, как его набрали, и
// фактический адрес соединения, если он другой (так их записывает learn).
func namesFor(hostname string, remote net.Addr) []string {
	names := []string{knownhosts.Normalize(hostname)}
	if remote != nil {
		if tcp, ok := remote.(*net.TCPAddr); ok && tcp.IP.IsUnspecified() {
			return names // канал внутри SSH (цепочка выхода): адреса нет
		}
		if n := knownhosts.Normalize(remote.String()); n != names[0] {
			names = append(names, n)
		}
	}
	return names
}

func learn(path, hostname string, remote net.Addr, key ssh.PublicKey, fp string, onLearn func(string, string)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	// Записываем и имя, под которым подключались, и фактический адрес —
	// иначе при подключении по другому имени ключ снова будет "неизвестным".
	addrs := []string{knownhosts.Normalize(hostname)}
	if remote != nil {
		if n := knownhosts.Normalize(remote.String()); n != addrs[0] {
			addrs = append(addrs, n)
		}
	}
	if _, err := f.WriteString(knownhosts.Line(addrs, key) + "\n"); err != nil {
		return err
	}
	if onLearn != nil {
		onLearn(hostname, fp)
	}
	return nil
}

func asKeyError(err error, target **knownhosts.KeyError) bool {
	ke, ok := err.(*knownhosts.KeyError)
	if ok {
		*target = ke
	}
	return ok
}
