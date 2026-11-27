// Package mirror — «Зеркальные папки» (ТЗ 6): устройство-сервер раздаёт
// папки, другие устройства держат их копии; правки идут устройство →
// сервер → все по сети устройств, без туннеля в интернет.
//
// Сервер — Hub (hub.go): единственный источник правды, версии и корзина 30
// дней, конфликты. Клиент — Replica (replica.go). Протокол — proto.go.
// Здесь — модуль ядра (addons): служба на порту Port сети устройств, поиск
// серверов, циклы сверки, API для страницы.
package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sshtunnel/internal/addons"
	"sshtunnel/internal/devnet"
	"sshtunnel/internal/mesh"
)

// Port — служба зеркальных папок в сети устройств (рядом со службой файлов
// 47835).
const Port = 47836

func init() {
	addons.Register("mirror", func() addons.Module { return &module{} })
	addons.RegisterRoutes("mirror",
		addons.Route{Path: "/api/mirror", Methods: "GET, POST",
			Desc: "Зеркальные папки: раздаваемые (serve, у каждой devices — кому видна), подключённые (subs) со статусом, найденные серверы (found), доступ (access: устройства сети, сколько папок у каждого). POST {\"op\":\"serve\",\"path\",\"name\"} | {\"op\":\"unserve\",\"id\"} | {\"op\":\"grant\",\"device\":\"<id устройства>\",\"id\":\"hub:<id>|*\",\"on\":true|false} | {\"op\":\"ignore\",\"id\",\"list\":[…]} | {\"op\":\"join\",\"peer\",\"host\",\"folder\",\"name\",\"path\",\"mode\":\"both|recv|send\",\"existing\":\"|server|merge\"} (папка не пустая и existing пуст — ответ {error, nonEmpty:{files,bytes,sample,zip}}) | {\"op\":\"snap_cfg\",\"id\":\"hub:<id>\",\"daily\",\"every\":1|3|7,\"time\":\"03:00\",\"keep\":3} | {\"op\":\"snap_root\",\"dir\":\"<папка>|\"} (папка снимков устройства: <папка>/<имя папки>/<дата>.zip; пусто — папка по умолчанию <папка программы>/backups; снимки переезжают) | {\"op\":\"snap_now\",\"id\"} | {\"op\":\"snap_restore\",\"id\",\"snap\":\"<файл>\"} | {\"op\":\"snap_save\",\"id\",\"snap\"} (копия в «Загрузки») | {\"op\":\"leave\",\"id\"} | {\"op\":\"mode\",\"id\",\"mode\"} | {\"op\":\"sync\",\"id\"} | {\"op\":\"resolve\",\"id\",\"path\",\"keep\":\"orig|copy|both\"} | {\"op\":\"restore\",\"id\",\"blob\"}. id раздаваемой — «hub:<id>».",
			H:    handleAPI},
		addons.Route{Path: "/api/mirror/versions", Methods: "GET",
			Desc: "Версии и корзина папки ?id=<hub:id|id подключённой>[&p=путь] (30 дней).",
			H:    handleVersions},
		addons.Route{Path: "/api/mirror/file", Methods: "GET",
			Desc: "Содержимое файла папки для просмотра ?id=&p= или старой версии ?id=&blob= (до 2 МБ).",
			H:    handleFile},
		addons.Route{Path: "/api/mirror/snapshots", Methods: "GET",
			Desc: "Снимки раздаваемой папки ?id=hub:<id>: config {daily,time,keep}, dir (где лежат), total, snapshots [{file,time,size,files,reason:auto|manual|pre,seq,error}] — новые сверху.",
			H:    handleSnapshots},
		addons.Route{Path: "/api/mirror/snapshot", Methods: "GET",
			Desc: "Скачать снимок ?id=hub:<id>&snap=<файл> (zip: файлы папки и snapshot.json).",
			H:    handleSnapshot},
	)
}

var (
	notifyMu sync.Mutex
	notifyFn = func(string) {}
)

// SetNotify — куда сообщать человеку о конфликтах (журнал окна,
// уведомление телефона).
func SetNotify(f func(text string)) {
	notifyMu.Lock()
	if f == nil {
		f = func(string) {}
	}
	notifyFn = f
	notifyMu.Unlock()
}

func notify(kind, text string) {
	notifyMu.Lock()
	f := notifyFn
	notifyMu.Unlock()
	f(text)
}

// Sub — подключённая папка этого устройства.
type Sub struct {
	ID     string `json:"id"`
	Peer   string `json:"peer"` // адрес сервера в сети устройств
	Host   string `json:"host"` // имя сервера (адрес мог смениться)
	// PeerID — постоянный id сервера в сети (mesh.Peer.ID, ТЗ 57): по нему
	// папка находит сервер после смены имени и адреса. Пусто — подключена
	// старой версией; id дописывается, как только сервер найден.
	PeerID string `json:"peerId,omitempty"`
	Folder string `json:"folder"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Mode   string `json:"mode"`
}

type found struct {
	Peer    string      `json:"peer"`
	Host    string      `json:"host"`
	PeerID  string      `json:"peerId,omitempty"`
	Folders []FolderRef `json:"folders"`
}

type module struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	hub    *Hub
	acl    *Access
	prog   string // папка программы (progdir.go); "" — нет
	srv    *http.Server
	ln     *meshListener

	mu    sync.Mutex
	subs  map[string]*runner
	found []found
	seen  time.Time // когда искали серверы

	finding atomic.Bool // поиск серверов уже идёт — второй не запускать
}

type runner struct {
	Sub
	m      *module
	rep    *Replica
	kick   chan struct{}
	cancel context.CancelFunc
}

var (
	curMu sync.Mutex
	cur   *module
)

func current() *module {
	curMu.Lock()
	defer curMu.Unlock()
	return cur
}

func dataDir() string { return filepath.Join(addons.DataDir(), "mirror") }

// selfName — имя этого устройства в сети (для конфликт-копий).
func selfName() string {
	for _, p := range devnet.Status().Peers {
		if p.Self && p.Host != "" {
			return p.Host
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" && h != "localhost" {
		return h
	}
	return runtime.GOOS
}

func (m *module) Start() error {
	h, err := OpenHub(filepath.Join(dataDir(), "hub"), selfName())
	if err != nil {
		return err
	}
	h.OnEvent = notify
	m.hub = h
	// Снимки по умолчанию — в <папка программы>/backups; лежавшие в данных
	// программы переезжают туда один раз (ТЗ 39).
	m.prog = programDirFn()
	if err := h.SetSnapRootDefault(backupsDir(m.prog)); err != nil {
		notify("snap", "Снимки: "+err.Error())
	}
	var ids []string
	for _, f := range h.Folders() {
		ids = append(ids, f.ID)
	}
	if m.acl, err = OpenAccess(filepath.Join(dataDir(), "access.json"), ids); err != nil {
		h.Close()
		return err
	}
	h.Visible = func(r *http.Request, folder string) bool {
		id := callerOf(r).ID
		if id != "" && id == selfID() {
			return true // сам к себе
		}
		return m.acl.Allowed(folder, id)
	}
	m.migrate()
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.subs = map[string]*runner{}

	m.ln = &meshListener{ch: make(chan net.Conn), done: make(chan struct{})}
	m.srv = &http.Server{Handler: h.Handler(who), ReadHeaderTimeout: 20 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if cc, ok := c.(callerConn); ok {
				return context.WithValue(ctx, callerKey{}, cc.from)
			}
			return ctx
		}}
	go m.srv.Serve(m.ln)
	mesh.Serve(Port, func(c net.Conn, from mesh.Caller) {
		select {
		case m.ln.ch <- callerConn{c, from}:
		case <-m.ln.done:
			c.Close()
		}
	})

	var subs []Sub
	_ = readJSON(filepath.Join(dataDir(), "subs.json"), &subs)
	for _, s := range subs {
		m.startSub(s)
	}
	m.wg.Add(2)
	go m.hubLoop()
	go m.findLoop()

	curMu.Lock()
	cur = m
	curMu.Unlock()
	return nil
}

func (m *module) Stop() {
	curMu.Lock()
	if cur == m {
		cur = nil
	}
	curMu.Unlock()
	mesh.Serve(Port, nil)
	m.cancel()
	m.mu.Lock()
	for _, r := range m.subs {
		r.cancel()
	}
	m.mu.Unlock()
	m.srv.Close()
	m.ln.Close()
	m.wg.Wait()
	m.hub.Close()
}

// hubLoop — сверка раздаваемых папок с диском (правки прямо на сервере)
// раз в 30 с и чистка версий старше 30 дней раз в час.
func (m *module) hubLoop() {
	defer m.wg.Done()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	lastPrune := time.Time{}
	for {
		for _, f := range m.hub.Folders() {
			_ = m.hub.Scan(f.ID)
		}
		m.hub.SnapTick() // снимки по расписанию (ТЗ 36)
		if time.Since(lastPrune) > time.Hour {
			m.hub.Prune()
			lastPrune = time.Now()
		}
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// findLoop — какие устройства сети раздают папки («Найден сервер,
// подключиться?»).
func (m *module) findLoop() {
	defer m.wg.Done()
	for {
		m.find()
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// selfID — постоянный id этого устройства в сети (mesh.PeerID).
func selfID() string { return devnet.Status().Self.ID }

// netDevice — устройство сети в списке «Доступ к папкам».
type netDevice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host,omitempty"`
	IP       string `json:"ip,omitempty"`
	Platform string `json:"platform,omitempty"`
	Server   bool   `json:"server,omitempty"`
	Online   bool   `json:"online"`
	// Gone — устройства больше нет в списке сети (убрали в панели), а
	// папки у него остались: их можно забрать.
	Gone    bool `json:"gone,omitempty"`
	Folders int  `json:"folders"`
}

// devices — все устройства сети с id (и не в сети), сначала серверы,
// потом остальные, внутри — по имени.
func (m *module) devices() []netDevice {
	return listDevices(devnet.Status().Peers, m.acl)
}

func listDevices(peers []mesh.Peer, acl *Access) []netDevice {
	granted := acl.Granted()
	seen := map[string]bool{}
	out := []netDevice{}
	for _, p := range peers {
		if p.Self || p.ID == "" || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		name := p.Name
		if name == "" {
			name = p.Host
		}
		d := netDevice{ID: p.ID, Name: name, Host: p.Host, Platform: p.Platform, Server: p.Platform == "server",
			Online: p.Online, Folders: granted[p.ID]}
		if p.IP.IsValid() {
			d.IP = p.IP.String()
		}
		out = append(out, d)
	}
	for id, n := range granted {
		if !seen[id] {
			name := acl.Name(id)
			if name == "" {
				name = id
			}
			out = append(out, netDevice{ID: id, Name: name, Gone: true, Folders: n})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Server != b.Server {
			return a.Server
		}
		if x, y := strings.ToLower(a.Name), strings.ToLower(b.Name); x != y {
			return x < y
		}
		return a.ID < b.ID
	})
	return out
}

// known — устройства сети с id для правила обновления.
func known(peers []mesh.Peer) []Known {
	var out []Known
	for _, p := range peers {
		if !p.Self && p.ID != "" {
			out = append(out, Known{ID: p.ID, Name: p.Name})
		}
	}
	return out
}

// migrate — правило обновления (access.go): как только известны id
// устройств сети, старые папки получают все известные в этот момент.
func (m *module) migrate() {
	if !m.acl.Pending() {
		return
	}
	if did, _ := m.acl.Migrate(known(devnet.Status().Peers)); did {
		m.hub.Changed()
	}
}

// grant — дать или забрать папку (folder "*" — все раздаваемые).
func (m *module) grant(device, folder string, on bool) error {
	if device == "" {
		return errors.New("нужен device — id устройства")
	}
	m.migrate() // старые папки — сначала по правилу обновления
	name := ""
	for _, d := range m.devices() {
		if d.ID == device {
			name = d.Name
		}
	}
	var ids []string
	if folder == "*" {
		for _, f := range m.hub.Folders() {
			ids = append(ids, f.ID)
		}
	} else {
		if _, err := m.hub.folder(folder); err != nil {
			return errors.New("нет такой раздаваемой папки")
		}
		ids = []string{folder}
	}
	defer m.hub.Changed() // ждущие опросы перепроверят доступ сразу
	for _, f := range ids {
		if err := m.acl.Set(f, device, name, on); err != nil {
			return err
		}
	}
	return nil
}

func (m *module) find() {
	m.migrate()
	var mu sync.Mutex
	var out []found
	var wg sync.WaitGroup
	for _, p := range devnet.Status().Peers {
		if p.Self || !p.Online || !p.IP.IsValid() {
			continue
		}
		wg.Add(1)
		go func(p mesh.Peer) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(m.ctx, 6*time.Second)
			defer cancel()
			fs, err := remoteFor(p.IP.String()).Folders(ctx)
			if err != nil || len(fs) == 0 {
				return
			}
			mu.Lock()
			out = append(out, found{Peer: p.IP.String(), Host: p.Host, PeerID: p.ID, Folders: fs})
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	m.mu.Lock()
	m.found, m.seen = out, time.Now()
	m.mu.Unlock()
}

// remoteFor — клиент сервера по адресу в сети устройств.
// Переменная — для тестов.
var remoteFor = func(peer string) *Remote {
	return &Remote{Base: "http://" + net.JoinHostPort(peer, strconv.Itoa(Port)),
		HC: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return devnet.DialPeer(addr)
			},
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		}}}
}

// locate — сервер папки среди устройств сети: по id (ТЗ 57: имя и адрес
// могут смениться), у папки без id — по имени, потом по адресу.
func locate(s Sub, peers []mesh.Peer) (mesh.Peer, bool) {
	if s.PeerID != "" {
		for _, p := range peers {
			if !p.Self && p.ID == s.PeerID {
				return p, true
			}
		}
		return mesh.Peer{}, false
	}
	if s.Host != "" {
		for _, p := range peers {
			if !p.Self && p.Host == s.Host && p.IP.IsValid() {
				return p, true
			}
		}
	}
	for _, p := range peers {
		if !p.Self && p.IP.IsValid() && p.IP.String() == s.Peer {
			return p, true
		}
	}
	return mesh.Peer{}, false
}

// peerAddr — нынешний адрес сервера папки; не нашли — последний известный.
func peerAddr(s Sub, peers []mesh.Peer) string {
	if p, ok := locate(s, peers); ok && p.IP.IsValid() {
		return p.IP.String()
	}
	return s.Peer
}

// follow — сервер папки сменил имя или адрес: запомнить новые (и id, если
// папка подключена старой версией) и сохранить список. Отдаёт адрес.
func (r *runner) follow(peers []mesh.Peer) string {
	m := r.m
	if m == nil {
		return peerAddr(r.Sub, peers)
	}
	m.mu.Lock()
	p, ok := locate(r.Sub, peers)
	changed := false
	if ok {
		ip := ""
		if p.IP.IsValid() {
			ip = p.IP.String()
		}
		if p.ID != "" && r.PeerID != p.ID {
			r.PeerID, changed = p.ID, true
		}
		if p.Host != "" && r.Host != p.Host {
			r.Host, changed = p.Host, true
		}
		if ip != "" && r.Peer != ip {
			r.Peer, changed = ip, true
		}
	}
	addr := r.Peer
	m.mu.Unlock()
	if changed {
		_ = m.saveSubs()
	}
	return addr
}

// peersNow — устройства сети (переменная — для тестов).
var peersNow = func() []mesh.Peer { return devnet.Status().Peers }

func (m *module) startSub(s Sub) {
	ctx, cancel := context.WithCancel(m.ctx)
	r := &runner{Sub: s, m: m, kick: make(chan struct{}, 1), cancel: cancel}
	r.rep = &Replica{Folder: s.Folder, Path: s.Path, Mode: s.Mode, Device: selfName(),
		State: filepath.Join(dataDir(), "sub-"+s.ID+".json"), R: remoteFor(peerAddr(s, peersNow())), OnEvent: notify}
	m.mu.Lock()
	m.subs[s.ID] = r
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		r.loop(ctx)
	}()
}

// loop — сверка: долгий опрос сервера (изменения приходят сразу), свои
// изменения — не позже чем через 10 с, полная перепроверка хешей — раз в 6
// часов. Без сервера — повтор раз в 30 с; очередь — разница диска и
// состояния, она не теряется.
func (r *runner) loop(ctx context.Context) {
	wait := time.Duration(0)
	lastFull := time.Now()
	for ctx.Err() == nil {
		r.rep.R = remoteFor(r.follow(peersNow()))
		full := time.Since(lastFull) > 6*time.Hour
		did, err := r.rep.Sync(ctx, wait, full)
		if full && err == nil {
			lastFull = time.Now()
		}
		pause := time.Duration(0)
		switch {
		case err != nil && ctx.Err() == nil:
			wait, pause = 0, 30*time.Second
			if st := r.rep.Status().State; st != "offline" && st != "closed" {
				pause = 15 * time.Second
			}
		case did:
			wait = 0
		default:
			wait = 10 * time.Second
		}
		if pause > 0 {
			select {
			case <-ctx.Done():
			case <-r.kick:
			case <-time.After(pause):
			}
		}
		select {
		case <-r.kick:
			wait = 0
		default:
		}
	}
}

func (m *module) saveSubs() error {
	m.mu.Lock()
	list := []Sub{}
	for _, r := range m.subs {
		list = append(list, r.Sub)
	}
	m.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return writeJSONAtomic(filepath.Join(dataDir(), "subs.json"), list)
}

func (m *module) overlaps(p string) error {
	for _, f := range m.hub.Folders() {
		if nested(f.Path, p) {
			return errors.New("пересекается с раздаваемой папкой «" + f.Name + "»")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.subs {
		if nested(r.Path, p) {
			return errors.New("пересекается с подключённой папкой «" + r.Name + "»")
		}
	}
	return nil
}

// errNonEmpty — в выбранной папке уже есть файлы, нужен выбор человека.
var errNonEmpty = errors.New("в этой папке уже есть файлы — выбери, что с ними сделать")

// join — подключить папку. В непустую папку — только с выбором existing
// (ТЗ 36): без него ответ errNonEmpty и что там лежит.
// sameServer — папки a и b с одного сервера: по id, если он есть у обеих,
// иначе по имени или адресу.
func sameServer(a, b Sub) bool {
	if a.PeerID != "" && b.PeerID != "" {
		return a.PeerID == b.PeerID
	}
	return a.Host == b.Host || a.Peer == b.Peer
}

func (m *module) join(s Sub, existing string) (*NonEmpty, string, error) {
	switch s.Mode {
	case "":
		s.Mode = ModeBoth
	case ModeBoth, ModeRecv, ModeSend:
	default:
		return nil, "", errors.New("mode: both, recv или send")
	}
	switch existing {
	case JoinAsk, JoinServer, JoinMerge:
	default:
		return nil, "", errors.New("existing: server или merge")
	}
	if s.Peer == "" || s.Folder == "" || strings.TrimSpace(s.Path) == "" {
		return nil, "", errors.New("нужны peer, folder и path")
	}
	abs, err := filepath.Abs(localPath(s.Path))
	if err != nil {
		return nil, "", err
	}
	if err := m.overlaps(abs); err != nil {
		return nil, "", err
	}
	if p, ok := locate(s, peersNow()); ok && s.PeerID == "" {
		s.PeerID = p.ID // старая страница id не шлёт — берём из сети
	}
	m.mu.Lock()
	for _, r := range m.subs {
		if r.Folder == s.Folder && sameServer(r.Sub, s) {
			m.mu.Unlock()
			return nil, "", errors.New("эта папка уже подключена: " + r.Path)
		}
	}
	m.mu.Unlock()
	text := ""
	ne, err := checkNonEmpty(abs, time.Now())
	if err != nil {
		return nil, "", err
	}
	if ne != nil {
		switch existing {
		case JoinAsk:
			return ne, "", errNonEmpty
		case JoinServer:
			// Своё — в архив рядом с папкой; не вышло (нет прав) — в данные
			// программы.
			zp := ne.Zip
			n, err := moveToZip(abs, zp)
			if err != nil && n == 0 {
				zp = joinZipPath(filepath.Join(dataDir(), "before-join", filepath.Base(abs)), time.Now())
				n, err = moveToZip(abs, zp)
			}
			if err != nil {
				return nil, "", err
			}
			text = fmt.Sprintf("Свои файлы папки (%d) убраны в архив %s; папка берётся с сервера", n, zp)
			notify("join", text)
		case JoinMerge:
			// Как раньше: совпадающие файлы не передаются, разные становятся
			// конфликт-копиями — ничего не затирается, но всё уходит в общую
			// папку.
		}
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, "", err
	}
	s.Path = abs
	s.ID = randHex(5)
	m.startSub(s)
	return nil, text, m.saveSubs()
}

func (m *module) leave(id string) error {
	m.mu.Lock()
	r, ok := m.subs[id]
	delete(m.subs, id)
	m.mu.Unlock()
	if !ok {
		return errors.New("нет такой папки")
	}
	r.cancel()
	r.rep.Forget() // ждёт конца прохода; файлы папки остаются
	return m.saveSubs()
}

func (m *module) setMode(id, mode string) error {
	if mode != ModeBoth && mode != ModeRecv && mode != ModeSend {
		return errors.New("mode: both, recv или send")
	}
	m.mu.Lock()
	r, ok := m.subs[id]
	if ok {
		r.Mode = mode
	}
	m.mu.Unlock()
	if !ok {
		return errors.New("нет такой папки")
	}
	r.rep.mu.Lock() // между проходами
	r.rep.Mode = mode
	r.rep.mu.Unlock()
	select {
	case r.kick <- struct{}{}:
	default:
	}
	return m.saveSubs()
}

func (m *module) sub(id string) (*runner, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.subs[id]
	return r, ok
}

// ---- API для страницы ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// localPath — путь, как его написал человек, — на телефоне во внутренней
// памяти: «/TRASH/Obsidian/Server» или «TRASH/Obsidian» — это
// /storage/emulated/0/TRASH/…, как у Syncthing (корень Android только для
// чтения, и такой путь иначе падал с «read-only file system»).
func localPath(p string) string {
	p = strings.TrimSpace(p)
	if runtime.GOOS != "android" || p == "" {
		return p
	}
	for _, root := range []string{"/storage/", "/sdcard", "/mnt/", "/data/"} {
		if strings.HasPrefix(p, root) {
			return p
		}
	}
	return "/storage/emulated/0/" + strings.TrimLeft(p, "/")
}

// suggestBase — куда предлагать новые папки: <папка программы>/
// mirrored_folders (ТЗ 39); папки программы нет — как раньше.
func (m *module) suggestBase() string {
	if m.prog != "" {
		return filepath.Join(m.prog, mirroredSub)
	}
	if runtime.GOOS == "android" {
		return androidRoot
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "Documents")
	}
	return ""
}

// newPath — путь по умолчанию для новой папки name.
func (m *module) newPath(name string) string {
	if p := newFolderPath(m.prog, name); p != "" {
		return p
	}
	if b := m.suggestBase(); b != "" {
		return filepath.Join(b, safeFileName(name))
	}
	return ""
}

// isJoined — папка folder сервера f уже подключена (одна из subs): по id
// сервера, у старых записей — по имени или адресу (ТЗ 57: после смены
// имени папка не предлагается второй раз).
func isJoined(subs []Sub, f found, folder string) bool {
	for _, s := range subs {
		if s.Folder == folder && sameServer(s, Sub{Peer: f.Peer, Host: f.Host, PeerID: f.PeerID}) {
			return true
		}
	}
	return false
}

func (m *module) state() map[string]any {
	type serveItem struct {
		HubFolder
		Files     int        `json:"files"`
		Bytes     int64      `json:"bytes"` // сколько занимают файлы папки (ТЗ 39)
		Conflicts []Conflict `json:"conflicts"`
		// Devices — кому видна (id устройств, ТЗ 18); Old — папка с прошлой
		// версии, пока видна всем (ждёт id устройств от meshd).
		Devices []string `json:"devices"`
		Old     bool     `json:"old,omitempty"`
		// Snap — снимки папки (ТЗ 36).
		Snap *SnapList `json:"snap,omitempty"`
	}
	m.migrate()
	serve := []serveItem{}
	for _, f := range m.hub.Folders() {
		it := serveItem{HubFolder: f, Conflicts: []Conflict{}, Devices: m.acl.Devices(f.ID), Old: m.acl.IsOld(f.ID)}
		if n, b, cs, err := m.hub.Counts(f.ID); err == nil {
			it.Files, it.Bytes, it.Conflicts = n, b, cs
		}
		if sl, err := m.hub.Snapshots(f.ID); err == nil {
			it.Snap = &sl
		}
		serve = append(serve, it)
	}
	type subItem struct {
		Sub
		Status Status `json:"status"`
	}
	m.mu.Lock()
	subs := []subItem{}
	var mine []Sub
	for _, r := range m.subs {
		subs = append(subs, subItem{r.Sub, r.rep.Status()})
		mine = append(mine, r.Sub)
	}
	type foundItem struct {
		found
		Joined map[string]bool `json:"joined"`
		// Path — куда по умолчанию подключить папку (id → путь), ТЗ 39.
		Path map[string]string `json:"path,omitempty"`
	}
	fs := []foundItem{}
	for _, f := range m.found {
		it := foundItem{f, map[string]bool{}, map[string]string{}}
		for _, x := range f.Folders {
			if isJoined(mine, f, x.ID) {
				it.Joined[x.ID] = true
			} else if p := m.newPath(x.Name); p != "" {
				it.Path[x.ID] = p
			}
		}
		fs = append(fs, it)
	}
	stale := time.Since(m.seen) > 15*time.Second
	m.mu.Unlock()
	if stale && m.finding.CompareAndSwap(false, true) {
		go func() { defer m.finding.Store(false); m.find() }()
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].Name < subs[j].Name })
	devs := m.devices()
	// ids — сеть даёт id устройств (новый meshd): без них доступ не выдать.
	ids := false
	for _, d := range devs {
		ids = ids || !d.Gone
	}
	def := m.hub.SnapRootDefault()
	return map[string]any{"serve": serve, "subs": subs, "found": fs, "self": selfName(),
		// Папка программы и папка снимков (ТЗ 39): snapRoot — своя ("" — по
		// умолчанию), snapRootDefault — по умолчанию, snapRootNow — где
		// архивы сейчас ("" — в данных программы). snapRootSuggest — для
		// старых страниц.
		"programDir": m.prog, "snapRoot": m.hub.SnapRootCustom(), "snapRootDefault": def,
		"snapRootNow": m.hub.SnapRoot(), "snapRootSuggest": def,
		"suggest": m.suggestBase(), "platform": runtime.GOOS, "devnet": devnet.Status().State,
		"access": map[string]any{"devices": devs, "ids": ids, "pending": m.acl.Pending()}}
}

func handleAPI(w http.ResponseWriter, r *http.Request) {
	m := current()
	if m == nil {
		http.Error(w, "приложение не запущено", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			Op     string   `json:"op"`
			ID     string   `json:"id"`
			Path   string   `json:"path"`
			Name   string   `json:"name"`
			List   []string `json:"list"`
			Peer   string   `json:"peer"`
			Host   string   `json:"host"`
			PeerID string   `json:"peerId"`
			Folder string   `json:"folder"`
			Mode   string   `json:"mode"`
			Keep   keepArg  `json:"keep"` // resolve: "orig|copy|both"; snap_cfg: число
			Blob   string   `json:"blob"`
			Device string   `json:"device"`
			On     bool     `json:"on"`
			// ТЗ 36.
			Existing string `json:"existing"`
			Daily    bool   `json:"daily"`
			Every    int    `json:"every"`
			Time     string `json:"time"`
			Snap     string `json:"snap"`
			Dir      string `json:"dir"` // snap_root
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "нужен JSON {\"op\"}"})
			return
		}
		var err error
		var text string // что сказать человеку (всплывашка)
		var ne *NonEmpty
		hubID, isHub := strings.CutPrefix(req.ID, "hub:")
		switch req.Op {
		case "serve":
			if err = m.overlaps(req.Path); err == nil && within(m.hub.SnapRoot(), localPath(req.Path)) {
				err = errors.New("в этой папке лежат снимки — раздавать её нельзя, архивы разошлись бы по всем устройствам")
			}
			if err == nil {
				err = m.makeNew(localPath(req.Path))
			}
			if err == nil {
				_, err = m.hub.AddFolder(localPath(req.Path), req.Name)
			}
		case "unserve":
			if err = m.hub.RemoveFolder(hubID); err == nil {
				err = m.acl.Remove(hubID)
			}
		case "grant":
			switch {
			case req.ID == "*":
				err = m.grant(req.Device, "*", req.On)
			case isHub:
				err = m.grant(req.Device, hubID, req.On)
			default:
				err = errors.New("id: hub:<id папки> или *")
			}
		case "ignore":
			err = m.hub.SetIgnore(hubID, req.List)
		case "join":
			ne, text, err = m.join(Sub{Peer: req.Peer, Host: req.Host, PeerID: req.PeerID, Folder: req.Folder, Name: req.Name, Path: req.Path, Mode: req.Mode}, req.Existing)
		case "snap_cfg":
			if !isHub {
				err = errors.New("снимки — только у раздаваемой папки (id hub:<id>)")
			} else {
				_, err = m.hub.SetSnapConfig(hubID, SnapConfig{Daily: req.Daily, Every: req.Every, Time: req.Time, Keep: req.Keep.n})
			}
		case "snap_root":
			var d string
			if d, err = m.hub.SetSnapRoot(localPath(req.Dir)); err == nil {
				if d == "" {
					text = "Снимки — в данных программы"
				} else {
					text = "Снимки теперь в " + d
				}
			}
		case "snap_now":
			if !isHub {
				err = errors.New("снимки — только у раздаваемой папки (id hub:<id>)")
			} else {
				var s Snapshot
				if s, err = m.hub.SnapNow(hubID, SnapManual); err == nil {
					text = "Снимок готов: " + s.File
				}
			}
		case "snap_restore":
			if !isHub {
				err = errors.New("снимки — только у раздаваемой папки (id hub:<id>)")
			} else {
				var res RestoreResult
				res, err = m.hub.RestoreSnapshot(hubID, selfName(), req.Snap)
				text = res.Text
			}
		case "snap_save":
			// Телефон: у WebView своих загрузок нет — копия снимка в «Загрузки».
			if !isHub {
				err = errors.New("снимки — только у раздаваемой папки (id hub:<id>)")
			} else {
				var p string
				if p, err = m.hub.SaveSnapshot(hubID, req.Snap, downloadsDir()); err == nil {
					text = "Снимок сохранён: " + p
				}
			}
		case "leave":
			err = m.leave(req.ID)
		case "mode":
			err = m.setMode(req.ID, req.Mode)
		case "sync":
			if isHub {
				err = m.hub.Scan(hubID)
			} else if s, ok := m.sub(req.ID); ok {
				select {
				case s.kick <- struct{}{}:
				default:
				}
			}
		case "resolve":
			if isHub {
				err = m.hub.Resolve(hubID, selfName(), req.Path, req.Keep.s)
			} else if s, ok := m.sub(req.ID); ok {
				ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
				err = s.rep.Resolve(ctx, req.Path, req.Keep.s)
				cancel()
				select {
				case s.kick <- struct{}{}:
				default:
				}
			} else {
				err = errors.New("нет такой папки")
			}
		case "restore":
			if isHub {
				_, err = m.hub.Restore(hubID, selfName(), req.Blob)
			} else if s, ok := m.sub(req.ID); ok {
				var out map[string]string
				err = s.rep.R.post(r.Context(), "/restore", map[string]string{"f": s.Folder, "blob": req.Blob}, &out)
			} else {
				err = errors.New("нет такой папки")
			}
		default:
			err = errors.New("op: serve, unserve, grant, ignore, join, leave, mode, sync, resolve, restore, snap_cfg, snap_root, snap_now или snap_restore")
		}
		if err != nil {
			out := map[string]any{"error": err.Error()}
			if ne != nil {
				out["nonEmpty"] = ne
			}
			writeJSON(w, out)
			return
		}
		if text != "" {
			st := m.state()
			st["text"] = text
			writeJSON(w, st)
			return
		}
	}
	writeJSON(w, m.state())
}

// handleSnapshots — список снимков раздаваемой папки.
func handleSnapshots(w http.ResponseWriter, r *http.Request) {
	m := current()
	if m == nil {
		http.Error(w, "приложение не запущено", http.StatusServiceUnavailable)
		return
	}
	id, ok := strings.CutPrefix(r.URL.Query().Get("id"), "hub:")
	if !ok {
		writeJSON(w, map[string]string{"error": "снимки — только у раздаваемой папки (id hub:<id>)"})
		return
	}
	sl, err := m.hub.Snapshots(id)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, sl)
}

// handleSnapshot — скачать архив снимка.
func handleSnapshot(w http.ResponseWriter, r *http.Request) {
	m := current()
	if m == nil {
		http.Error(w, "приложение не запущено", http.StatusServiceUnavailable)
		return
	}
	id, ok := strings.CutPrefix(r.URL.Query().Get("id"), "hub:")
	if !ok {
		http.Error(w, "снимки — только у раздаваемой папки", http.StatusNotFound)
		return
	}
	f, s, err := m.hub.OpenSnapshot(id, r.URL.Query().Get("snap"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	ServeSnapshot(w, r, f, s)
}

// ServeSnapshot отдаёт архив снимка как скачивание (и панели сервера).
func ServeSnapshot(w http.ResponseWriter, r *http.Request, f io.ReadSeeker, s Snapshot) {
	w.Header().Set("Content-Type", "application/zip")
	name := snapDownloadName(s)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", time.Unix(s.Time, 0), f)
}

func handleVersions(w http.ResponseWriter, r *http.Request) {
	m := current()
	if m == nil {
		http.Error(w, "приложение не запущено", http.StatusServiceUnavailable)
		return
	}
	id, p := r.URL.Query().Get("id"), r.URL.Query().Get("p")
	if hubID, ok := strings.CutPrefix(id, "hub:"); ok {
		vs, err := m.hub.Versions(hubID, p)
		if err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, vs)
		return
	}
	s, ok := m.sub(id)
	if !ok {
		writeJSON(w, map[string]string{"error": "нет такой папки"})
		return
	}
	var vs []Version
	if err := s.rep.R.json(r.Context(), "GET", "/versions", url.Values{"f": {s.Folder}, "p": {p}}, nil, &vs); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, vs)
}

// handleFile — файл для просмотра двух версий рядом. С клиента берётся с
// сервера: у «только отправлять» копии конфликта на устройстве нет.
func handleFile(w http.ResponseWriter, r *http.Request) {
	m := current()
	if m == nil {
		http.Error(w, "приложение не запущено", http.StatusServiceUnavailable)
		return
	}
	id, p, blob := r.URL.Query().Get("id"), r.URL.Query().Get("p"), r.URL.Query().Get("blob")
	var rc io.ReadCloser
	if hubID, ok := strings.CutPrefix(id, "hub:"); ok {
		var f *os.File
		var err error
		if blob != "" {
			f, err = m.hub.OpenVersion(hubID, blob)
		} else {
			f, err = m.hub.Open(hubID, p, "")
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		rc = f
	} else {
		s, ok := m.sub(id)
		if !ok {
			http.Error(w, "нет такой папки", http.StatusNotFound)
			return
		}
		path, q := "/get", url.Values{"f": {s.Folder}, "p": {p}}
		if blob != "" {
			path, q = "/blob", url.Values{"f": {s.Folder}, "b": {blob}}
		}
		resp, err := s.rep.R.do(r.Context(), "GET", path, q, nil, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			http.Error(w, resp.Status, resp.StatusCode)
			return
		}
		rc = resp.Body
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, io.LimitReader(rc, 2<<20))
}

// ---- служба в сети устройств (как у internal/devfiles) ----

type callerKey struct{}

type callerConn struct {
	net.Conn
	from mesh.Caller
}

type meshListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func (l *meshListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *meshListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *meshListener) Addr() net.Addr { return &net.TCPAddr{Port: Port} }

// callerOf — кто пришёл (ставит сеть устройств, не подделать).
func callerOf(r *http.Request) mesh.Caller {
	c, _ := r.Context().Value(callerKey{}).(mesh.Caller)
	return c
}

// who — имя пришедшего устройства (ставит сеть устройств, не подделать).
func who(r *http.Request) string {
	c := callerOf(r)
	if c.Host != "" {
		return c.Host
	}
	return c.IP
}

// keepArg — поле keep запроса: строка у resolve, число у snap_cfg.
type keepArg struct {
	s string
	n int
}

func (k *keepArg) UnmarshalJSON(b []byte) error {
	if json.Unmarshal(b, &k.s) == nil {
		return nil
	}
	return json.Unmarshal(b, &k.n)
}

// makeNew — новая папка в <папка программы>/mirrored_folders создаётся сама:
// путь туда подставлен по умолчанию, а заводить её руками незачем. Любой
// другой путь должен уже быть.
func (m *module) makeNew(p string) error {
	if m.prog == "" || strings.TrimSpace(p) == "" {
		return nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil
	}
	base := filepath.Join(m.prog, mirroredSub)
	if !within(abs, base) || samePath(abs, base) {
		return nil
	}
	if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return fmt.Errorf("папка не создаётся: %w", err)
		}
	}
	return nil
}

// downloadsDir — «Загрузки» этого устройства (как у «Файлов устройства»).
func downloadsDir() string {
	if runtime.GOOS == "android" {
		return "/storage/emulated/0/Download"
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Downloads")
}
