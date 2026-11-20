package panel

// Сеть серверов (ТЗ 22): все серверы равны, сервер добавляется в сеть одним
// кодом, правила и учётные записи общие.
//
// Как устроено:
//   - у каждого сервера свой ключ связи (servernet/id_ed25519). Ключи всех
//     серверов сети лежат в authorized_keys пользователя meshlink у каждого
//     с ограничением «только проброс до 127.0.0.1:SyncPort»: ни оболочки,
//     ни других адресов (meshsvc.PeerKeyOptions, meshsvc.LinkSSHDBlock);
//   - панель каждого сервера слушает 127.0.0.1:SyncPort и раз в 30 секунд
//     (и сразу после правки) обменивается общими данными с каждым сервером
//     сети напрямую — по SSH, другого транспорта нет. Запрос подписан
//     ключом связи отправителя: порт на петле видят и другие программы
//     сервера;
//   - общие данные — набор записей «ключ → значение, время правки, кто
//     правил»; побеждает последняя правка, удалённая запись остаётся
//     надгробием, чтобы не вернуться (как цепочка выхода, ExitT/ExitBy);
//   - сервер, о котором узнали от другого, уже доверенный: его ключ
//     приходит в общих данных, второй раз связывать не нужно.
//
// Код «Добавить сервер» — одноразовый, на час: адрес и SSH-ключи этого
// сервера, id сети, разовый секрет и разовый ключ входа (его открытая часть
// лежит у meshlink, пока код не использован или не истёк). Долгоживущего
// ключа в коде нет: новый сервер по разовому ключу отдаёт свой ключ связи
// и получает общие данные.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/meshsvc"
)

// srvItem — запись общих данных.
type srvItem struct {
	T   int64           `json:"t"`
	By  string          `json:"by,omitempty"`
	Del bool            `json:"del,omitempty"`
	D   json.RawMessage `json:"d,omitempty"`
}

// newer — a новее b (при равном времени решает id сервера: одинаково везде).
func (a srvItem) newer(b srvItem) bool {
	if a.T != b.T {
		return a.T > b.T
	}
	if a.By != b.By {
		return a.By > b.By
	}
	return a.Del && !b.Del
}

// srvRec — сервер сети.
type srvRec struct {
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	HostKeys []string `json:"hostKeys,omitempty"`
	// Key — открытый ключ связи сервера ("ssh-ed25519 AAAA…").
	Key string `json:"key"`
	// MeshID — id его meshd (по нему устройства узнают сервер).
	MeshID string `json:"meshId,omitempty"`
}

type onRec struct {
	On bool `json:"on"`
}

type nameRec struct {
	Name string `json:"name"`
}

type aliasRec struct {
	Alias string `json:"alias"`
}

// srvInvite — выданный код (хранится только здесь).
type srvInvite struct {
	Hash    string `json:"hash"`
	Pub     string `json:"pub"`
	Expires int64  `json:"expires"`
}

// srvFile — servernet.json.
type srvFile struct {
	Net     string             `json:"net,omitempty"`
	Self    string             `json:"self"`
	Items   map[string]srvItem `json:"items,omitempty"`
	Invites []srvInvite        `json:"invites,omitempty"`
	// Removed — этот сервер убрали из сети (имя того, кто убрал): панель
	// покажет это один раз, сервер работает дальше один.
	Removed string `json:"removed,omitempty"`
}

// Ключи записей.
const (
	kSrv    = "srv:"
	kAcct   = "acct:"
	kAccess = "access"
	kAppr   = "appr:"
	kNName  = "nname:"
	kNDel   = "ndel:"
	kOK     = "ok:"
	kFrz    = "frz:"
	kAlias  = "alias:"
	kForget = "forget:"
	// kSName — имя сервера, выбранное в панели (ТЗ 24): новее — побеждает,
	// перекрывает имя, которое сервер называет сам.
	kSName = "sname:"
)

func devKey(prefix, netID, dev string) string { return prefix + netID + "/" + dev }

// inviteTTL — сколько живёт код «Добавить сервер».
const inviteTTL = time.Hour

const (
	codePrefix  = "sshtunnel-net1:"
	maxServers  = 16
	syncEvery   = 30 * time.Second
	maxSyncBody = 8 << 20
)

// ServerNet — сеть серверов этой панели.
type ServerNet struct {
	path    string
	keyPath string

	mesh *MeshManager
	srv  *Server // учётные записи и правила (nil — их нет, как в части тестов)

	// dial — соединение с портом обмена сервера rec (по SSH; в тестах —
	// напрямую). signer — каким ключом входить.
	dial func(ctx context.Context, rec srvRec, signer ssh.Signer) (net.Conn, error)
	now  func() time.Time
	// listen — где слушать обмен ("" — 127.0.0.1:SyncPort).
	listen string

	mu     sync.Mutex
	f      srvFile
	signer ssh.Signer
	peers  map[string]*peerStatus
	kick   chan struct{}
	ln     net.Listener
	cancel context.CancelFunc

	// views — последняя живая картина каждого сервера сети (ТЗ 24,
	// srvnet_view.go), лежит на диске рядом с общими данными; pulled —
	// когда её последний раз запрашивали (не чаще раза в 5 секунд).
	viewPath string
	views    map[string]peerView
	pulled   map[string]time.Time
	// lastRemote/remoteAt — что последним отдали meshd о других серверах
	// (srvnet_relay.go).
	lastRemote string
	remoteAt   time.Time

	reconMu  sync.Mutex
	lastKeys string
	lastKnwn string
	knownAt  time.Time
	// meshdStart — когда запущен meshd, которому отдавали списки: meshd
	// перезапустился (обновили) — у него пусто, отдать всё заново сразу.
	meshdStart int64
	applying bool
}

type peerStatus struct {
	Online bool
	LastOK time.Time
	Err    string
	// RTT — сколько шёл подписанный запрос обмена туда и обратно (без
	// установки SSH), мс; 0 — не мерили.
	RTT float64
}

func newServerNet(m *MeshManager) *ServerNet {
	n := &ServerNet{mesh: m, now: time.Now, peers: map[string]*peerStatus{}, kick: make(chan struct{}, 1),
		views: map[string]peerView{}, pulled: map[string]time.Time{}}
	n.dial = n.sshDial
	if m.dataDir != "" {
		n.path = filepath.Join(m.dataDir, "servernet.json")
		n.keyPath = filepath.Join(m.dataDir, "servernet", "id_ed25519")
		n.viewPath = filepath.Join(m.dataDir, "servernet-views.json")
	}
	n.load()
	n.loadViews()
	return n
}

func (n *ServerNet) load() {
	n.f = srvFile{Items: map[string]srvItem{}}
	if n.path != "" {
		if data, err := os.ReadFile(n.path); err == nil {
			json.Unmarshal(data, &n.f)
		}
	}
	if n.f.Items == nil {
		n.f.Items = map[string]srvItem{}
	}
	if n.f.Self == "" {
		n.f.Self = randomHex(8)
		n.saveLocked()
	}
}

func (n *ServerNet) saveLocked() {
	if n.path == "" {
		return
	}
	data, err := json.MarshalIndent(n.f, "", " ")
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(n.path), 0o700)
	tmp := n.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, n.path)
	}
}

// keyLocked — ключ связи этого сервера (заводится при первой нужде). На
// сервере, подключённом старым способом, ключ проброса и есть ключ связи:
// он уже разрешён на том сервере, к которому был проброс.
func (n *ServerNet) keyLocked() (ssh.Signer, error) {
	if n.signer != nil {
		return n.signer, nil
	}
	if n.keyPath != "" {
		if data, err := os.ReadFile(n.keyPath); err == nil {
			if s, err := ssh.ParsePrivateKey(data); err == nil {
				n.signer = s
				return s, nil
			}
		}
		if data, err := os.ReadFile(filepath.Join(n.mesh.linkDir(), "id_ed25519")); err == nil {
			if s, err := ssh.ParsePrivateKey(data); err == nil {
				n.signer = s
				n.writeKey(data)
				return s, nil
			}
		}
	}
	_, priv, err := generateKeyPair()
	if err != nil {
		return nil, err
	}
	s, err := ssh.ParsePrivateKey([]byte(priv))
	if err != nil {
		return nil, err
	}
	n.signer = s
	n.writeKey([]byte(priv))
	return s, nil
}

func (n *ServerNet) writeKey(data []byte) {
	if n.keyPath == "" {
		return
	}
	os.MkdirAll(filepath.Dir(n.keyPath), 0o700)
	os.WriteFile(n.keyPath, data, 0o600)
}

func pubLine(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// ---------- общие данные ----------

// InNet — сервер в сети серверов.
func (n *ServerNet) InNet() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.f.Net != ""
}

// stampLocked записывает правку этого сервера.
func (n *ServerNet) stampLocked(key string, del bool, d any) {
	it := srvItem{T: n.now().UnixMilli(), By: n.f.Self, Del: del}
	if old, ok := n.f.Items[key]; ok && it.T <= old.T {
		it.T = old.T + 1
	}
	if d != nil && !del {
		it.D, _ = json.Marshal(d)
	}
	n.f.Items[key] = it
}

// note — правка на этом сервере: записать и разослать. Сервер не в сети —
// ничего: при создании сети всё нужное соберётся заново (seedLocked).
func (n *ServerNet) note(key string, del bool, d any) {
	n.mu.Lock()
	if n.f.Net == "" || n.applying {
		n.mu.Unlock()
		return
	}
	n.stampLocked(key, del, d)
	n.saveLocked()
	n.mu.Unlock()
	n.poke()
}

func (n *ServerNet) poke() {
	select {
	case n.kick <- struct{}{}:
	default:
	}
}

// mergeLocked — чужие записи: берутся те, что новее. Возвращает ключи, что
// поменялись.
func (n *ServerNet) mergeLocked(items map[string]srvItem) []string {
	var changed []string
	for k, it := range items {
		if !validItemKey(k) || len(it.D) > 64<<10 {
			continue
		}
		if old, ok := n.f.Items[k]; ok && !it.newer(old) {
			continue
		}
		n.f.Items[k] = it
		changed = append(changed, k)
	}
	if len(changed) > 0 {
		n.saveLocked()
	}
	return changed
}

func validItemKey(k string) bool {
	if len(k) > 200 || k == "" {
		return false
	}
	if k == kAccess || k == kNewDev {
		return true
	}
	for _, p := range []string{kSrv, kAcct, kAppr, kNName, kNDel, kOK, kFrz, kAlias, kForget, kSName} {
		if strings.HasPrefix(k, p) && len(k) > len(p) {
			return true
		}
	}
	return false
}

func (n *ServerNet) snapshotLocked() map[string]srvItem {
	out := make(map[string]srvItem, len(n.f.Items))
	for k, v := range n.f.Items {
		out[k] = v
	}
	return out
}

// serversLocked — живые серверы сети (с этим), по id.
func (n *ServerNet) serversLocked() map[string]srvRec {
	out := map[string]srvRec{}
	for k, it := range n.f.Items {
		if !strings.HasPrefix(k, kSrv) || it.Del {
			continue
		}
		var r srvRec
		if json.Unmarshal(it.D, &r) == nil {
			id := strings.TrimPrefix(k, kSrv)
			r.Name = n.srvNameLocked(id, r)
			out[id] = r
		}
	}
	return out
}

func decodeItem[T any](it srvItem) (T, bool) {
	var v T
	if it.Del || json.Unmarshal(it.D, &v) != nil {
		return v, false
	}
	return v, true
}

// ---------- свой сервер ----------

// selfRec — как этот сервер выглядит для других серверов сети.
func (n *ServerNet) selfRec(signer ssh.Signer, meshID string) srvRec {
	s := n.mesh.settings.Get().Mesh
	host, port := n.selfHost(), n.mesh.sshPort
	if port == 0 {
		port = 22
	}
	return srvRec{Name: n.mesh.selfName(s), Host: host, Port: port, HostKeys: n.mesh.sys.HostKeys(),
		Key: pubLine(signer.PublicKey()), MeshID: meshID}
}

// selfHost — по какому адресу устройства и другие серверы приходят на этот
// сервер: адрес -ssh-host, если он снаружи сети устройств, иначе адрес из
// настроек, иначе первый внешний адрес машины. ТЗ 24: London был запущен с
// -ssh-host london.mesh (так его панель отдаёт конфиги клиентам) — и этот
// адрес уходил в сеть серверов; устройство, у которого есть только
// Германия, по нему до London не дойдёт.
func (n *ServerNet) selfHost() string {
	s := n.mesh.settings.Get().Mesh
	for _, h := range []string{n.mesh.sshHost, s.Host} {
		if h != "" && deviceReachable(h) {
			return h
		}
	}
	for _, a := range n.mesh.sys.LocalAddrs() {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil && !ip.IsPrivate() && deviceReachable(a) {
			return a
		}
	}
	return ""
}

// meshRange — адреса сети устройств: 198.19.0.0/16 у устройств,
// 198.18.0.0/16 у серверов.
var meshRange = func() *net.IPNet { _, r, _ := net.ParseCIDR("198.18.0.0/15"); return r }()

// deviceReachable — по такому адресу до сервера можно дойти, не будучи
// уже в его сети устройств: не имя .mesh, не адрес сети устройств, не петля.
func deviceReachable(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".mesh") {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return !ip.IsLoopback() && !ip.IsUnspecified() && !meshRange.Contains(ip)
	}
	// Имя без точки («London») — это имя сервера, а не адрес: снаружи его
	// никто не найдёт. Годится только полное имя вида uk.example.com.
	return strings.Contains(h, ".")
}

// meshID — id своего meshd (пусто — не отвечает).
func (n *ServerNet) meshID(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if st, err := n.mesh.admin.State(ctx); err == nil {
		return st.Self.ID
	}
	return ""
}

// ---------- подпись запросов ----------

const sigDomain = "sshtunnel-srvnet1"

func sigMessage(netID, from, path string, ts int64, body []byte) []byte {
	h := sha256.Sum256(body)
	return []byte(sigDomain + "\n" + netID + "\n" + from + "\n" + path + "\n" + strconv.FormatInt(ts, 10) + "\n" + hex.EncodeToString(h[:]))
}

func signRequest(req *http.Request, signer ssh.Signer, netID, from string, body []byte, now time.Time) error {
	ts := now.Unix()
	sig, err := signer.Sign(rand.Reader, sigMessage(netID, from, req.URL.Path, ts, body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Srv-Net", netID)
	req.Header.Set("X-Srv-From", from)
	req.Header.Set("X-Srv-Time", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Srv-Sig", base64.StdEncoding.EncodeToString(ssh.Marshal(sig)))
	return nil
}

// verifyRequest проверяет подпись запроса ключом pub.
func verifyRequest(r *http.Request, pub string, body []byte, now time.Time) error {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		return errors.New("нет ключа сервера")
	}
	ts, err := strconv.ParseInt(r.Header.Get("X-Srv-Time"), 10, 64)
	if err != nil {
		return errors.New("нет времени запроса")
	}
	// Часы серверов расходятся — 10 минут хватает с запасом.
	if d := now.Unix() - ts; d > 600 || d < -600 {
		return errors.New("часы серверов расходятся больше чем на 10 минут")
	}
	raw, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Srv-Sig"))
	if err != nil {
		return errors.New("нет подписи")
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(raw, &sig); err != nil {
		return errors.New("подпись повреждена")
	}
	msg := sigMessage(r.Header.Get("X-Srv-Net"), r.Header.Get("X-Srv-From"), r.URL.Path, ts, body)
	if err := key.Verify(msg, &sig); err != nil {
		return errors.New("подпись не сходится")
	}
	return nil
}

// ---------- код «Добавить сервер» ----------

type netCode struct {
	V    int      `json:"v"`
	Net  string   `json:"net"`
	ID   string   `json:"id"`
	Name string   `json:"name,omitempty"`
	Host string   `json:"host"`
	Port int      `json:"port"`
	HK   []string `json:"hk,omitempty"`
	// K — разовый ключ входа (32 байта семени ed25519), S — разовый секрет.
	K string `json:"k"`
	S string `json:"s"`
	E int64  `json:"e"` // до какого времени действует (unix)
}

func encodeNetCode(c netCode) string {
	data, _ := json.Marshal(c)
	return codePrefix + base64.RawURLEncoding.EncodeToString(data)
}

func decodeNetCode(code string) (netCode, ssh.Signer, error) {
	var c netCode
	code = strings.Join(strings.Fields(code), "")
	if strings.HasPrefix(code, invitePrefix) {
		return c, nil, errors.New("это код старого способа подключения — получи новый: на сервере сети «Сети» → «Серверы сети» → «Добавить сервер»")
	}
	if !strings.HasPrefix(code, codePrefix) {
		return c, nil, errors.New("это не код добавления сервера — он начинается с " + codePrefix)
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, codePrefix))
	if err != nil || json.Unmarshal(data, &c) != nil {
		return c, nil, errors.New("код повреждён — скопируй его целиком")
	}
	if c.Net == "" || c.ID == "" || c.S == "" || validHost(c.Host) != nil || c.Port <= 0 || c.Port > 65535 {
		return c, nil, errors.New("в коде не хватает данных")
	}
	seed, err := base64.RawURLEncoding.DecodeString(c.K)
	if err != nil || len(seed) != ed25519.SeedSize {
		return c, nil, errors.New("в коде испорчен ключ")
	}
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		return c, nil, errors.New("в коде испорчен ключ")
	}
	return c, signer, nil
}

func hashSecret(s string) string {
	h := sha256.Sum256([]byte("sshtunnel-invite\x00" + s))
	return hex.EncodeToString(h[:])
}

// ---------- запросы к другому серверу ----------

// call отправляет запрос серверу rec и разбирает ответ в out. sign — каким
// ключом подписать (nil — без подписи: разовые запросы по секрету кода).
func (n *ServerNet) call(ctx context.Context, rec srvRec, login ssh.Signer, sign bool, netID, path string, body, out any) error {
	return n.callTimed(ctx, rec, login, sign, netID, path, body, out, nil)
}

// callTimed — call, который ещё меряет, сколько шёл запрос: от отправки до
// первого байта ответа (SSH-рукопожатие не в счёт).
func (n *ServerNet) callTimed(ctx context.Context, rec srvRec, login ssh.Signer, sign bool, netID, path string, body, out any, rtt *time.Duration) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tr := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return n.dial(ctx, rec, login)
		},
	}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://srvnet"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if rtt != nil {
		var t0 time.Time
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			WroteRequest:         func(httptrace.WroteRequestInfo) { t0 = time.Now() },
			GotFirstResponseByte: func() { *rtt = time.Since(t0) },
		}))
	}
	if sign {
		n.mu.Lock()
		self := n.f.Self
		signer, kerr := n.keyLocked()
		n.mu.Unlock()
		if kerr != nil {
			return kerr
		}
		if err := signRequest(req, signer, netID, self, data, n.now()); err != nil {
			return err
		}
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return explainDial(rec, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSyncBody))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return &callError{Status: resp.StatusCode, Text: e.Error}
		}
		return &callError{Status: resp.StatusCode, Text: "сервер ответил " + resp.Status}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

type callError struct {
	Status int
	Text   string
}

func (e *callError) Error() string { return e.Text }

func explainDial(rec srvRec, err error) error {
	var ce *callError
	if errors.As(err, &ce) {
		return err
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		return fmt.Errorf("сервер %s не пускает: ключа этого сервера у него нет (код уже использован или истёк?)", rec.Host)
	case strings.Contains(msg, "connection refused") && strings.Contains(msg, strconv.Itoa(meshsvc.SyncPort)):
		return fmt.Errorf("на сервере %s панель не обновлена или не запущена", rec.Host)
	}
	return fmt.Errorf("нет связи с %s: %v", rec.Host, err)
}

// sshDial — настоящая связь: SSH к серверу rec под meshlink и проброс до
// его порта обмена. Ключ сервера проверяется по тому, что пришло в коде или
// в общих данных.
func (n *ServerNet) sshDial(ctx context.Context, rec srvRec, login ssh.Signer) (net.Conn, error) {
	var keys []ssh.PublicKey
	for _, k := range rec.HostKeys {
		if pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k)); err == nil {
			keys = append(keys, pk)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("неизвестны SSH-ключи сервера " + rec.Host)
	}
	cfg := &ssh.ClientConfig{
		User: meshsvc.LinkUser,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(login)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			for _, k := range keys {
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					return nil
				}
			}
			return errors.New("SSH-ключ сервера " + rec.Host + " не тот, что в сети — подмена?")
		},
		Timeout: 10 * time.Second,
	}
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(rec.Host, strconv.Itoa(rec.Port)))
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		raw.SetDeadline(dl)
	}
	c, chans, reqs, err := ssh.NewClientConn(raw, rec.Host, cfg)
	if err != nil {
		raw.Close()
		return nil, err
	}
	raw.SetDeadline(time.Time{})
	client := ssh.NewClient(c, chans, reqs)
	conn, err := client.Dial("tcp", "127.0.0.1:"+strconv.Itoa(meshsvc.SyncPort))
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("на сервере %s порт обмена не открыт: %v", rec.Host, err)
	}
	return &sshConn{Conn: conn, client: client}, nil
}

// sshConn закрывает SSH-соединение вместе с пробросом.
type sshConn struct {
	net.Conn
	client *ssh.Client
}

func (c *sshConn) Close() error {
	err := c.Conn.Close()
	c.client.Close()
	return err
}

// ---------- обмен ----------

type exchangeReq struct {
	Items map[string]srvItem `json:"items"`
	// View — живая картина звонящего (ТЗ 25): если до него самого не
	// достучаться, картина всё равно приходит с его обменом. Старая панель
	// её не шлёт и не читает.
	View *srvView `json:"view,omitempty"`
}

type exchangeResp struct {
	Net   string             `json:"net"`
	Items map[string]srvItem `json:"items"`
	// View — живая картина отвечающего: отдельный /srv/v1/view не нужен.
	View *srvView `json:"view,omitempty"`
}

// syncPeer — обмен с одним сервером.
func (n *ServerNet) syncPeer(ctx context.Context, id string, rec srvRec) {
	n.mu.Lock()
	netID := n.f.Net
	items := n.snapshotLocked()
	signer, err := n.keyLocked()
	n.mu.Unlock()
	if netID == "" || err != nil {
		return
	}
	var resp exchangeResp
	var rtt time.Duration
	mine := n.buildView(ctx)
	err = n.callTimed(ctx, rec, signer, true, netID, "/srv/v1/exchange", exchangeReq{Items: items, View: &mine}, &resp, &rtt)
	var ce *callError
	if errors.As(err, &ce) && ce.Status == http.StatusGone {
		// Нас убрали из сети: работаем дальше одни.
		n.leaveLocal(rec.Name)
		return
	}
	n.mu.Lock()
	st := n.peers[id]
	if st == nil {
		st = &peerStatus{}
		n.peers[id] = st
	}
	if err != nil {
		st.Online, st.Err = false, err.Error()
		n.mu.Unlock()
		return
	}
	st.Online, st.Err, st.LastOK = true, "", n.now()
	if rtt > 0 {
		st.RTT = float64(rtt.Microseconds()) / 1000
	}
	var changed []string
	if resp.Net == netID {
		changed = n.mergeLocked(resp.Items)
	}
	n.mu.Unlock()
	n.afterMerge(ctx, changed)
	// Живая картина того сервера — в ответе на обмен (ТЗ 25); старая панель
	// её не присылает — тогда отдельным запросом, как раньше (ТЗ 24).
	if resp.View == nil || !n.takeView(id, netID, *resp.View) {
		n.pullView(ctx, id, rec)
	}
}

// syncAll — обмен со всеми серверами сети сразу.
func (n *ServerNet) syncAll(ctx context.Context) {
	n.mu.Lock()
	servers := n.serversLocked()
	self := n.f.Self
	n.mu.Unlock()
	var wg sync.WaitGroup
	for id, rec := range servers {
		if id == self {
			continue
		}
		wg.Add(1)
		go func(id string, rec srvRec) {
			defer wg.Done()
			n.syncPeer(ctx, id, rec)
		}(id, rec)
	}
	wg.Wait()
}

// Run — обмен и сверка, пока не отменят ctx.
func (n *ServerNet) Run(ctx context.Context) {
	if err := n.startListener(); err != nil {
		n.mesh.logf("сеть серверов: порт обмена не открыт: %v", err)
	}
	t := time.NewTicker(syncEvery)
	defer t.Stop()
	go n.relayLoop(ctx)
	n.reconcile(ctx)
	for {
		if n.InNet() {
			n.syncAll(ctx)
		}
		n.reconcile(ctx)
		select {
		case <-ctx.Done():
			n.stopListener()
			return
		case <-t.C:
		case <-n.kick:
			// Несколько правок подряд — одним обменом.
			select {
			case <-ctx.Done():
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
}

// Start запускает сеть серверов в фоне.
func (n *ServerNet) Start() {
	n.mu.Lock()
	if n.cancel != nil {
		n.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel
	n.mu.Unlock()
	go n.Run(ctx)
}

// Stop останавливает сеть серверов.
func (n *ServerNet) Stop() {
	n.mu.Lock()
	cancel := n.cancel
	n.cancel = nil
	n.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (n *ServerNet) startListener() error {
	addr := n.listen
	if addr == "" {
		addr = "127.0.0.1:" + strconv.Itoa(meshsvc.SyncPort)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	n.mu.Lock()
	n.ln = ln
	n.mu.Unlock()
	srv := &http.Server{Handler: n.handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	return nil
}

func (n *ServerNet) stopListener() {
	n.mu.Lock()
	ln := n.ln
	n.ln = nil
	n.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
}

// handler — вход обмена для других серверов сети.
func (n *ServerNet) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/srv/v1/exchange", n.handleExchange)
	mux.HandleFunc("/srv/v1/preview", n.handlePreview)
	mux.HandleFunc("/srv/v1/pair", n.handlePair)
	mux.HandleFunc("/srv/v1/migrate", n.handleMigrate)
	mux.HandleFunc("/srv/v1/view", n.handleView)
	mux.HandleFunc("/srv/v1/exit", n.handleExit)
	mux.HandleFunc("/srv/v1/relay", n.handleRelay)
	return mux
}

func readSyncBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSyncBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return nil, false
	}
	return body, true
}

func (n *ServerNet) handleExchange(w http.ResponseWriter, r *http.Request) {
	body, ok := readSyncBody(w, r)
	if !ok {
		return
	}
	from, ok := n.authPeer(w, r, body)
	if !ok {
		return
	}
	var req exchangeReq
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	n.mu.Lock()
	changed := n.mergeLocked(req.Items)
	n.markPeerLocked(from)
	resp := exchangeResp{Net: n.f.Net, Items: n.snapshotLocked()}
	netID := n.f.Net
	n.mu.Unlock()
	// Звонящий на связи — значит, и его картина свежая (ТЗ 25, п. 7: раньше
	// «сверено» ставилось и от его звонка, а картину брали только своим
	// запросом к нему, который мог не проходить).
	if req.View != nil {
		n.takeView(from, netID, *req.View)
	}
	mine := n.buildView(r.Context())
	resp.View = &mine
	writeJSON(w, http.StatusOK, resp)
	go n.afterMerge(context.Background(), changed)
}

// markPeerLocked — сервер from только что достучался сюда: он на связи.
func (n *ServerNet) markPeerLocked(from string) {
	st := n.peers[from]
	if st == nil {
		st = &peerStatus{}
		n.peers[from] = st
	}
	st.Online, st.Err, st.LastOK = true, "", n.now()
}

// authPeer — запрос пришёл от сервера этой сети и подписан его ключом
// связи. Нет — отвечает сам и возвращает false.
func (n *ServerNet) authPeer(w http.ResponseWriter, r *http.Request, body []byte) (string, bool) {
	from := r.Header.Get("X-Srv-From")
	n.mu.Lock()
	netID := n.f.Net
	it, known := n.f.Items[kSrv+from]
	n.mu.Unlock()
	if netID == "" || r.Header.Get("X-Srv-Net") != netID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "этот сервер не в той сети"})
		return "", false
	}
	if !known {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "этот сервер ещё не знает о вас — повторите позже"})
		return "", false
	}
	if it.Del {
		writeJSON(w, http.StatusGone, map[string]string{"error": "сервер убран из сети"})
		return "", false
	}
	rec, _ := decodeItem[srvRec](it)
	if err := verifyRequest(r, rec.Key, body, n.now()); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return "", false
	}
	return from, true
}

// afterMerge — пришли чужие правки: разовые действия (убрать устройство,
// удалить сеть) выполняются один раз, остальное приводит сверка.
func (n *ServerNet) afterMerge(ctx context.Context, changed []string) {
	if len(changed) == 0 {
		return
	}
	n.mu.Lock()
	self := n.f.Self
	if it, ok := n.f.Items[kSrv+self]; ok && it.Del {
		by := n.nameOfLocked(it.By)
		n.mu.Unlock()
		n.leaveLocal(by)
		return
	}
	var once []string
	for _, k := range changed {
		it := n.f.Items[k]
		if it.By != self && (strings.HasPrefix(k, kForget) || strings.HasPrefix(k, kNDel)) {
			once = append(once, k)
		}
	}
	n.mu.Unlock()
	for _, k := range once {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if strings.HasPrefix(k, kForget) {
			netID, dev, _ := strings.Cut(strings.TrimPrefix(k, kForget), "/")
			n.mesh.admin.Forget(cctx, netID, dev)
		} else {
			n.mesh.admin.DeleteNetwork(cctx, strings.TrimPrefix(k, kNDel))
		}
		cancel()
	}
	n.reconcile(ctx)
	n.poke()
}

func (n *ServerNet) nameOfLocked(id string) string {
	if r, ok := n.serversLocked()[id]; ok {
		return n.srvNameLocked(id, r)
	}
	if it, ok := n.f.Items[kSrv+id]; ok {
		var r srvRec
		if json.Unmarshal(it.D, &r) == nil && r.Name != "" {
			return r.Name
		}
	}
	return "другой сервер сети"
}

// leaveLocal — этот сервер больше не в сети: данные (учётные записи,
// правила) остаются как есть, связь с другими серверами — нет.
func (n *ServerNet) leaveLocal(by string) {
	n.mu.Lock()
	if n.f.Net == "" {
		n.mu.Unlock()
		return
	}
	n.f.Net = ""
	n.f.Removed = by
	for k := range n.f.Items {
		if strings.HasPrefix(k, kSrv) {
			delete(n.f.Items, k)
		}
	}
	n.f.Invites = nil
	n.peers = map[string]*peerStatus{}
	n.views = map[string]peerView{}
	n.saveViewsLocked()
	n.saveLocked()
	n.mu.Unlock()
	n.mesh.logf("сеть серверов: этот сервер убран из сети (%s) — работает один", by)
	n.reconcile(context.Background())
}

// ---------- состояние для панели ----------

// SrvState — сервер в списке «Серверы сети».
type SrvState struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Host   string `json:"host"`
	Port   int    `json:"port,omitempty"`
	Self   bool   `json:"self,omitempty"`
	Online bool   `json:"online"`
	// LastSync — когда последний раз обменялись данными (unix); Error —
	// почему нет связи.
	LastSync int64  `json:"lastSync,omitempty"`
	Error    string `json:"error,omitempty"`
	// Waiting — подключён старым способом и ещё не перешёл («ждёт
	// перехода»).
	Waiting bool `json:"waiting,omitempty"`
	// RTTMs — задержка связи с ним по SSH (запрос обмена туда и обратно).
	RTTMs float64 `json:"rttMs,omitempty"`
	// HostBad — его адрес устройствам не годится (имя .mesh, адрес сети
	// устройств): устройства его сами не добавят (ТЗ 24).
	HostBad bool `json:"hostBad,omitempty"`
}

// SrvNetState — сеть серверов для панели.
type SrvNetState struct {
	// Net — id сети ("" — сервер один).
	Net     string     `json:"net,omitempty"`
	SelfID  string     `json:"selfId"`
	Servers []SrvState `json:"servers"`
	// Legacy — этот сервер подключён к другому старым способом (проброс):
	// панель показывает «Перейти».
	Legacy bool `json:"legacy,omitempty"`
	// Removed — этот сервер убрали из сети (кто).
	Removed string `json:"removed,omitempty"`
	// Invites — сколько выданных кодов ещё действуют.
	Invites int `json:"invites,omitempty"`
	// SSHHostInside — панель запущена с -ssh-host, по которому снаружи сети
	// устройств не дойти (london.mesh): в сеть серверов ушёл другой адрес.
	SSHHostInside string `json:"sshHostInside,omitempty"`
}

// State — сеть серверов для панели. links — серверы старого способа
// (подключённые к этому), с тем, на связи ли они.
func (n *ServerNet) State(links []SrvState) SrvNetState {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := SrvNetState{Net: n.f.Net, SelfID: n.f.Self, Servers: []SrvState{}, Removed: n.f.Removed}
	now := n.now().Unix()
	for _, inv := range n.f.Invites {
		if inv.Expires > now {
			st.Invites++
		}
	}
	s := n.mesh.settings.Get().Mesh
	st.Legacy = s.Role == MeshRoleSecondary
	self := SrvState{ID: n.f.Self, Name: n.mesh.selfName(s), Host: n.mesh.sshHost, Port: n.mesh.sshPort, Self: true,
		Online: n.mesh.sys.UnitState(meshdUnit).Active}
	var others []SrvState
	for id, r := range n.serversLocked() {
		if id == n.f.Self {
			self.Host, self.Port = r.Host, r.Port
			continue
		}
		ss := SrvState{ID: id, Name: n.srvNameLocked(id, r), Host: r.Host, Port: r.Port, HostBad: !deviceReachable(r.Host)}
		if p := n.peers[id]; p != nil {
			ss.Online, ss.Error, ss.RTTMs = p.Online, p.Err, p.RTT
			if !p.LastOK.IsZero() {
				ss.LastSync = p.LastOK.Unix()
			}
		}
		others = append(others, ss)
	}
	sort.Slice(others, func(i, j int) bool {
		if others[i].Name != others[j].Name {
			return others[i].Name < others[j].Name
		}
		return others[i].ID < others[j].ID
	})
	if h := n.mesh.sshHost; h != "" && !deviceReachable(h) {
		st.SSHHostInside = h
	}
	self.HostBad = self.Host != "" && !deviceReachable(self.Host)
	st.Servers = append(st.Servers, self)
	st.Servers = append(st.Servers, others...)
	st.Servers = append(st.Servers, links...)
	return st
}

// DirectHosts — адреса других серверов сети: связь с ними всегда напрямую,
// не через выход сервера (egress) — иначе без B пропала бы и сеть.
func (n *ServerNet) DirectHosts() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for id, r := range n.serversLocked() {
		if id != n.f.Self && r.Host != "" {
			out = append(out, r.Host)
		}
	}
	sort.Strings(out)
	return out
}

// constEq — сравнение секретов за постоянное время.
func constEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
