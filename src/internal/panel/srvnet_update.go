package panel

// «Обновить на всех серверах» (ТЗ 31, часть C).
//
// Как устроено:
//   - кнопка на одном сервере кладёт в общие данные запись upd:<версия>
//     (кто положил — By) и сразу шлёт каждому серверу сети подписанный
//     POST /srv/v1/update с этой записью — тем же ключом связи и той же
//     связкой SSH, что и обмен;
//   - каждый сервер обновляет себя сам, своим обычным путём: панель — как
//     кнопка «Обновить» (startSelfUpdate: файл только из релиза проекта на
//     GitHub, контрольная сумма из SHA256SUMS.txt того же релиза), потом,
//     уже новой панелью, meshd — как кнопка «Переустановить meshd». Никто не
//     качает за другого;
//   - сервер без связи получает запись со следующим обменом (общие данные) и
//     обновляется тогда же: отдельной очереди нет;
//   - сервер, на котором нажали, обновляется последним: ждёт, пока другие
//     на связи закончат (или сломаются), но не дольше updSelfWait;
//   - только на версию новее своей; сломалось на одном — остальные идут
//     дальше, сам сервер повторит попытку не раньше чем через updRetry;
//   - ход обновления каждый сервер кладёт в свою живую картину (upd, panel,
//     meshdRel) — страница видит его у всех.
//
// Устройства обновление не рвёт иначе, чем ручная кнопка: перезапуск meshd
// они переживают сами (переподключаются, как после обрыва связи).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	kUpd = "upd:"
	// updRetry — не чаще раза в столько повторять сломавшееся обновление.
	updRetry = 30 * time.Minute
	// updSelfWait — сколько сервер, на котором нажали, ждёт остальных.
	updSelfWait = 15 * time.Minute
	// maxVersionLen — длина номера версии в записи.
	maxVersionLen = 32
)

// Шаги «обновить на всех» в картине и на странице.
const (
	updWaiting = "waiting" // ждёт своей очереди (этот сервер — последним)
	updMeshd   = "meshd"   // панель новая, ставится meshd
	updDone    = "done"
	updError   = "error"
	updManual  = "manual" // сборка не из релиза — только вручную
)

// updRec — запись upd:<версия>.
type updRec struct {
	Target string `json:"target"`
}

// updState — как идёт обновление на сервере (поле upd картины).
type updState struct {
	Target string `json:"target"`
	// Stage — waiting / download / verify / install / restart / meshd /
	// done / error / manual.
	Stage string `json:"stage"`
	Error string `json:"error,omitempty"`
}

// updLocal — своё обновление (в памяти: после перезапуска панели всё видно
// заново по версиям).
type updLocal struct {
	target     string
	stage      string
	err        string
	tried      time.Time // когда последний раз запускали обновление панели
	meshdTried time.Time
	loop       bool // идёт быстрый круг после нажатия
}

// cleanVersion — «v1.10.7» → «1.10.7»; "" — не номер версии (цифры через
// точку, хвост «-beta» можно).
func cleanVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" || len(v) > maxVersionLen {
		return ""
	}
	num, tail, _ := strings.Cut(v, "-")
	for _, p := range strings.Split(num, ".") {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return ""
		}
	}
	if strings.Trim(tail, "0123456789abcdefghijklmnopqrstuvwxyz.") != "" {
		return ""
	}
	return v
}

// updTargetLocked — самая новая запись upd: (версия, кто положил, когда).
func (n *ServerNet) updTargetLocked() (target, by string, t int64) {
	for k, it := range n.f.Items {
		if !strings.HasPrefix(k, kUpd) || it.Del {
			continue
		}
		r, ok := decodeItem[updRec](it)
		if !ok || cleanVersion(r.Target) == "" || r.Target != strings.TrimPrefix(k, kUpd) {
			continue
		}
		if target == "" || versionLess(target, r.Target) {
			target, by, t = r.Target, it.By, it.T
		}
	}
	return
}

// panelVersion — версия этой панели ("" / "dev" — сборка не из релиза).
func (n *ServerNet) panelVersion() string {
	if n.panelVer != nil {
		return strings.TrimPrefix(n.panelVer(), "v")
	}
	return strings.TrimPrefix(n.mesh.version, "v")
}

// selfUpdateFn — запустить обновление этой панели (в тестах — подделка).
func (n *ServerNet) selfUpdateFn() error {
	if n.startUpdate != nil {
		return n.startUpdate()
	}
	if n.srv == nil {
		return errors.New("панели нечего обновлять")
	}
	return n.srv.startSelfUpdate()
}

// selfUpdateNow — идёт ли обновление этой панели и на каком шаге.
func (n *ServerNet) selfUpdateNow() (running bool, stage, errText string) {
	if n.updProgress != nil {
		return n.updProgress()
	}
	if n.srv == nil {
		return false, "", ""
	}
	return n.srv.selfUpdateState()
}

// meshdWanted — meshd на этом сервере есть и он не из релиза панели.
func (n *ServerNet) meshdWanted() bool {
	m := n.mesh
	if m.settings.Get().Mesh.Role != MeshRoleMain && !m.sys.UnitState(meshdUnit).Installed {
		return false
	}
	if n.meshdLatest != nil {
		return !n.meshdLatest()
	}
	return !m.meshdIsLatest()
}

// meshdUpdateFn — поставить meshd из релиза этой панели (как кнопка).
func (n *ServerNet) meshdUpdateFn() error {
	if n.meshdUpdate != nil {
		return n.meshdUpdate()
	}
	_, err := n.mesh.ReinstallIfNeeded()
	return err
}

// updTick — посмотреть, не пора ли обновиться. Зовётся каждым кругом обмена
// и, после нажатия, чаще (updLoop).
func (n *ServerNet) updTick(ctx context.Context) {
	n.updMu.Lock()
	defer n.updMu.Unlock()
	n.mu.Lock()
	target, by, t := n.updTargetLocked()
	self := n.f.Self
	if target == "" || n.f.Net == "" {
		n.mu.Unlock()
		return
	}
	if n.upd.target != target {
		n.upd = updLocal{target: target, loop: n.upd.loop}
	}
	n.mu.Unlock()

	ver := n.panelVersion()
	if cleanVersion(ver) == "" {
		n.setUpd(updManual, "сборка не из релиза — обнови этот сервер вручную")
		return
	}
	if versionLess(ver, target) {
		running, stage, errText := n.selfUpdateNow()
		if running {
			n.setUpd(stage, "")
			return
		}
		if by == self && !n.othersDone(target, t) {
			n.setUpd(updWaiting, "")
			return
		}
		n.mu.Lock()
		tried := n.upd.tried
		n.mu.Unlock()
		if !tried.IsZero() && n.now().Sub(tried) < updRetry {
			if errText != "" {
				n.setUpd(updError, errText)
			}
			return
		}
		n.mu.Lock()
		n.upd.tried = n.now()
		n.mu.Unlock()
		if err := n.selfUpdateFn(); err != nil && !errors.Is(err, errUpdateRunning) {
			n.setUpd(updError, err.Error())
			n.mesh.logf("обновить на всех: панель не обновилась: %v", err)
			return
		}
		n.mesh.logf("обновить на всех: обновляю панель до %s (просил %s)", target, n.nameOf(by))
		n.setUpd("download", "")
		return
	}
	// Панель уже новая — теперь meshd (новой панелью, из её релиза).
	if j := n.mesh.jobSnapshot(); j != nil && j.Running && j.Kind == "install" {
		n.setUpd(updMeshd, "")
		return
	}
	if !n.meshdWanted() {
		n.setUpd(updDone, "")
		return
	}
	n.mu.Lock()
	tried := n.upd.meshdTried
	n.mu.Unlock()
	if !tried.IsZero() && n.now().Sub(tried) < updRetry {
		if j := n.mesh.jobSnapshot(); j != nil && j.Error != "" {
			n.setUpd(updError, "meshd: "+j.Error)
		}
		return
	}
	n.mu.Lock()
	n.upd.meshdTried = n.now()
	n.mu.Unlock()
	if err := n.meshdUpdateFn(); err != nil && !errors.Is(err, errJobRunning) {
		n.setUpd(updError, "meshd: "+err.Error())
		return
	}
	n.setUpd(updMeshd, "")
}

func (n *ServerNet) setUpd(stage, errText string) {
	n.mu.Lock()
	n.upd.stage, n.upd.err = stage, errText
	n.mu.Unlock()
}

func (n *ServerNet) nameOf(id string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if id == n.f.Self {
		return "этот сервер"
	}
	return n.nameOfLocked(id)
}

// updViewLocked — своё обновление для картины (nil — обновлять нечего).
func (n *ServerNet) updViewLocked() *updState {
	if n.upd.target == "" || n.upd.stage == "" {
		return nil
	}
	return &updState{Target: n.upd.target, Stage: n.upd.stage, Error: n.upd.err}
}

// peerUpdLocked — как идёт обновление до target на другом сервере, по его
// картине: шаг и текст ошибки.
func (n *ServerNet) peerUpdLocked(id, target string, now int64) (stage, errText string) {
	pv, have := n.views[id]
	p := n.peers[id]
	online := p != nil && p.Online
	if !have || pv.View.Panel == "" {
		// Картины нет или в ней нет версии панели: старая панель, сама она
		// «обновить на всех» не поймёт.
		if !online {
			return "offline", ""
		}
		return "old", ""
	}
	v := pv.View
	newer := !versionLess(v.Panel, target)
	var u *updState
	if v.Upd != nil && v.Upd.Target == target {
		u = v.Upd
	}
	if n.liveLocked(id, pv, now) {
		switch {
		case u != nil:
			return u.Stage, u.Error
		case newer:
			return updDone, ""
		}
		return updWaiting, "" // запрос ещё не дошёл до него
	}
	switch {
	case u != nil && (u.Stage == "download" || u.Stage == "verify" || u.Stage == "install" || u.Stage == "restart"):
		return "restart", "" // перезапускается
	case newer && (u == nil || u.Stage == updDone):
		return updDone, ""
	}
	return "offline", ""
}

// othersDone — все остальные серверы, которые на связи, закончили (или
// сломались, или не умеют); или ждём уже дольше updSelfWait.
func (n *ServerNet) othersDone(target string, t int64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	if now.Sub(time.UnixMilli(t)) > updSelfWait {
		return true
	}
	for id := range n.serversLocked() {
		if id == n.f.Self {
			continue
		}
		switch st, _ := n.peerUpdLocked(id, target, now.Unix()); st {
		case updDone, updError, updManual, "offline", "old":
		default:
			return false
		}
	}
	return true
}

// ---------- нажали «Обновить на всех серверах» ----------

// UpdateAll — обновить все серверы сети до target: запись в общие данные и
// сразу запрос каждому серверу на связи. Этот сервер — последним.
func (n *ServerNet) UpdateAll(ctx context.Context, target string) error {
	target = cleanVersion(target)
	if target == "" {
		return errors.New("неверный номер версии")
	}
	n.mu.Lock()
	if n.f.Net == "" {
		n.mu.Unlock()
		return errors.New("этот сервер не в сети серверов — обнови его кнопкой «Обновить»")
	}
	// Старые записи — надгробиями: в силе одна, самая новая.
	for k, it := range n.f.Items {
		if strings.HasPrefix(k, kUpd) && !it.Del && k != kUpd+target {
			n.stampLocked(k, true, nil)
		}
	}
	n.stampLocked(kUpd+target, false, updRec{Target: target})
	item := n.f.Items[kUpd+target]
	n.saveLocked()
	servers := n.serversLocked()
	netID, self := n.f.Net, n.f.Self
	signer, err := n.keyLocked()
	n.upd = updLocal{target: target, stage: updWaiting}
	n.mu.Unlock()
	if err != nil {
		return err
	}
	for id, rec := range servers {
		if id == self {
			continue
		}
		go func(id string, rec srvRec) {
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var out updState
			err := n.call(cctx, rec, signer, true, netID, "/srv/v1/update",
				updReq{Items: map[string]srvItem{kUpd + target: item}}, &out)
			if err != nil {
				// Не на связи или старая панель — получит запись обменом.
				n.mesh.logf("обновить на всех: %s получит запрос позже: %v", rec.Name, err)
			}
			n.RefreshViews()
		}(id, rec)
	}
	n.poke()
	if !n.updLoopOff {
		go n.updLoop()
	}
	return nil
}

// updLoop — после нажатия смотреть чаще, чем раз в 30 секунд обмена.
func (n *ServerNet) updLoop() {
	n.mu.Lock()
	if n.upd.loop {
		n.mu.Unlock()
		return
	}
	n.upd.loop = true
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		n.upd.loop = false
		n.mu.Unlock()
	}()
	deadline := time.Now().Add(updSelfWait + 5*time.Minute)
	for time.Now().Before(deadline) {
		n.RefreshViews()
		n.updTick(context.Background())
		n.mu.Lock()
		st := n.upd.stage
		n.mu.Unlock()
		if st != updWaiting && st != "" {
			return // своё обновление пошло (или кончилось) — дальше обычный круг
		}
		time.Sleep(5 * time.Second)
	}
}

// updReq — тело /srv/v1/update.
type updReq struct {
	Items map[string]srvItem `json:"items"`
}

// handleUpdate — /srv/v1/update: другой сервер сети просит обновиться.
// Только подписанный запрос сервера этой сети; только запись upd:.
func (n *ServerNet) handleUpdate(w http.ResponseWriter, r *http.Request) {
	body, ok := readSyncBody(w, r)
	if !ok {
		return
	}
	from, ok := n.authPeer(w, r, body)
	if !ok {
		return
	}
	var req updReq
	if err := json.Unmarshal(body, &req); err != nil || len(req.Items) == 0 || len(req.Items) > 4 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный запрос обновления"})
		return
	}
	for k := range req.Items {
		if !strings.HasPrefix(k, kUpd) || cleanVersion(strings.TrimPrefix(k, kUpd)) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный запрос обновления"})
			return
		}
	}
	n.mu.Lock()
	n.mergeLocked(req.Items)
	n.markPeerLocked(from)
	target, _, _ := n.updTargetLocked()
	n.mu.Unlock()
	ver := n.panelVersion()
	if target == "" || cleanVersion(ver) != "" && !versionLess(ver, target) && !n.meshdWanted() {
		// Не новее своей — делать нечего.
		writeJSON(w, http.StatusOK, updState{Target: target, Stage: updDone})
		return
	}
	go n.updTick(context.Background())
	writeJSON(w, http.StatusOK, updState{Target: target, Stage: "download"})
}

// ---------- для страницы ----------

// UpdSrv — сервер в списке «Обновить на всех серверах».
type UpdSrv struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Self   bool   `json:"self,omitempty"`
	Online bool   `json:"online"`
	// Panel — версия панели, Meshd — номер протокола meshd, MeshdRel — из
	// какого релиза meshd ("" — неизвестно).
	Panel    string `json:"panel,omitempty"`
	Meshd    string `json:"meshd,omitempty"`
	MeshdRel string `json:"meshdRel,omitempty"`
	// Stage — idle (обновлять не просили) / waiting / download / verify /
	// install / restart / meshd / done / error / offline («обновится, когда
	// вернётся») / old (старая панель — обнови вручную) / manual.
	Stage string `json:"stage"`
	Error string `json:"error,omitempty"`
}

// UpdAll — ответ GET /api/update/all.
type UpdAll struct {
	// Target — до какой версии обновляем ("" — не просили).
	Target  string   `json:"target,omitempty"`
	InNet   bool     `json:"inNet"`
	Servers []UpdSrv `json:"servers"`
}

// UpdateAllState — версии и ход обновления всех серверов сети.
func (n *ServerNet) UpdateAllState(ctx context.Context) UpdAll {
	meshVer, meshRel := "", ""
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	if ms, err := n.mesh.admin.State(cctx); err == nil {
		meshVer, meshRel = ms.Version, ms.Release
	}
	cancel()
	n.mu.Lock()
	defer n.mu.Unlock()
	target, _, _ := n.updTargetLocked()
	out := UpdAll{Target: target, InNet: n.f.Net != "", Servers: []UpdSrv{}}
	s := n.mesh.settings.Get().Mesh
	self := UpdSrv{ID: n.f.Self, Name: n.mesh.selfName(s), Self: true, Online: true, Panel: n.panelVersion(),
		Meshd: meshVer, MeshdRel: meshRel, Stage: "idle"}
	if target != "" {
		self.Stage = updWaiting
		if u := n.updViewLocked(); u != nil && u.Target == target {
			self.Stage, self.Error = u.Stage, u.Error
		} else if !versionLess(self.Panel, target) {
			self.Stage = updDone
		}
	}
	out.Servers = append(out.Servers, self)
	if n.f.Net == "" {
		return out
	}
	now := n.now().Unix()
	var others []UpdSrv
	for id, r := range n.serversLocked() {
		if id == n.f.Self {
			continue
		}
		u := UpdSrv{ID: id, Name: r.Name, Stage: "idle"}
		if p := n.peers[id]; p != nil {
			u.Online = p.Online
		}
		if pv, ok := n.views[id]; ok {
			u.Panel, u.Meshd, u.MeshdRel = pv.View.Panel, pv.View.Version, pv.View.MeshdRel
		}
		if target != "" {
			u.Stage, u.Error = n.peerUpdLocked(id, target, now)
		} else if !u.Online {
			u.Stage = "offline"
		}
		others = append(others, u)
	}
	sort.Slice(others, func(i, j int) bool {
		if others[i].Name != others[j].Name {
			return others[i].Name < others[j].Name
		}
		return others[i].ID < others[j].ID
	})
	out.Servers = append(out.Servers, others...)
	return out
}
