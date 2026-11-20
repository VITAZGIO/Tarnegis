package vault

// Одно хранилище паролей на всю сеть устройств (ТЗ 33).
//
// Хранилище узнаётся по соли файла (id — начало её sha256). Новое
// устройство не создаёт своё, а находит хранилище у других устройств сети,
// скачивает зашифрованный файл и открывает его тем же главным паролем
// (Join). Два разных хранилища, заведённых раньше, сливаются в одно
// (Merge): записи второго переносятся в первое, а в файл первого кладётся
// его ключ, зашифрованный ключом второго (Wraps). Устройства со вторым
// хранилищем при следующей синхронизации находят этот ключ своим и сами
// переходят на первое — с его главным паролем.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"sshtunnel/internal/devnet"
)

// wrap — ключ хранилища, зашифрованный ключом слитого в него хранилища.
type wrap struct {
	Salt  string `json:"salt"` // соль слитого хранилища (base64)
	Nonce string `json:"nonce"`
	Key   string `json:"key"`
}

// peerFile — файл паролей устройства сети.
type peerFile struct {
	Name string // имя устройства для человека
	Raw  []byte
}

// kick — синхронизация в фоне после правки (тесты выключают: состояние
// пакета общее, и фоновый проход мешал бы сменять «устройства»).
var kick = func() { go Sync() }

// fetchPeers — файлы паролей устройств сети, которые сейчас на связи
// (тесты подменяют).
var fetchPeers = func() []peerFile {
	var out []peerFile
	for _, p := range devnet.Status().Peers {
		if p.Self || !p.Online || !p.IP.IsValid() {
			continue
		}
		resp, err := peerClient.Get("http://" + net.JoinHostPort(p.IP.String(), "47841") + "/vault")
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		name := p.Name
		if name == "" {
			name = p.Host
		}
		out = append(out, peerFile{Name: name, Raw: raw})
	}
	return out
}

// vaultID — id хранилища для страницы: соль не показываем.
func vaultID(s []byte) string {
	h := sha256.Sum256(append([]byte("ssh_tunnel vault v1\x00"), s...))
	return hex.EncodeToString(h[:6])
}

// unwrapFor — ключ хранилища, чей файл (n, d) несёт ws, если в него слили
// наше хранилище (наш ключ k): прямо или цепочкой (наше слили во второе,
// второе — в это). nil — не наше.
func unwrapFor(ws []wrap, k, n, d []byte) []byte {
	if k == nil {
		return nil
	}
	known := [][]byte{k}
	used := make([]bool, len(ws))
	for grew := true; grew; {
		grew = false
		for i, w := range ws {
			if used[i] {
				continue
			}
			wn, e1 := base64.StdEncoding.DecodeString(w.Nonce)
			wd, e2 := base64.StdEncoding.DecodeString(w.Key)
			if e1 != nil || e2 != nil {
				used[i] = true
				continue
			}
			for _, kk := range known {
				nk, err := open(kk, wn, wd)
				if err != nil || len(nk) != 32 {
					continue
				}
				used[i], grew = true, true
				if _, err := open(nk, n, d); err == nil {
					return nk
				}
				known = append(known, nk)
				break
			}
		}
	}
	return nil
}

// unionWraps — a и то из b, чего в a нет (по соли).
func unionWraps(a, b []wrap) []wrap {
	have := map[string]bool{}
	out := append([]wrap(nil), a...)
	for _, w := range a {
		have[w.Salt] = true
	}
	for _, w := range b {
		if !have[w.Salt] {
			have[w.Salt] = true
			out = append(out, w)
		}
	}
	return out
}

// NetVault — хранилище, найденное в сети.
type NetVault struct {
	ID      string   `json:"id"`
	Devices []string `json:"devices"`
	// Mine — то же, что открыто здесь (открывается нашим ключом или уже
	// слито в наше). Есть только при открытом хранилище.
	Mine bool `json:"mine,omitempty"`
}

// Network — хранилища паролей на устройствах сети, по одному на соль.
// При открытом хранилище отмечает свои (Mine); чужие — повод слить.
func Network() []NetVault {
	files := fetchPeers()
	mu.Lock()
	k0, s0, pass := key, salt, master
	absorbed := map[string]bool{} // слитые в наше: их устройства перейдут сами
	for _, w := range wraps {
		absorbed[w.Salt] = true
	}
	mu.Unlock()
	byID := map[string]*NetVault{}
	var order []string
	for _, pf := range files {
		f, s, n, d, err := readFile(pf.Raw)
		if err != nil {
			continue
		}
		id := vaultID(s)
		v := byID[id]
		if v == nil {
			v = &NetVault{ID: id, Devices: []string{}}
			byID[id] = v
			order = append(order, id)
			if k0 != nil {
				v.Mine = string(s) == string(s0) || absorbed[base64.StdEncoding.EncodeToString(s)] ||
					opens(s, n, d, pass) || unwrapFor(f.Wraps, k0, n, d) != nil
			}
		}
		v.Devices = append(v.Devices, pf.Name)
	}
	out := []NetVault{}
	for _, id := range order {
		sort.Strings(byID[id].Devices)
		out = append(out, *byID[id])
	}
	return out
}

// opens — открывается ли файл паролем pass (ключ запоминается).
func opens(s, n, d []byte, pass string) bool {
	if pass == "" {
		return false
	}
	mu.Lock()
	k := keys[string(s)]
	mu.Unlock()
	if k == nil {
		var err error
		if k, err = derive(pass, s); err != nil {
			return false
		}
	}
	if _, err := open(k, n, d); err != nil {
		return false
	}
	mu.Lock()
	if key != nil {
		keys[string(s)] = k
	}
	mu.Unlock()
	return true
}

// findFile — файл хранилища id и ключ к нему от pass.
func findFile(id, pass string) (peerFile, []byte, error) {
	found := false
	for _, pf := range fetchPeers() {
		_, s, n, d, err := readFile(pf.Raw)
		if err != nil || vaultID(s) != id {
			continue
		}
		found = true
		k, err := derive(pass, s)
		if err != nil {
			return pf, nil, err
		}
		if _, err := open(k, n, d); err == nil {
			return pf, k, nil
		}
	}
	if !found {
		return peerFile{}, nil, errors.New("устройств с этим хранилищем сейчас нет в сети")
	}
	return peerFile{}, nil, errors.New("неверный главный пароль")
}

// Join — открыть здесь хранилище id с другого устройства сети: скачать его
// зашифрованный файл и открыть главным паролем. Только когда здесь своего
// хранилища ещё нет.
func Join(id, pass string) error {
	if exists() {
		return errors.New("здесь уже есть хранилище — его можно слить с хранилищем сети")
	}
	pf, _, err := findFile(id, pass)
	if err != nil {
		return err
	}
	mu.Lock()
	if exists() {
		mu.Unlock()
		return errors.New("здесь уже есть хранилище")
	}
	err = writeRaw(pf.Raw)
	mu.Unlock()
	if err != nil {
		return err
	}
	return Unlock(pass)
}

// writeRaw кладёт файл паролей как есть; под mu.
func writeRaw(raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path()), 0o700); err != nil {
		return err
	}
	tmp := path() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path())
}

// Merge — слить в открытое здесь хранилище другое хранилище сети id
// (otherPass — его главный пароль). Остаётся главный пароль открытого.
// Устройства с тем хранилищем перейдут на это сами (Wraps).
func Merge(id, otherPass string) (int, error) {
	mu.Lock()
	open0 := key != nil
	mu.Unlock()
	if !open0 {
		return 0, errors.New("хранилище закрыто")
	}
	pf, k, err := findFile(id, otherPass)
	if err != nil {
		if err.Error() == "неверный главный пароль" {
			err = errors.New("неверный главный пароль того хранилища")
		}
		return 0, err
	}
	f, s, n, d, err := readFile(pf.Raw)
	if err != nil {
		return 0, err
	}
	plain, err := open(k, n, d)
	if err != nil {
		return 0, err
	}
	var theirs []Entry
	if err := json.Unmarshal(plain, &theirs); err != nil {
		return 0, errors.New("файл паролей того устройства повреждён")
	}
	mu.Lock()
	defer mu.Unlock()
	if key == nil {
		return 0, errors.New("хранилище закрыто")
	}
	if string(s) == string(salt) {
		return 0, errors.New("это и есть хранилище, открытое здесь")
	}
	c := mergeEntries(theirs)
	wn, wd, err := seal(k, key)
	if err != nil {
		return 0, err
	}
	sb := base64.StdEncoding.EncodeToString(s)
	nw := []wrap{{Salt: sb, Nonce: base64.StdEncoding.EncodeToString(wn), Key: base64.StdEncoding.EncodeToString(wd)}}
	for _, w := range wraps {
		if w.Salt != sb {
			nw = append(nw, w)
		}
	}
	// Хранилища, слитые раньше в то, переходят дальше — в наше: их ключ
	// раскрывает ключ того, а тот файл — наш ключ.
	wraps = unionWraps(nw, f.Wraps)
	keys[string(s)] = k
	if err := save(); err != nil {
		return 0, err
	}
	kick()
	return c, nil
}
