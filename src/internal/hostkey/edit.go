package hostkey

// Правка known_hosts из программы (ТЗ 46): «принять новый ключ» и «забыть
// ключ» без ручной правки файла — на телефоне до него вообще не добраться.

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostName — имя сервера в known_hosts: "host" для порта 22, иначе
// "[host]:port" (как knownhosts.Normalize).
func HostName(host string, port int) string {
	if port <= 0 {
		port = 22
	}
	return knownhosts.Normalize(host + ":" + strconv.Itoa(port))
}

// Replace убирает из known_hosts все ключи (любых типов) для имён names и
// записывает вместо них один key под этими именами. Строки других серверов
// не трогаются; строка с несколькими именами теряет только наши.
func Replace(path string, names []string, key ssh.PublicKey) error {
	names = normNames(names)
	if len(names) == 0 {
		return errors.New("не указан сервер")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	out, _ := dropNames(data, names)
	out = append(out, []byte(knownhosts.Line(names, key)+"\n")...)
	return writeAtomic(path, out)
}

// ReplaceLine — Replace для ключа строкой "тип base64".
func ReplaceLine(path string, names []string, keyLine string) error {
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(keyLine))
	if err != nil {
		return err
	}
	return Replace(path, names, pk)
}

// Forget убирает все ключи для имён names. removed — сколько строк ушло
// или потеряло имя. Следующее подключение запомнит ключ заново (TOFU).
func Forget(path string, names []string) (removed int, err error) {
	names = normNames(names)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	out, removed := dropNames(data, names)
	if removed == 0 {
		return 0, nil
	}
	return removed, writeAtomic(path, out)
}

// Aliases — name и все имена, записанные с ним в одной строке (learn
// пишет «адрес,IP»): «забыть» должно убрать и IP, иначе старый ключ под ним
// снова дал бы «ключ изменился».
func Aliases(path, name string) []string {
	out := []string{name}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	seen := map[string]bool{name: true}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || line[0] == '@' || strings.HasPrefix(line, "|1|") {
			continue
		}
		hosts, _, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		list := strings.Split(hosts, ",")
		has := false
		for _, h := range list {
			if h == name {
				has = true
			}
		}
		if !has {
			continue
		}
		for _, h := range list {
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	return out
}

// Known — ключи, записанные для host:port (простые и хэшированные имена).
func Known(path, host string, port int) []KeyInfo {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	want := HostName(host, port)
	var out []KeyInfo
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == '@' {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 || !hostsMatch(f[0], want) {
			continue
		}
		pk, _, _, _, perr := ssh.ParseAuthorizedKey([]byte(f[1] + " " + f[2]))
		if perr != nil {
			continue
		}
		fp := ssh.FingerprintSHA256(pk)
		if !seen[fp] {
			seen[fp] = true
			out = append(out, KeyInfo{Type: pk.Type(), Fingerprint: fp})
		}
	}
	return out
}

func normNames(names []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !strings.HasPrefix(n, "[") && strings.Contains(n, ":") && strings.Count(n, ":") == 1 {
			n = knownhosts.Normalize(n)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// dropNames — содержимое known_hosts без имён names. Хэшированная строка,
// совпавшая с любым из имён, уходит целиком (её имена не разобрать);
// обычная — теряет только совпавшие имена и уходит, если имён не осталось.
// Комментарии и пометки (@revoked, @cert-authority) остаются как были.
func dropNames(data []byte, names []string) ([]byte, int) {
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	var b bytes.Buffer
	removed := 0
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || line[0] == '@' {
			b.WriteString(raw + "\n")
			continue
		}
		hosts, rest, ok := strings.Cut(line, " ")
		if !ok {
			b.WriteString(raw + "\n")
			continue
		}
		if strings.HasPrefix(hosts, "|1|") {
			hit := false
			for _, n := range names {
				if hostsMatch(hosts, n) {
					hit = true
					break
				}
			}
			if hit {
				removed++
				continue
			}
			b.WriteString(raw + "\n")
			continue
		}
		var keep []string
		for _, h := range strings.Split(hosts, ",") {
			if !drop[h] {
				keep = append(keep, h)
			}
		}
		switch {
		case len(keep) == len(strings.Split(hosts, ",")):
			b.WriteString(raw + "\n")
		case len(keep) == 0:
			removed++
		default:
			removed++
			b.WriteString(strings.Join(keep, ",") + " " + strings.TrimLeft(rest, " ") + "\n")
		}
	}
	return b.Bytes(), removed
}

// writeAtomic — через временный файл рядом: оборванная запись не оставит
// пустой known_hosts (а с ним — «первое подключение» ко всем серверам).
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
