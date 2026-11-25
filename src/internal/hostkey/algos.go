package hostkey

import (
	"bufio"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Algorithms — алгоритмы ключа сервера, которые просить у addr ("host:port"),
// чтобы сервер показал ключ того типа, что уже записан в known_hosts.
//
// Без этого Go-клиент просит сначала ECDSA, потом RSA и только потом
// ed25519. Если в known_hosts записан только ed25519 (так его заносит сеть
// серверов, Pin), сервер отдаёт ECDSA, knownhosts видит «хост известен, ключ
// другой» и программа кричит о подмене, хотя сервер тот же. OpenSSH делает
// так же: просит в первую очередь записанные типы.
//
// nil — про этот адрес ничего не записано, порядок по умолчанию.
func Algorithms(path, addr string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	want := knownhosts.Normalize(addr)
	host, _, _ := net.SplitHostPort(addr)
	seen := map[string]bool{}
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], "@") {
			continue
		}
		match := false
		for _, h := range strings.Split(f[0], ",") {
			if h == want || (host != "" && h == host && want == host) {
				match = true
				break
			}
		}
		if !match || seen[f[1]] {
			continue
		}
		seen[f[1]] = true
		out = append(out, algosFor(f[1])...)
	}
	return out
}

func algosFor(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		// Ключ RSA подписывают современными SHA-2, ssh-rsa (SHA-1) — запасной.
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}
