package hostkey

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Pin записывает SSH-ключ сервера ("тип ключ") в known_hosts, если про этот
// адрес там ещё ничего нет. Ключ пришёл от сервера сети по SSH (сеть
// серверов, ТЗ 22) — это надёжнее, чем поверить первому соединению. Уже
// записанный ключ не трогает: сверку и смену решает Callback.
// Общий для ПК (internal/app) и телефона (android/core/mobile).
func Pin(path, host string, port int, key string) {
	f := strings.Fields(key)
	if path == "" || host == "" || len(f) < 2 {
		return
	}
	name := host
	if port != 22 && port > 0 {
		name = "[" + host + "]:" + strconv.Itoa(port)
	}
	data, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(data), "\n") {
		if hosts, _, ok := strings.Cut(strings.TrimSpace(l), " "); ok {
			for _, h := range strings.Split(hosts, ",") {
				if h == name {
					return
				}
			}
		}
	}
	line := name + " " + f[0] + " " + f[1] + "\n"
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		line = "\n" + line
	}
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	fh.WriteString(line)
	fh.Close()
}

// Trust записывает ключ из доверенного источника (ТЗ 50): приглашение или
// профиль устройства, прочитанный по проверенному SSH. В отличие от Pin он
// чинит устаревшее: записан ключ того же типа, но другой (сервер
// переустановили) — заменяется; записаны ключи только других типов —
// дописывается рядом (тот же сервер показывает ключи нескольких типов).
// Тот же ключ уже есть — ничего. Ключи, пришедшие от сети серверов (Pin),
// так никогда не пишутся.
func Trust(path, host string, port int, key string) {
	f := strings.Fields(key)
	if path == "" || host == "" || len(f) < 2 {
		return
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(f[0] + " " + f[1]))
	if err != nil {
		return
	}
	known := Known(path, host, port)
	if len(known) == 0 {
		Pin(path, host, port, key)
		return
	}
	fp := ssh.FingerprintSHA256(pk)
	sameType := false
	for _, k := range known {
		if k.Fingerprint == fp {
			return
		}
		if k.Type == pk.Type() {
			sameType = true
		}
	}
	name := HostName(host, port)
	if sameType {
		_ = Replace(path, []string{name}, pk)
		return
	}
	data, _ := os.ReadFile(path)
	line := knownhosts.Line([]string{name}, pk) + "\n"
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		line = "\n" + line
	}
	_ = writeAtomic(path, append(data, line...))
}
