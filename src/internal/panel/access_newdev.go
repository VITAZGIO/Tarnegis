package panel

// Новое устройство в простом доступе (ТЗ 28): «видно всем и видит всех»
// или «без доступа, пока не отмечу». Выбор общий для сети серверов (запись
// newdev, новее — побеждает) и действует на устройства, которые появятся
// позже: уже поставленные галочки не меняются.
//
// Права у SSH-пользователя, поэтому «новое устройство» — новая учётная
// запись. При «видно всем» и включённых правилах она попадает в группу
// ~all, а в правилах есть «~all → *» и «* → ~all»: так ей можно ко всем и ко
// всем к ней, что бы ни было отмечено у остальных. Если в правах есть роль
// «Моё устройство» (~mine), учётная запись получает и её — в таблице у неё
// сразу роль. Первая же правка таблицы на странице переписывает это своими
// правилами (группа ~all — простого режима, как и роли). Правила выключены
// — все и так видят всех, ничего не добавляется. «Без доступа» — новая
// учётная запись без групп, а страница не пишет правил «→ *», которые
// открыли бы к ней путь.

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
)

const (
	NewDeviceAll  = "all"
	NewDeviceNone = "none"
	// groupNewAll — группа «новые: видны всем и видят всех».
	groupNewAll = "~all"
	groupMine   = "~mine"
)

func validNewDevice(m string) bool { return m == "" || m == NewDeviceAll || m == NewDeviceNone }

// newDeviceGrant дописывает в права группу ~all и её два правила (если их
// нет) и возвращает группы новой учётной записи. Права выключены — ничего.
func newDeviceGrant(a *AccessSettings, mode string, groups []string) []string {
	if mode != NewDeviceAll || !a.Enabled {
		return groups
	}
	if len(a.Groups) >= maxAccessGroups && !slices.Contains(a.Groups, groupNewAll) {
		return groups
	}
	if !slices.Contains(a.Groups, groupNewAll) {
		a.Groups = append(a.Groups, groupNewAll)
		slices.Sort(a.Groups)
	}
	for _, r := range []AccessRule{{From: groupNewAll, To: GroupAll}, {From: GroupAll, To: groupNewAll}} {
		if !slices.Contains(a.Rules, r) && len(a.Rules) < maxAccessRules {
			a.Rules = append(a.Rules, r)
		}
	}
	out := append([]string(nil), groups...)
	add := []string{groupNewAll}
	if slices.Contains(a.Groups, groupMine) {
		add = append(add, groupMine)
	}
	for _, g := range add {
		if !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	slices.Sort(out)
	return out
}

// grantNewDevice — только что заведённая здесь учётная запись получает права
// по выбору «Новое устройство». Учётные записи с других серверов приходят
// уже с группами (их выдал тот сервер), сюда не попадают.
func (s *Server) grantNewDevice(ctx context.Context, c Client) {
	if s.settings == nil || s.clients == nil {
		return
	}
	accessMu.Lock()
	defer accessMu.Unlock()
	st := s.settings.Get()
	a := st.Access
	a.Groups = append([]string(nil), a.Groups...)
	a.Rules = append([]AccessRule(nil), a.Rules...)
	groups := newDeviceGrant(&a, st.NewDevice, c.Groups)
	if slices.Equal(groups, c.Groups) {
		return
	}
	if err := s.settings.Update(func(st *Settings) error { st.Access = a; return nil }); err != nil {
		log.Printf("новое устройство: права не записаны: %v", err)
		return
	}
	if err := s.clients.Update(c.ID, func(cl *Client) { cl.Groups = groups }); err != nil {
		log.Printf("новое устройство: группы %s не записаны: %v", c.Username, err)
	}
	if s.mesh != nil {
		s.mesh.net.noteAccess(a)
	}
	if err := s.pushPolicy(ctx); err != nil {
		log.Printf("права доступа: meshd не принял политику: %v", err)
	}
}

// handleNewDevice: GET — {"mode"}; POST {"mode":"all"|"none"} — выбрать.
func (s *Server) handleNewDevice(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "settings_unavailable"})
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]string{"mode": s.settings.Get().NewDevice})
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	if req.Mode != NewDeviceAll && req.Mode != NewDeviceNone {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errBadNewDevice.Error()})
		return
	}
	if err := s.settings.Update(func(st *Settings) error { st.NewDevice = req.Mode; return nil }); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s.mesh != nil {
		s.mesh.net.noteNewDevice(req.Mode)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": req.Mode})
}

var errBadNewDevice = errors.New("новое устройство: «all» (видно всем) или «none» (без доступа)")

// ---------- сеть серверов ----------

// kNewDev — общий выбор «Новое устройство».
const kNewDev = "newdev"

type newDevRec struct {
	Mode string `json:"mode"`
}

func (n *ServerNet) noteNewDevice(mode string) { n.note(kNewDev, false, newDevRec{Mode: mode}) }

// applyNewDevice — выбор из сети; здешний, которого в сети ещё нет, уходит
// в сеть.
func (n *ServerNet) applyNewDevice(items map[string]srvItem) {
	if n.srv == nil || n.srv.settings == nil {
		return
	}
	cur := n.srv.settings.Get().NewDevice
	it, ok := items[kNewDev]
	if !ok {
		if cur != "" {
			n.noteNewDevice(cur)
		}
		return
	}
	rec, ok := decodeItem[newDevRec](it)
	if !ok || !validNewDevice(rec.Mode) || rec.Mode == cur {
		return
	}
	if err := n.srv.settings.Update(func(st *Settings) error { st.NewDevice = rec.Mode; return nil }); err != nil {
		n.mesh.logf("сеть серверов: «новое устройство» не записано: %v", err)
	}
}
