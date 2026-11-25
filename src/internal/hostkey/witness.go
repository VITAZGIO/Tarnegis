package hostkey

// Подтверждение ключа сетью серверов (ТЗ 46, п. 2) и список смен ключа,
// которые ждут решения человека.
//
// Серверы сети устройств рассказывают устройству о других серверах сети,
// вместе с их SSH-ключом (mesh.NetServer.HostKey, «hk»). Этот ключ сервер
// сети получил своим путём — по своему SSH-соединению с соседом, — поэтому
// он независим от того, что сейчас показывает соединение с самим сервером.
// Если сосед сообщил ровно тот новый ключ, который сервер показывает, —
// это переустановка, а не подмена посередине: принимаем без вопроса и
// пишем одну строку в журнал. Сам сервер про себя свидетелем не считается
// никогда (Via — соединение, через которое пришёл список).

import (
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Witness — что сеть серверов сообщила о сервере Host:Port.
type Witness struct {
	Host string
	Port int
	Key  string // "тип base64"
	// Via — "адрес:порт" сервера, от которого пришло сообщение.
	Via string
}

var (
	witMu     sync.Mutex
	witnesses []Witness
	vouchLog  func(host, fingerprint string)

	pendMu  sync.Mutex
	pending = map[string]*ErrChanged{} // по knownhosts.Normalize(Host)
)

// SetWitnesses заменяет то, что сейчас известно от сети серверов (зовётся на
// каждый новый список сети, ПК — app, телефон — android/core/mobile).
func SetWitnesses(list []Witness) {
	witMu.Lock()
	witnesses = append([]Witness(nil), list...)
	witMu.Unlock()
}

// SetVouchLog — куда писать строку «ключ обновлён по данным сети серверов».
func SetVouchLog(f func(host, fingerprint string)) {
	witMu.Lock()
	vouchLog = f
	witMu.Unlock()
}

func logVouched(host, fp string) {
	witMu.Lock()
	f := vouchLog
	witMu.Unlock()
	if f != nil {
		f(host, fp)
	}
}

// vouched — подтвердил ли кто-то, кроме самого сервера hostname, что его
// ключ теперь key. Нет данных или ключи разные — нет.
func vouched(hostname string, key ssh.PublicKey) bool {
	witMu.Lock()
	list := witnesses
	witMu.Unlock()
	return Vouches(list, hostname, key)
}

// Vouches — решение по списку (отдельно, чтобы проверить тестом).
func Vouches(list []Witness, hostname string, key ssh.PublicKey) bool {
	want := knownhosts.Normalize(hostname)
	keyLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	ok := false
	for _, w := range list {
		if w.Host == "" || HostName(w.Host, w.Port) != want {
			continue
		}
		if w.Via == "" || knownhosts.Normalize(w.Via) == want {
			continue // сам о себе — не свидетель
		}
		f := strings.Fields(w.Key)
		if len(f) < 2 {
			continue
		}
		if f[0]+" "+f[1] != keyLine {
			// Хоть один сосед знает другой ключ — решать человеку.
			return false
		}
		ok = true
	}
	return ok
}

func addPending(e *ErrChanged) {
	pendMu.Lock()
	pending[knownhosts.Normalize(e.Host)] = e
	pendMu.Unlock()
}

func clearPending(hostname string) {
	pendMu.Lock()
	delete(pending, knownhosts.Normalize(hostname))
	pendMu.Unlock()
}

// Pending — смены ключа, увиденные с запуска программы и ещё не решённые.
func Pending() []*ErrChanged {
	pendMu.Lock()
	defer pendMu.Unlock()
	out := make([]*ErrChanged, 0, len(pending))
	for _, e := range pending {
		out = append(out, e)
	}
	return out
}

// PendingFor — ожидающая смена ключа сервера host:port или nil.
func PendingFor(host string, port int) *ErrChanged {
	pendMu.Lock()
	defer pendMu.Unlock()
	return pending[HostName(host, port)]
}

// Accept — человек подтвердил новый ключ сервера hostport ("host:port").
// fingerprint обязан совпасть с тем, что сервер показал (и что было на
// экране): принимается только увиденный ключ, не присланный снаружи.
func Accept(hostport, fingerprint string) (*ErrChanged, error) {
	pendMu.Lock()
	e := pending[knownhosts.Normalize(hostport)]
	pendMu.Unlock()
	if e == nil {
		return nil, errNoPending
	}
	if strings.TrimSpace(fingerprint) != e.Fingerprint {
		return nil, errFpMismatch
	}
	if err := ReplaceLine(e.KnownHosts, e.Names, e.Key); err != nil {
		return nil, err
	}
	clearPending(e.Host)
	return e, nil
}

// Dismiss — «Отмена»: убрать из ожидающих, файл не трогать.
func Dismiss(hostport string) { clearPending(hostport) }

type hkError string

func (e hkError) Error() string { return string(e) }

const (
	errNoPending  = hkError("смены ключа этого сервера нет — подключись ещё раз")
	errFpMismatch = hkError("отпечаток не совпадает с тем, что показал сервер — подключись ещё раз")
)

// CheckCommand — команда, которой на самом сервере (консоль хостера)
// смотрят отпечаток ключа нужного типа.
func CheckCommand(keyType string) string {
	t := "ed25519"
	switch {
	case strings.HasPrefix(keyType, "ecdsa"):
		t = "ecdsa"
	case strings.HasPrefix(keyType, "ssh-rsa"), strings.HasPrefix(keyType, "rsa-"):
		t = "rsa"
	case strings.HasPrefix(keyType, "ssh-dss"):
		t = "dsa"
	}
	return "ssh-keygen -lf /etc/ssh/ssh_host_" + t + "_key.pub"
}

// splitHostPort — "host:port" → host, port (порт по умолчанию 22).
func splitHostPort(hp string) (string, int) {
	hp = strings.TrimSpace(hp)
	if strings.HasPrefix(hp, "[") {
		if i := strings.Index(hp, "]:"); i > 0 {
			p, _ := strconv.Atoi(hp[i+2:])
			return hp[1:i], p
		}
		return strings.Trim(hp, "[]"), 22
	}
	if strings.Count(hp, ":") == 1 {
		h, p, _ := strings.Cut(hp, ":")
		n, _ := strconv.Atoi(p)
		return h, n
	}
	return hp, 22
}
