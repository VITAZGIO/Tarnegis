package panel

// Устройства из панели (ТЗ 47), серверная часть: на каких серверах сети
// есть учётная запись и есть ли у неё сеть устройств, приглашения, вход по
// приглашению (join) и профиль устройства (/api/me/profile). Строка
// приглашения и хранение секретов — devinvite.go, пользователь tun_join и
// sshd — joinsys.go.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/ssh"
)

// devServer — сервер сети для галочек «Серверы».
type devServer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Self bool   `json:"self,omitempty"`
}

func (s *Server) selfServerID() string {
	if s.mesh == nil {
		return ""
	}
	return s.mesh.net.SelfID()
}

// devServers — серверы, на которые можно пустить устройство: вся сеть
// серверов или один этот сервер. Без meshd-части (часть тестов) — пусто:
// список серверов тогда не ведётся.
func (s *Server) devServers() []devServer {
	if s.mesh == nil {
		return nil
	}
	n := s.mesh.net
	n.mu.Lock()
	self := n.f.Self
	var out []devServer
	if n.f.Net != "" {
		for id, r := range n.serversLocked() {
			out = append(out, devServer{ID: id, Name: r.Name, Self: id == self})
		}
	}
	n.mu.Unlock()
	if len(out) == 0 {
		out = []devServer{{ID: self, Name: s.mesh.selfName(s.mesh.settings.Get().Mesh), Self: true}}
	}
	slices.SortFunc(out, func(a, b devServer) int {
		if a.Self != b.Self {
			if a.Self {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// checkServers — список серверов от страницы: только известные, хоть один.
// Все серверы у записи без списка — так и остаётся «на всех» (и на тех,
// что придут потом).
func (s *Server) checkServers(in []string, was []string, existing bool) ([]string, error) {
	known := s.devServers()
	if len(known) == 0 {
		return nil, nil
	}
	if in == nil {
		return nil, nil
	}
	out := cleanServers(in)
	for _, id := range out {
		if !slices.ContainsFunc(known, func(k devServer) bool { return k.ID == id }) {
			return nil, fmt.Errorf("нет такого сервера в сети: %s", id)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("отметь хотя бы один сервер")
	}
	if existing && was == nil && len(out) == len(known) {
		return nil, nil
	}
	return out, nil
}

// ---------- учётные записи сети, которых здесь нет ----------

// netAccounts — учётные записи сети (без удалённых).
func (n *ServerNet) netAccounts() map[string]acctRec {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[string]acctRec{}
	if n.f.Net == "" {
		return out
	}
	for k, it := range n.f.Items {
		if !strings.HasPrefix(k, kAcct) {
			continue
		}
		if rec, ok := decodeItem[acctRec](it); ok {
			out[strings.TrimPrefix(k, kAcct)] = rec
		}
	}
	return out
}

// noteAcct — правка учётной записи, которой на этом сервере нет.
func (n *ServerNet) noteAcct(user string, rec *acctRec) {
	if rec == nil {
		n.note(kAcct+user, true, nil)
		return
	}
	n.note(kAcct+user, false, *rec)
}

// elsewhereClients — учётные записи сети, которым этот сервер не разрешён:
// в «Устройствах» они есть (их можно пустить сюда, поменять, удалить), но
// пользователя здесь нет.
func (s *Server) elsewhereClients() []Client {
	if s.mesh == nil {
		return nil
	}
	var out []Client
	for u, rec := range s.mesh.net.netAccounts() {
		if _, ok := s.clients.Get(u); ok || !ValidUsername(u) {
			continue
		}
		c := Client{ID: u, Username: u, Name: rec.Name, DeviceType: rec.Type, CreatedAt: rec.Created,
			State: StateActive, Groups: rec.Groups, Origin: rec.Origin, Servers: cleanServers(rec.Servers), NoMesh: rec.NoMesh}
		if rec.Off {
			c.State = StateFrozen
		}
		out = append(out, c)
	}
	return out
}

// ---------- серверы и сеть устройств у устройства ----------

// setDeviceScope — «Серверы» и «Сеть устройств» в профиле устройства.
// Учётная запись приходит на разрешённые серверы и уходит с остальных сама
// (сверка сети серверов, srvnet_apply.go); здесь — сразу.
func (s *Server) setDeviceScope(ctx context.Context, user string, servers []string, mesh bool) error {
	self := s.selfServerID()
	if c, ok := s.clients.Get(user); ok {
		list, err := s.checkServers(servers, c.Servers, true)
		if err != nil {
			return err
		}
		if err := s.clients.Update(user, func(c *Client) { c.Servers, c.NoMesh = list, !mesh }); err != nil {
			return err
		}
		if self != "" && !allowedOn(list, self) {
			if err := s.clients.dropLocal(user); err != nil {
				return err
			}
		}
	} else {
		if s.mesh == nil {
			return fmt.Errorf("нет устройства %q", user)
		}
		rec, ok := s.mesh.net.netAccounts()[user]
		if !ok {
			return fmt.Errorf("нет устройства %q", user)
		}
		list, err := s.checkServers(servers, cleanServers(rec.Servers), true)
		if err != nil {
			return err
		}
		rec.Servers, rec.NoMesh = list, !mesh
		s.mesh.net.noteAcct(user, &rec)
		if allowedOn(list, self) {
			if err := s.clients.ApplyShared(user, &rec); err != nil {
				return err
			}
		}
	}
	if err := s.pushPolicy(ctx); err != nil {
		log.Printf("права доступа: meshd не принял политику: %v", err)
	}
	return nil
}

// deleteDevice — «Удалить устройство»: на всех серверах сети (те, что не на
// связи, — при следующем обмене).
func (s *Server) deleteDevice(user string) error {
	if _, ok := s.clients.Get(user); ok {
		return s.clients.DeleteClient(user)
	}
	if s.mesh != nil {
		if _, ok := s.mesh.net.netAccounts()[user]; ok {
			s.mesh.net.noteAcct(user, nil)
		}
	}
	return nil
}

// ---------- приглашения ----------

func (s *Server) inviteStore() *inviteStore {
	s.invMu.Lock()
	defer s.invMu.Unlock()
	if s.invites == nil {
		path := ""
		if s.dataDir != "" {
			path = filepath.Join(s.dataDir, "device-invites.json")
		}
		s.invites = openInviteStore(path)
	}
	return s.invites
}

// joinHost — куда программе идти с приглашением: публичный SSH-адрес этого
// сервера (и когда сама панель открыта только локально).
func (s *Server) joinHost() (string, int) {
	host, port := s.sshHost, s.sshPort
	if s.mesh != nil {
		if h := s.mesh.net.selfHost(); h != "" {
			host = h
		}
		if s.mesh.sshPort != 0 {
			port = s.mesh.sshPort
		}
	}
	if port == 0 {
		port = 22
	}
	return host, port
}

func (s *Server) hostKeys() []string {
	if s.hostKeysFn != nil {
		return s.hostKeysFn()
	}
	if s.mesh != nil {
		return s.mesh.sys.HostKeys()
	}
	return systemMesh{}.HostKeys()
}

// inviteReq — «Добавить устройство» / «Новое приглашение».
type inviteReq struct {
	Name       string   `json:"name"`
	DeviceType string   `json:"deviceType"`
	Servers    []string `json:"servers"`
	Mesh       *bool    `json:"mesh"`
	User       string   `json:"user"` // новое приглашение уже заведённому
}

// inviteResult — ответ: строка приглашения (один раз), её QR и куда она ведёт.
type inviteResult struct {
	Invite inviteView `json:"invite"`
	Text   string     `json:"text"`
	QR     string     `json:"qrPngBase64,omitempty"`
	Host   string     `json:"host"`
	Port   int        `json:"port"`
}

func (s *Server) createInvite(req inviteReq) (inviteResult, error) {
	host, port := s.joinHost()
	if host == "" {
		return inviteResult{}, errors.New("не задан внешний адрес сервера для подключения " +
			"(флаг -ssh-host или -domain при запуске панели)")
	}
	hk := preferredHostKey(s.hostKeys())
	if hk == "" {
		return inviteResult{}, errors.New("не нашёл SSH-ключ этого сервера (/etc/ssh/ssh_host_*_key.pub)")
	}
	d := devInvite{Name: strings.TrimSpace(req.Name), Type: DeviceType(req.DeviceType), NoMesh: req.Mesh != nil && !*req.Mesh}
	if req.User != "" {
		c, ok := s.clients.Get(req.User)
		if !ok {
			return inviteResult{}, errors.New("устройству нельзя на этот сервер — выдай приглашение на сервере из его списка " +
				"или отметь здесь этот сервер")
		}
		if c.State == StateFrozen {
			return inviteResult{}, errors.New("устройство заморожено — сначала разморозь")
		}
		d.User, d.Name, d.Type, d.Servers, d.NoMesh = c.ID, c.Name, c.DeviceType, c.Servers, c.NoMesh
	} else {
		if d.Name == "" {
			return inviteResult{}, errors.New("имя устройства не может быть пустым")
		}
		if len([]rune(d.Name)) > maxNameLength {
			return inviteResult{}, fmt.Errorf("имя устройства длиннее %d символов", maxNameLength)
		}
		if !validDeviceType(d.Type) {
			return inviteResult{}, fmt.Errorf("неизвестный тип устройства: %q", req.DeviceType)
		}
		// Этот сервер в списке всегда: устройство входит через него.
		servers := req.Servers
		if self := s.selfServerID(); self != "" && servers != nil && !slices.Contains(servers, self) {
			servers = append(slices.Clone(servers), self)
		}
		list, err := s.checkServers(servers, nil, false)
		if err != nil {
			return inviteResult{}, err
		}
		if servers == nil && s.selfServerID() != "" {
			list = []string{s.selfServerID()}
		}
		d.Servers = list
	}
	tok, d, err := s.inviteStore().add(d)
	if err != nil {
		return inviteResult{}, err
	}
	text := encodeDevInvite(inviteDoc{V: 1, Host: host, Port: port, HK: hk, Tok: tok, Exp: d.Expires, Name: d.Name})
	res := inviteResult{Invite: d.view(), Text: text, Host: host, Port: port}
	if png, err := qrcode.Encode(text, qrcode.Low, 512); err == nil {
		res.QR = base64.StdEncoding.EncodeToString(png)
	}
	return res, nil
}

// ---------- вход по приглашению ----------

// joinReq — что пересылает команда join (joinsys.go): запрос программы и
// то, что известно sshd.
type joinReq struct {
	Tok  string `json:"tok"`
	Name string `json:"name"`
	Type string `json:"type"`
	Key  string `json:"key"`  // ключ, которым программа вошла (SSH_USER_AUTH)
	From string `json:"from"` // адрес программы (SSH_CONNECTION)
}

type joinResp struct {
	OK   bool   `json:"ok"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Err  string `json:"err,omitempty"`
}

// redeem — принять приглашение. Отказы пишутся строкой «ssh_tunnel join:
// refused (<код>) from <адрес>» — по ней fail2ban (joinsys.go).
func (s *Server) redeem(ctx context.Context, req joinReq) joinResp {
	from := strings.TrimSpace(req.From)
	if from == "" {
		from = "?"
	}
	key := "join|" + from
	fail := func(code, why string) joinResp {
		s.limiter.Fail(key)
		log.Printf("ssh_tunnel join: refused (%s) from %s", code, from)
		if why != "" {
			log.Printf("приглашение устройства: отказ %s: %s", from, why)
		}
		return joinResp{Err: code}
	}
	if locked, _ := s.limiter.Before(key); locked {
		log.Printf("ssh_tunnel join: refused (%s) from %s", joinLimit, from)
		return joinResp{Err: joinLimit}
	}
	if len(req.Tok) < 16 || len(req.Tok) > 128 {
		return fail(joinUnknown, "")
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req.Key)); err != nil {
		return fail(joinUnknown, "нет годного ключа входа")
	}
	inv, code := s.inviteStore().take(req.Tok)
	if code != "" {
		return fail(code, "")
	}
	var c Client
	if inv.User != "" {
		cur, ok := s.clients.Get(inv.User)
		if !ok {
			return fail(joinUnknown, "устройство уже удалено")
		}
		if cur.State == StateFrozen {
			s.inviteStore().release(inv.ID)
			return fail(joinFrozen, "")
		}
		if err := s.clients.AddKey(cur.ID, req.Key); err != nil {
			s.inviteStore().release(inv.ID)
			return fail(joinUnknown, err.Error())
		}
		c, _ = s.clients.Get(cur.ID)
	} else {
		name := strings.TrimSpace(req.Name)
		if name == "" || len([]rune(name)) > maxNameLength {
			name = inv.Name
		}
		dt := joinDeviceType(req.Type)
		if dt == "" {
			dt = inv.Type
		}
		var err error
		c, err = s.clients.CreateClientWithKey(name, dt, req.Key, func(c *Client) {
			c.Servers, c.NoMesh = slices.Clone(inv.Servers), inv.NoMesh
		})
		if err != nil {
			s.inviteStore().release(inv.ID)
			return fail(joinUnknown, err.Error())
		}
		s.grantNewDevice(ctx, c)
		if fresh, ok := s.clients.Get(c.ID); ok {
			c = fresh
		}
		if err := s.pushPolicy(ctx); err != nil {
			log.Printf("права доступа: meshd не принял политику: %v", err)
		}
	}
	s.inviteStore().markUsed(inv.ID, c.ID)
	s.limiter.Success(key)
	log.Printf("приглашение устройства: %s (%s) вошло с %s", c.Name, c.ID, from)
	return joinResp{OK: true, ID: c.ID, Name: c.Name}
}

// joinCallerOK — запрос /v1/join пришёл от команды join (пользователь
// tun_join), а не от программы устройства.
func (s *Server) joinCallerOK(r *http.Request) bool {
	find := s.connUID
	if find == nil {
		find = procTCPUID
	}
	uid, ok := find(r)
	if !ok {
		return false
	}
	want := s.joinUID
	if want == nil {
		want = func() (int, bool) {
			u, err := user.Lookup(JoinUser)
			if err != nil {
				return 0, false
			}
			n, err := strconv.Atoi(u.Uid)
			return n, err == nil
		}
	}
	w, ok := want()
	return ok && w != 0 && uid == w
}

// ---------- профиль устройства ----------

type profileServer struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
	HK   string `json:"hk"`
	Flag string `json:"flag,omitempty"`
}

// deviceProfile — ответ /api/me/profile (контракт ТЗ 47 ↔ 48, раздел C).
type deviceProfile struct {
	V       int             `json:"v"`
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Rev     int64           `json:"rev"`
	Servers []profileServer `json:"servers"`
	Mesh    bool            `json:"mesh"`
	Frozen  bool            `json:"frozen"`
}

// netProfile — серверы сети, разрешённые записи, и rev по общим данным:
// наибольшее время правки её записи и записей серверов (новый сервер в сети,
// сменился адрес или ключ — профиль тоже поменялся). ok=false — сервер не в
// сети серверов.
func (n *ServerNet) netProfile(user string, servers []string) (list []profileServer, rev int64, ok bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.f.Net == "" {
		return nil, 0, false
	}
	for k, it := range n.f.Items {
		if k == kAcct+user || strings.HasPrefix(k, kSrv) || strings.HasPrefix(k, kSName) {
			rev = max(rev, it.T)
		}
	}
	self := n.f.Self
	all := n.serversLocked()
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		r := all[id]
		if !allowedOn(servers, id) {
			continue
		}
		host := r.Host
		if id == self && !deviceReachable(host) {
			host = n.selfHost()
		}
		if validHost(host) != nil || !deviceReachable(host) {
			continue
		}
		port := r.Port
		if port == 0 {
			port = 22
		}
		list = append(list, profileServer{Name: r.Name, Host: host, Port: port, HK: preferredHostKey(r.HostKeys)})
	}
	return list, rev, true
}

// profileOf — профиль устройства. rev: в сети серверов — по общим данным
// (одинаковый на всех серверах), иначе — Rev записи здесь.
func (s *Server) profileOf(c Client) deviceProfile {
	p := deviceProfile{V: 1, ID: c.ID, Name: c.Name, Rev: c.Rev, Mesh: !c.NoMesh, Frozen: c.State == StateFrozen,
		Servers: []profileServer{}}
	if s.mesh != nil {
		if list, rev, ok := s.mesh.net.netProfile(c.Username, c.Servers); ok {
			p.Servers = append(p.Servers, list...)
			p.Rev = rev
			return p
		}
	}
	if host, port := s.joinHost(); host != "" {
		name := ""
		if s.mesh != nil {
			name = s.mesh.selfName(s.mesh.settings.Get().Mesh)
		}
		p.Servers = append(p.Servers, profileServer{Name: name, Host: host, Port: port, HK: preferredHostKey(s.hostKeys())})
	}
	return p
}

// ---------- ручки панели ----------

func (s *Server) registryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/devices/servers", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		list := s.devServers()
		if list == nil {
			list = []devServer{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"servers": list})
	}))
	mux.HandleFunc("/api/devices/invite", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		var req inviteReq
		if !decodePost(w, r, &req) {
			return
		}
		res, err := s.createInvite(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
	}))
	mux.HandleFunc("/api/devices/invites", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"invites": s.inviteStore().open()})
	}))
	mux.HandleFunc("/api/devices/invite/revoke", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		if !s.inviteStore().revoke(req.ID) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нет такого приглашения"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("/api/clients/scope", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID      string   `json:"id"`
			Servers []string `json:"servers"`
			Mesh    bool     `json:"mesh"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		replyErr(w, s.setDeviceScope(r.Context(), req.ID, req.Servers, req.Mesh), map[string]bool{"ok": true})
	}))
	mux.HandleFunc("/api/clients/key/remove", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		replyErr(w, s.clients.RemoveKey(req.ID, req.Key), map[string]bool{"ok": true})
	}))
}
