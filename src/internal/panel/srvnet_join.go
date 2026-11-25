package panel

// Сеть серверов: код «Добавить сервер», «Войти в сеть», «Убрать из сети» и
// переход со старого способа (проброс к другому серверу).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/meshsvc"
)

// Invite — код «Добавить сервер»: одноразовый, на час. Сервер ещё ни с кем
// не связан — с этого кода начинается сеть (из того, что уже есть на нём).
func (n *ServerNet) Invite(ctx context.Context) (string, time.Time, error) {
	if !n.mesh.sys.UnitState(meshdUnit).Active {
		return "", time.Time{}, errors.New("сначала включи сеть устройств на этом сервере («Настройки» → «Сеть устройств на этом сервере»)")
	}
	meshID := n.meshID(ctx)
	if err := n.ensureNet(ctx, meshID); err != nil {
		return "", time.Time{}, err
	}
	seed := make([]byte, ed25519.SeedSize)
	rand.Read(seed)
	login, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		return "", time.Time{}, err
	}
	secret := randomHex(16)
	exp := n.now().Add(inviteTTL)

	n.mu.Lock()
	signer, err := n.keyLocked()
	if err != nil {
		n.mu.Unlock()
		return "", time.Time{}, err
	}
	if len(n.serversLocked()) >= maxServers {
		n.mu.Unlock()
		return "", time.Time{}, fmt.Errorf("в сети уже %d серверов — больше нельзя", maxServers)
	}
	now := n.now().Unix()
	live := n.f.Invites[:0]
	for _, inv := range n.f.Invites {
		if inv.Expires > now {
			live = append(live, inv)
		}
	}
	if len(live) >= 8 {
		live = live[1:]
	}
	n.f.Invites = append(live, srvInvite{Hash: hashSecret(secret), Pub: pubLine(login.PublicKey()), Expires: exp.Unix()})
	netID, self := n.f.Net, n.f.Self
	n.saveLocked()
	n.mu.Unlock()

	rec := n.selfRec(signer, meshID)
	if err := validHost(rec.Host); err != nil {
		return "", time.Time{}, errors.New("панель не знает адреса этого сервера — запусти её с -ssh-host")
	}
	if err := n.mesh.syncLinks(); err != nil {
		return "", time.Time{}, fmt.Errorf("разовый ключ не записан: %w", err)
	}
	code := encodeNetCode(netCode{V: 1, Net: netID, ID: self, Name: rec.Name, Host: rec.Host, Port: rec.Port,
		HK: rec.HostKeys, K: base64.RawURLEncoding.EncodeToString(seed), S: secret, E: exp.Unix()})
	return code, exp, nil
}

// ensureNet — сеть есть; нет — заводится из того, что на этом сервере.
func (n *ServerNet) ensureNet(ctx context.Context, meshID string) error {
	n.mu.Lock()
	if n.f.Net != "" {
		n.mu.Unlock()
		return nil
	}
	signer, err := n.keyLocked()
	n.mu.Unlock()
	if err != nil {
		return err
	}
	rec := n.selfRec(signer, meshID)
	seed := n.collectLocal(ctx)

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.f.Net != "" {
		return nil
	}
	n.f.Net = randomHex(16)
	n.f.Removed = ""
	n.f.Items = map[string]srvItem{}
	n.stampLocked(kSrv+n.f.Self, false, rec)
	for k, v := range seed {
		n.stampLocked(k, false, v)
	}
	n.saveLocked()
	return nil
}

// collectLocal — всё общее, что есть на этом сервере: учётные записи,
// правила, а из meshd — одобрения, заморозки, имена.
func (n *ServerNet) collectLocal(ctx context.Context) map[string]any {
	out := map[string]any{}
	if n.srv != nil {
		name := n.mesh.selfName(n.mesh.settings.Get().Mesh)
		for _, c := range n.srv.clients.List() {
			out[kAcct+c.Username] = recOfClient(c, name)
		}
		if n.srv.settings != nil {
			out[kAccess] = n.srv.settings.Get().Access
		}
	}
	// Сеть панели этого сервера — в общие данные новой сети (входит в чужую
	// сеть — остаётся заведённый раньше, panelnet.go).
	n.mu.Lock()
	if r := n.loadPanelNetLocked(); r.valid() {
		out[kMeshKey] = r
	}
	n.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ms, err := n.mesh.admin.State(ctx)
	if err != nil {
		return out
	}
	for _, nw := range ms.Networks {
		if nw.Approval {
			out[kAppr+nw.ID] = onRec{On: true}
		}
		if nw.Name != "" {
			out[kNName+nw.ID] = nameRec{Name: nw.Name}
		}
		for _, d := range nw.Devices {
			if nw.Approval && !d.Pending {
				out[devKey(kOK, nw.ID, d.ID)] = struct{}{}
			}
			if d.Frozen {
				out[devKey(kFrz, nw.ID, d.ID)] = onRec{On: true}
			}
			if d.Alias != "" {
				out[devKey(kAlias, nw.ID, d.ID)] = aliasRec{Alias: d.Alias}
			}
		}
	}
	return out
}

// ---------- разовые запросы по коду ----------

type previewReq struct {
	S string `json:"s"`
}

type pairReq struct {
	S  string `json:"s"`
	ID string `json:"id"`
	// Rec — новый сервер; Accounts — его учётные записи (объединяются с
	// учётными записями сети).
	Rec      srvRec             `json:"rec"`
	Accounts map[string]acctRec `json:"accounts,omitempty"`
}

type migrateReq struct {
	Link     string             `json:"link"`
	ID       string             `json:"id"`
	Rec      srvRec             `json:"rec"`
	Accounts map[string]acctRec `json:"accounts,omitempty"`
}

// inviteLocked — действующий код с этим секретом (индекс) или -1.
func (n *ServerNet) inviteLocked(secret string) int {
	h, now := hashSecret(secret), n.now().Unix()
	for i, inv := range n.f.Invites {
		if constEq(inv.Hash, h) && inv.Expires > now {
			return i
		}
	}
	return -1
}

func (n *ServerNet) handlePreview(w http.ResponseWriter, r *http.Request) {
	body, ok := readSyncBody(w, r)
	if !ok {
		return
	}
	var req previewReq
	json.Unmarshal(body, &req)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.f.Net == "" || n.inviteLocked(req.S) < 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "код уже использован или истёк — получи новый"})
		return
	}
	writeJSON(w, http.StatusOK, exchangeResp{Net: n.f.Net, Items: n.snapshotLocked()})
}

func (n *ServerNet) handlePair(w http.ResponseWriter, r *http.Request) {
	body, ok := readSyncBody(w, r)
	if !ok {
		return
	}
	var req pairReq
	if json.Unmarshal(body, &req) != nil || !validID(req.ID) || req.Rec.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	n.mu.Lock()
	i := -1
	if n.f.Net != "" {
		i = n.inviteLocked(req.S)
	}
	if i < 0 {
		n.mu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "код уже использован или истёк — получи новый"})
		return
	}
	// Код одноразовый: разовый ключ уходит из authorized_keys сразу.
	n.f.Invites = slices.Delete(n.f.Invites, i, i+1)
	resp, err := n.admitLocked(req.ID, req.Rec, req.Accounts)
	n.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
	go n.afterAdmit()
}

// admitLocked принимает сервер в сеть: запись о нём и объединение учётных
// записей (одно имя с разными ключами — остаются оба).
func (n *ServerNet) admitLocked(id string, rec srvRec, accounts map[string]acctRec) (exchangeResp, error) {
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(rec.Key)); err != nil {
		return exchangeResp{}, errors.New("неверный ключ связи")
	}
	if validHost(rec.Host) != nil || rec.Port < 1 || rec.Port > 65535 {
		return exchangeResp{}, errors.New("неверный адрес нового сервера")
	}
	if id == n.f.Self {
		return exchangeResp{}, errors.New("это код этого же сервера")
	}
	if old, ok := n.f.Items[kSrv+id]; !ok || old.Del {
		if len(n.serversLocked()) >= maxServers {
			return exchangeResp{}, fmt.Errorf("в сети уже %d серверов — больше нельзя", maxServers)
		}
	}
	rec.Name = cleanName(rec.Name)
	n.stampLocked(kSrv+id, false, rec)
	for user, a := range accounts {
		if !ValidUsername(user) {
			continue
		}
		key := kAcct + user
		cur, ok := n.f.Items[key]
		if !ok {
			n.stampLocked(key, false, a)
			continue
		}
		have, live := decodeItem[acctRec](cur)
		if !live {
			// В сети эта запись удалена — новый сервер её не вернёт.
			continue
		}
		merged := cleanKeys(append(slices.Clone(have.Keys), a.Keys...))
		if !slices.Equal(merged, have.Keys) {
			have.Keys = merged
			n.stampLocked(key, false, have)
		}
	}
	n.saveLocked()
	return exchangeResp{Net: n.f.Net, Items: n.snapshotLocked()}, nil
}

func validID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// afterAdmit — новый сервер принят: ключи meshlink и рассылка остальным.
func (n *ServerNet) afterAdmit() {
	n.mesh.syncLinks()
	n.reconcile(context.Background())
	n.poke()
}

// handleMigrate — сервер старого способа (проброс к этому) переходит в сеть
// серверов: входит по своему ключу проброса и подписывает им же.
func (n *ServerNet) handleMigrate(w http.ResponseWriter, r *http.Request) {
	body, ok := readSyncBody(w, r)
	if !ok {
		return
	}
	var req migrateReq
	if json.Unmarshal(body, &req) != nil || !validID(req.ID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	var link *MeshLink
	for _, l := range n.mesh.settings.Get().Mesh.Links {
		if l.ID == req.Link {
			l := l
			link = &l
		}
	}
	if link == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "этот сервер не подключён сюда старым способом"})
		return
	}
	if err := verifyRequest(r, link.PublicKey, body, n.now()); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	if err := n.ensureNet(r.Context(), n.meshID(r.Context())); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req.Rec.Key = link.PublicKey
	if req.Rec.Name == "" {
		req.Rec.Name = link.Name
	}
	n.mu.Lock()
	resp, err := n.admitLocked(req.ID, req.Rec, req.Accounts)
	n.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Старый способ для него закончен: его ключ остаётся — уже как ключ
	// связи сервера сети.
	n.mesh.settings.Update(func(s *Settings) error {
		out := s.Mesh.Links[:0]
		for _, l := range s.Mesh.Links {
			if l.ID != req.Link {
				out = append(out, l)
			}
		}
		s.Mesh.Links = out
		return nil
	})
	writeJSON(w, http.StatusOK, resp)
	go n.afterAdmit()
}

// ---------- новый сервер: «Войти в сеть» ----------

// JoinPreview — что будет, если войти в сеть по коду: для окна
// подтверждения (ничего не меняет, код не тратит).
type JoinPreview struct {
	Servers []string `json:"servers"`
	// Rules — сколько правил доступа здесь заменится, NetRules — сколько их
	// в сети; Holds — сколько своих одобрений, заморозок и имён заменится.
	Rules    int `json:"rules"`
	NetRules int `json:"netRules"`
	Holds    int `json:"holds"`
	// AddAccounts — сколько учётных записей добавится сюда, GiveAccounts —
	// сколько здешних уйдёт в сеть.
	AddAccounts  int `json:"addAccounts"`
	GiveAccounts int `json:"giveAccounts"`
}

// Preview спрашивает сервер из кода, что в сети, и считает, что поменяется.
func (n *ServerNet) Preview(ctx context.Context, code string) (JoinPreview, error) {
	c, login, err := decodeNetCode(code)
	if err != nil {
		return JoinPreview{}, err
	}
	if err := n.canJoin(c); err != nil {
		return JoinPreview{}, err
	}
	var resp exchangeResp
	if err := n.call(ctx, codeRec(c), login, false, c.Net, "/srv/v1/preview", previewReq{S: c.S}, &resp); err != nil {
		return JoinPreview{}, err
	}
	return n.preview(ctx, resp.Items), nil
}

func codeRec(c netCode) srvRec {
	return srvRec{Name: c.Name, Host: c.Host, Port: c.Port, HostKeys: c.HK}
}

// canJoin — можно ли этому серверу войти по такому коду.
func (n *ServerNet) canJoin(c netCode) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if c.ID == n.f.Self {
		return errors.New("это код этого же сервера — вставь его в панели другого сервера")
	}
	// Тот же номер сети, но других серверов этот сервер не знает: его
	// убрали из сети, пока он был без связи, и он об этом не узнал. Такой
	// сервер входит заново.
	if n.f.Net != "" && n.f.Net == c.Net && len(n.serversLocked()) > 1 {
		return errors.New("этот сервер уже в этой сети")
	}
	if n.f.Net != "" && len(n.serversLocked()) > 1 {
		return errors.New("этот сервер уже в другой сети серверов — сначала «Убрать из сети» на нём самом")
	}
	if c.E > 0 && c.E < n.now().Unix() {
		return errors.New("код истёк — получи новый")
	}
	return nil
}

func (n *ServerNet) preview(ctx context.Context, items map[string]srvItem) JoinPreview {
	p := JoinPreview{Servers: []string{}}
	netAccts := map[string]bool{}
	for k, it := range items {
		switch {
		case strings.HasPrefix(k, kSrv) && !it.Del:
			if r, ok := decodeItem[srvRec](it); ok {
				p.Servers = append(p.Servers, r.Name)
			}
		case strings.HasPrefix(k, kAcct) && !it.Del:
			netAccts[strings.TrimPrefix(k, kAcct)] = true
		case k == kAccess:
			if a, ok := decodeItem[AccessSettings](it); ok {
				p.NetRules = len(a.Rules)
			}
		}
	}
	slices.Sort(p.Servers)
	local := map[string]bool{}
	if n.srv != nil {
		for _, c := range n.srv.clients.List() {
			local[c.Username] = true
			if !netAccts[c.Username] {
				p.GiveAccounts++
			}
		}
		if n.srv.settings != nil {
			p.Rules = len(n.srv.settings.Get().Access.Rules)
		}
	}
	for u := range netAccts {
		if !local[u] {
			p.AddAccounts++
		}
	}
	for k, v := range n.collectLocal(ctx) {
		if strings.HasPrefix(k, kAcct) || k == kAccess || strings.HasPrefix(k, kOK) || k == kMeshKey {
			continue
		}
		want, _ := json.Marshal(v)
		if it, ok := items[k]; !ok || it.Del || string(it.D) != string(want) {
			p.Holds++
		}
	}
	return p
}

// Join — «Войти в сеть»: ставит или обновляет meshd, входит по коду и
// забирает общие данные сети. Идёт заданием (его видно в панели).
func (n *ServerNet) Join(code string) error {
	c, login, err := decodeNetCode(code)
	if err != nil {
		return err
	}
	if err := n.canJoin(c); err != nil {
		return err
	}
	return n.mesh.runJob("join", func(onLine func(string)) error {
		if err := n.mesh.ensureOwnMeshd(onLine); err != nil {
			return err
		}
		ctx := context.Background()
		onLine("Вхожу в сеть через " + c.Host)
		req, err := n.joinRequest(ctx)
		if err != nil {
			return err
		}
		var resp exchangeResp
		err = n.call(ctx, codeRec(c), login, false, c.Net, "/srv/v1/pair",
			pairReq{S: c.S, ID: req.ID, Rec: req.Rec, Accounts: req.Accounts}, &resp)
		if err != nil {
			return err
		}
		return n.adopt(ctx, resp, onLine)
	})
}

// joinRequest — что этот сервер рассказывает о себе при входе.
func (n *ServerNet) joinRequest(ctx context.Context) (pairReq, error) {
	n.mu.Lock()
	signer, err := n.keyLocked()
	self := n.f.Self
	n.mu.Unlock()
	if err != nil {
		return pairReq{}, err
	}
	rec := n.selfRec(signer, n.meshID(ctx))
	if err := validHost(rec.Host); err != nil {
		return pairReq{}, errors.New("панель не знает адреса этого сервера — запусти её с -ssh-host")
	}
	req := pairReq{ID: self, Rec: rec, Accounts: map[string]acctRec{}}
	if n.srv != nil {
		for _, c := range n.srv.clients.List() {
			req.Accounts[c.Username] = recOfClient(c, rec.Name)
		}
	}
	return req, nil
}

// adopt — этот сервер принят: общие данные сети заменяют здешние правила,
// одобрения, заморозки и имена (учётные записи уже объединены).
func (n *ServerNet) adopt(ctx context.Context, resp exchangeResp, onLine func(string)) error {
	if resp.Net == "" {
		return errors.New("сервер не прислал данных сети")
	}
	local := n.collectLocal(ctx)
	n.mu.Lock()
	n.f.Net = resp.Net
	n.f.Removed = ""
	n.f.Items = map[string]srvItem{}
	n.mergeLocked(resp.Items)
	n.f.Invites = nil
	n.saveLocked()
	n.mu.Unlock()
	onLine("Данные сети получены — привожу этот сервер к ним")
	n.replaceLocal(ctx, local)
	n.mesh.syncLinks()
	n.reconcile(ctx)
	n.poke()
	onLine("Готово: сервер в сети, правила и учётные записи общие")
	return nil
}

// replaceLocal — одобрения, заморозки и имена сетей этого сервера, которых
// в сети нет, сбрасываются: «берутся правила той сети, куда входишь».
func (n *ServerNet) replaceLocal(ctx context.Context, local map[string]any) {
	n.mu.Lock()
	items := n.snapshotLocked()
	n.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for k := range local {
		if _, ok := items[k]; ok {
			continue
		}
		switch {
		case strings.HasPrefix(k, kAppr):
			n.mesh.admin.SetApproval(ctx, strings.TrimPrefix(k, kAppr), false)
		case strings.HasPrefix(k, kNName):
			n.mesh.admin.RenameNetwork(ctx, strings.TrimPrefix(k, kNName), "")
		case strings.HasPrefix(k, kFrz):
			netID, dev, _ := strings.Cut(strings.TrimPrefix(k, kFrz), "/")
			n.mesh.admin.Freeze(ctx, netID, dev, false)
		}
	}
}

// ---------- «Убрать из сети» ----------

// Remove убирает сервер id из сети (себя — «выйти»). Остальные забывают его
// и закрывают ему вход; убранный работает дальше один со своими данными.
func (n *ServerNet) Remove(ctx context.Context, id string) error {
	n.mu.Lock()
	if n.f.Net == "" {
		n.mu.Unlock()
		return errors.New("этот сервер ни с кем не связан")
	}
	servers := n.serversLocked()
	rec, ok := servers[id]
	if !ok {
		n.mu.Unlock()
		return errors.New("нет такого сервера в сети")
	}
	self := n.f.Self
	n.stampLocked(kSrv+id, true, nil)
	n.saveLocked()
	n.mu.Unlock()

	if id == self {
		// Уходим сами: сказать остальным и остаться одним.
		n.syncAll(ctx)
		n.leaveLocal(n.mesh.selfName(n.mesh.settings.Get().Mesh))
		n.mesh.syncLinks()
		n.mesh.sys.KillLinkSessions()
		return nil
	}
	// Убираемому — сначала ему самому (пока он ещё доверяет нам), потом
	// всем остальным.
	n.syncPeer(ctx, id, rec)
	n.mu.Lock()
	delete(n.peers, id)
	n.mu.Unlock()
	n.mesh.syncLinks()
	n.mesh.sys.KillLinkSessions()
	n.reconcile(ctx)
	n.poke()
	return nil
}

// ---------- переход со старого способа ----------

// Migrate — сервер, подключённый старым способом (проброс к другому),
// переходит на свою службу сети и входит в сеть серверов того сервера, к
// которому был проброс: его ключ там уже разрешён.
func (n *ServerNet) Migrate() error {
	s := n.mesh.settings.Get().Mesh
	if s.Role != MeshRoleSecondary {
		return errors.New("этот сервер подключён не старым способом")
	}
	dir := n.mesh.linkDir()
	keyData, err := os.ReadFile(filepath.Join(dir, "id_ed25519"))
	if err != nil {
		return errors.New("нет ключа старого подключения — войди в сеть по новому коду")
	}
	login, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return errors.New("ключ старого подключения испорчен — войди в сеть по новому коду")
	}
	var hostKeys []string
	if data, err := os.ReadFile(filepath.Join(dir, "known_hosts")); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if f := strings.Fields(l); len(f) >= 3 {
				hostKeys = append(hostKeys, f[1]+" "+f[2])
			}
		}
	}
	mainRec := srvRec{Name: "", Host: s.MainHost, Port: s.MainPort, HostKeys: hostKeys}
	n.mu.Lock()
	n.signer = login // ключ проброса становится ключом связи
	n.writeKey(keyData)
	n.mu.Unlock()

	n.mesh.mu.Lock()
	oldIP := ""
	if n.mesh.link != nil {
		oldIP = n.mesh.link.State().MeshIP
	}
	n.mesh.mu.Unlock()
	return n.mesh.runJob("join", func(onLine func(string)) error {
		if err := n.mesh.becomeOwn(onLine, oldIP); err != nil {
			return err
		}
		ctx := context.Background()
		req, err := n.joinRequest(ctx)
		if err != nil {
			return err
		}
		onLine("Вхожу в сеть серверов через " + s.MainHost)
		var resp exchangeResp
		err = n.call(ctx, mainRec, login, true, "", "/srv/v1/migrate",
			migrateReq{Link: s.LinkID, ID: req.ID, Rec: req.Rec, Accounts: req.Accounts}, &resp)
		if err != nil {
			return fmt.Errorf("свою службу сети сервер получил, но в сеть не вошёл: %w — получи код «Добавить сервер» и нажми «Войти в сеть»", err)
		}
		return n.adopt(ctx, resp, onLine)
	})
}

// linkLines — строки authorized_keys пользователя meshlink: серверы сети,
// действующие разовые ключи кодов и серверы старого способа.
func (n *ServerNet) linkLines(links []MeshLink) []string {
	var lines []string
	for _, l := range links {
		lines = append(lines, meshsvc.LinkAuthorizedKey(l.PublicKey, l.Name))
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.f.Net == "" {
		return lines
	}
	ids := []string{}
	servers := n.serversLocked()
	for id := range servers {
		if id != n.f.Self {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		if r := servers[id]; r.Key != "" {
			lines = append(lines, meshsvc.PeerAuthorizedKey(r.Key, r.Name))
		}
	}
	now := n.now().Unix()
	for _, inv := range n.f.Invites {
		if inv.Expires > now {
			lines = append(lines, meshsvc.PeerAuthorizedKey(inv.Pub, "invite"))
		}
	}
	return lines
}
