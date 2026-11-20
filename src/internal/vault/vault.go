// Package vault — плагин «Пароли»: свои пароли под одним главным паролем,
// на всех своих устройствах, с подстановкой на телефоне (автозаполнение
// Android) и в браузере на ПК (расширение, ext.go).
//
// Хранилище — vault.json: соль и записи, зашифрованные AES-256-GCM ключом
// из главного пароля (scrypt). Главный пароль нигде не хранится; ключ живёт
// в памяти, пока хранилище открыто. Синхронизация — по сети устройств
// (порт Port): устройство отдаёт свой зашифрованный файл, другое
// расшифровывает тем же главным паролем и сливает записи по времени правки
// (удаление — запись с del, чтобы оно тоже доехало).
package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"

	"sshtunnel/internal/addons"
	"sshtunnel/internal/devnet"
	"sshtunnel/internal/mesh"
)

// ID — плагин в каталоге.
const ID = "vault"

// Port — порт синхронизации в сети устройств.
const Port = 47841

func init() {
	addons.Register(ID, func() addons.Module { return &module{} })
	addons.RegisterRoutes(ID, addons.Route{
		Path: "/api/vault", Methods: "GET, POST",
		Desc: "Пароли: GET — состояние (есть ли хранилище, открыто ли) и записи без паролей; POST {\"op\":\"create|unlock|lock|get|set|delete|gen|sync|import|export|extkey|network|join|merge\",…}. Пароль отдаёт только get {id} при открытом хранилище. network — хранилища на устройствах сети [{id,devices,mine}]; join {vault,master} — открыть здесь хранилище сети (когда своего нет); merge {vault,other} — слить в открытое хранилище другое (other — его главный пароль; его устройства перейдут сами). import {csv} — CSV из Chrome/Яндекса/Firefox/Bitwarden; export {master} — CSV в «Загрузки».",
		H:    handleAPI,
	})
}

// Entry — запись.
type Entry struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Site  string `json:"site,omitempty"` // домен (example.com) или пакет приложения
	Login string `json:"login,omitempty"`
	Pass  string `json:"pass,omitempty"`
	Note  string `json:"note,omitempty"`
	Upd   int64  `json:"upd"` // мс
	Del   bool   `json:"del,omitempty"`
}

type file struct {
	Salt  string `json:"salt"`
	Nonce string `json:"nonce"`
	Data  string `json:"data"`
	// Wraps — ключ этого хранилища, зашифрованный ключами хранилищ, которые
	// в него слили (ТЗ 33, network.go): устройство со старым хранилищем
	// само переходит на это. Старые версии поле не знают и не трогают.
	Wraps []wrap `json:"wraps,omitempty"`
}

var (
	mu       sync.Mutex
	key      []byte // ключ открытого хранилища; nil — закрыто
	salt     []byte
	entries  []Entry
	keys     = map[string][]byte{} // соль чужого файла → ключ (тот же пароль)
	master   string                // нужен для ключей к чужой соли; только пока открыто
	lastSync time.Time
	wraps    []wrap // см. file.Wraps
	notice   string // что сказать человеку на странице (переход на общее хранилище)
)

func path() string { return filepath.Join(addons.DataDir(), "vault.json") }

func exists() bool { _, err := os.Stat(path()); return err == nil }

func derive(pass string, s []byte) ([]byte, error) {
	return scrypt.Key([]byte(pass), s, 1<<15, 8, 1, 32)
}

func seal(k []byte, plain []byte) (nonce, data []byte, err error) {
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, nil, err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, g.NonceSize())
	_, _ = rand.Read(nonce)
	return nonce, g.Seal(nil, nonce, plain, nil), nil
}

func open(k, nonce, data []byte) ([]byte, error) {
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return g.Open(nil, nonce, data, nil)
}

func readFile(raw []byte) (file, []byte, []byte, []byte, error) {
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, nil, nil, nil, errors.New("файл паролей повреждён")
	}
	s, e1 := base64.StdEncoding.DecodeString(f.Salt)
	n, e2 := base64.StdEncoding.DecodeString(f.Nonce)
	d, e3 := base64.StdEncoding.DecodeString(f.Data)
	if e1 != nil || e2 != nil || e3 != nil {
		return f, nil, nil, nil, errors.New("файл паролей повреждён")
	}
	return f, s, n, d, nil
}

// save — под mu.
func save() error {
	plain, _ := json.Marshal(entries)
	n, d, err := seal(key, plain)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(file{base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(n), base64.StdEncoding.EncodeToString(d), wraps})
	tmp := path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path())
}

// Create — новое хранилище с главным паролем.
func Create(pass string) error {
	if len([]rune(pass)) < 8 {
		return errors.New("главный пароль — хотя бы 8 знаков: им открываются все остальные")
	}
	mu.Lock()
	defer mu.Unlock()
	if exists() {
		return errors.New("хранилище уже есть — открой его главным паролем")
	}
	s := make([]byte, 16)
	_, _ = rand.Read(s)
	k, err := derive(pass, s)
	if err != nil {
		return err
	}
	key, salt, master, entries, wraps = k, s, pass, []Entry{}, nil
	keys[string(s)] = k
	return save()
}

// Unlock открывает хранилище главным паролем.
func Unlock(pass string) error {
	raw, err := os.ReadFile(path())
	if err != nil {
		return errors.New("хранилища ещё нет")
	}
	f, s, n, d, err := readFile(raw)
	if err != nil {
		return err
	}
	k, err := derive(pass, s)
	if err != nil {
		return err
	}
	plain, err := open(k, n, d)
	if err != nil {
		return errors.New("неверный главный пароль")
	}
	var es []Entry
	if err := json.Unmarshal(plain, &es); err != nil {
		return errors.New("файл паролей повреждён")
	}
	mu.Lock()
	key, salt, master, entries, wraps = k, s, pass, es, f.Wraps
	keys[string(s)] = k
	mu.Unlock()
	kick()
	return nil
}

// Lock закрывает: ключ и пароль забываются.
func Lock() {
	mu.Lock()
	key, master, entries, wraps, notice = nil, "", nil, nil, ""
	keys = map[string][]byte{}
	mu.Unlock()
}

// Unlocked — открыто ли хранилище.
func Unlocked() bool {
	mu.Lock()
	defer mu.Unlock()
	return key != nil
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// normSite — домен из ссылки или как написали (без www.).
func normSite(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			s = u.Hostname()
		}
	}
	s = strings.TrimPrefix(strings.SplitN(s, "/", 2)[0], "www.")
	return s
}

// Match — записи для сайта или приложения (с паролями): домен совпал или
// поддомен (login.example.com → example.com).
func Match(site string) []Entry {
	site = normSite(site)
	mu.Lock()
	defer mu.Unlock()
	var out []Entry
	if key == nil || site == "" {
		return out
	}
	for _, e := range entries {
		if e.Del || e.Site == "" {
			continue
		}
		if site == e.Site || strings.HasSuffix(site, "."+e.Site) || strings.HasSuffix(e.Site, "."+site) {
			out = append(out, e)
		}
	}
	return out
}

// Put — добавить или изменить запись (id пусто — новая).
func Put(e Entry) (Entry, error) {
	mu.Lock()
	defer mu.Unlock()
	if key == nil {
		return e, errors.New("хранилище закрыто")
	}
	e.Site = normSite(e.Site)
	if e.Title == "" {
		e.Title = e.Site
	}
	e.Upd = time.Now().UnixMilli()
	e.Del = false
	if e.ID == "" {
		e.ID = randID()
		entries = append(entries, e)
	} else {
		found := false
		for i := range entries {
			if entries[i].ID == e.ID {
				if e.Pass == "" {
					e.Pass = entries[i].Pass // правка без нового пароля — старый
				}
				entries[i], found = e, true
			}
		}
		if !found {
			entries = append(entries, e)
		}
	}
	if err := save(); err != nil {
		return e, err
	}
	kick()
	return e, nil
}

func del(id string) error {
	mu.Lock()
	defer mu.Unlock()
	if key == nil {
		return errors.New("хранилище закрыто")
	}
	for i := range entries {
		if entries[i].ID == id {
			entries[i] = Entry{ID: id, Del: true, Upd: time.Now().UnixMilli()}
		}
	}
	if err := save(); err != nil {
		return err
	}
	kick()
	return nil
}

// Gen — случайный пароль: буквы, цифры и знаки, без похожих (l/1/O/0).
func Gen(n int) string {
	if n < 8 || n > 64 {
		n = 18
	}
	const abc = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#$%^&*-_=+?"
	var b strings.Builder
	for i := 0; i < n; i++ {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(abc))))
		b.WriteByte(abc[x.Int64()])
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Синхронизация между своими устройствами.

type module struct {
	srv *http.Server
	ln  *chanListener
}

type chanListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *chanListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *chanListener) Addr() net.Addr { return &net.TCPAddr{Port: Port} }

func (m *module) Start() error {
	m.ln = &chanListener{ch: make(chan net.Conn), done: make(chan struct{})}
	mux := http.NewServeMux()
	// Отдаём только зашифрованный файл: без главного пароля он бесполезен.
	mux.HandleFunc("/vault", func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(path())
		if err != nil {
			http.Error(w, "нет", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
	m.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go m.srv.Serve(m.ln)
	ln := m.ln
	mesh.Serve(Port, func(c net.Conn, _ mesh.Caller) {
		select {
		case ln.ch <- c:
		case <-ln.done:
			c.Close()
		}
	})
	startExt()
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ln.done:
				return
			case <-t.C:
				if Unlocked() {
					Sync()
				}
			}
		}
	}()
	return nil
}

func (m *module) Stop() {
	mesh.Serve(Port, nil)
	stopExt()
	if m.srv != nil {
		m.srv.Close()
	}
	if m.ln != nil {
		m.ln.Close()
	}
}

var peerClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext: func(_ context.Context, _, addr string) (net.Conn, error) { return devnet.DialPeer(addr) },
	},
}

// Sync забирает файлы паролей с устройств сети и сливает записи. Возвращает,
// со скольких устройств взял и сколько записей изменилось. Файл, который
// нашим паролем не открыть, но в котором есть ключ для нашего хранилища
// (Wraps), — хранилище, в которое слили наше: переходим на него (adopt).
func Sync() (int, int) {
	mu.Lock()
	if key == nil {
		mu.Unlock()
		return 0, 0
	}
	pass := master
	mu.Unlock()
	got, changed := 0, 0
	for _, pf := range fetchPeers() {
		f, s, n, d, err := readFile(pf.Raw)
		if err != nil {
			continue
		}
		mu.Lock()
		k := keys[string(s)]
		mu.Unlock()
		if k == nil {
			if k, err = derive(pass, s); err != nil {
				continue
			}
		}
		plain, err := open(k, n, d)
		adopt := false
		if err != nil {
			// Другой главный пароль. Может, наше хранилище в него слили?
			mu.Lock()
			k = unwrapFor(f.Wraps, key, n, d)
			mu.Unlock()
			if k == nil {
				continue // чужое хранилище
			}
			if plain, err = open(k, n, d); err != nil {
				continue
			}
			adopt = true
		}
		var theirs []Entry
		if json.Unmarshal(plain, &theirs) != nil {
			continue
		}
		mu.Lock()
		if key == nil {
			mu.Unlock()
			return got, changed
		}
		keys[string(s)] = k
		c := mergeEntries(theirs)
		if adopt {
			// Дальше наше хранилище — то: его соль, ключ и главный пароль.
			// Записи уже слиты, наши правки уйдут к остальным с ним.
			key, salt = k, s
			wraps = unionWraps(wraps, f.Wraps)
			notice = "Это хранилище объединили с хранилищем на «" + pf.Name + "»: записи слиты, теперь оно открывается главным паролем того хранилища."
			c++
		} else if string(s) == string(salt) {
			// То же хранилище: ключи слитых в него хранилищ разносим дальше.
			if w := unionWraps(wraps, f.Wraps); len(w) != len(wraps) {
				wraps = w
				c++
			}
		}
		if c > 0 {
			_ = save()
		}
		lastSync = time.Now()
		mu.Unlock()
		got++
		changed += c
	}
	return got, changed
}

// mergeEntries сливает чужие записи в свои по времени правки; под mu.
func mergeEntries(theirs []Entry) int {
	idx := map[string]int{}
	for i, e := range entries {
		idx[e.ID] = i
	}
	c := 0
	for _, e := range theirs {
		if i, ok := idx[e.ID]; !ok {
			idx[e.ID] = len(entries)
			entries = append(entries, e)
			c++
		} else if e.Upd > entries[i].Upd {
			entries[i] = e
			c++
		}
	}
	return c
}

// ---------------------------------------------------------------------------
// API страницы.

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func listing() []Entry {
	mu.Lock()
	defer mu.Unlock()
	out := []Entry{}
	for _, e := range entries {
		if e.Del {
			continue
		}
		e.Pass = ""
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
	return out
}

func handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		mu.Lock()
		ls := lastSync
		mu.Unlock()
		st := map[string]any{"exists": exists(), "unlocked": Unlocked(), "entries": listing(), "ext": extInfo()}
		if !ls.IsZero() {
			st["lastSync"] = ls.Unix()
		}
		mu.Lock()
		if notice != "" {
			st["notice"] = notice
		}
		mu.Unlock()
		writeJSON(w, st)
		return
	}
	var req struct {
		Op    string `json:"op"`
		Pass  string `json:"master"`
		Len   int    `json:"len"`
		CSV   string `json:"csv"`
		Vault string `json:"vault"`
		Other string `json:"other"`
		Entry
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "нужен JSON {\"op\"}"})
		return
	}
	var err error
	var out any = map[string]bool{"ok": true}
	switch req.Op {
	case "create":
		err = Create(req.Pass)
	case "unlock":
		err = Unlock(req.Pass)
	case "lock":
		Lock()
	case "get":
		mu.Lock()
		var found *Entry
		for i := range entries {
			if entries[i].ID == req.ID && !entries[i].Del {
				e := entries[i]
				found = &e
			}
		}
		locked := key == nil
		mu.Unlock()
		if locked {
			err = errors.New("хранилище закрыто")
		} else if found == nil {
			err = errors.New("нет такой записи")
		} else {
			out = found
		}
	case "set":
		var e Entry
		e, err = Put(req.Entry)
		e.Pass = ""
		out = e
	case "delete":
		err = del(req.ID)
	case "gen":
		out = map[string]string{"pass": Gen(req.Len)}
	case "sync":
		g, c := Sync()
		out = map[string]int{"devices": g, "changed": c}
	case "import":
		var a, u, k int
		if a, u, k, err = ImportCSV([]byte(req.CSV)); err == nil {
			out = map[string]int{"added": a, "updated": u, "skipped": k}
		}
	case "export":
		var p string
		var n int
		if p, n, err = ExportCSV(req.Pass); err == nil {
			out = map[string]any{"path": p, "count": n}
		}
	case "network":
		out = map[string]any{"vaults": Network()}
	case "join":
		err = Join(req.Vault, req.Pass)
	case "merge":
		var n int
		if n, err = Merge(req.Vault, req.Other); err == nil {
			out = map[string]int{"changed": n}
		}
	case "notice":
		mu.Lock()
		notice = ""
		mu.Unlock()
	case "extkey":
		out = map[string]string{"key": newExtKey()}
	case "extinstall":
		var dir string
		if dir, err = installExt(); err == nil {
			out = map[string]string{"dir": dir}
		}
	default:
		err = errors.New("op: create, unlock, lock, get, set, delete, gen, sync, import, export, extkey, extinstall, network, join, merge, notice")
	}
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, out)
}
