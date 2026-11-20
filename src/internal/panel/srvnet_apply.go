package panel

// Сеть серверов: правки этого сервера уходят в общие данные (note*), а общие
// данные приводят к себе meshd, учётные записи и правила этого сервера
// (reconcile). Сверка идёт после каждого обмена и раз в 30 секунд: устройство,
// которое одобрили на другом сервере, здесь одобряется, как только появится.

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"sshtunnel/internal/meshsvc"
)

// ---------- правки на этом сервере ----------

func (n *ServerNet) noteClient(c Client, deleted bool) {
	if deleted {
		n.note(kAcct+c.Username, true, nil)
		return
	}
	n.note(kAcct+c.Username, false, recOfClient(c, n.mesh.selfName(n.mesh.settings.Get().Mesh)))
}

func (n *ServerNet) noteAccess(a AccessSettings) { n.note(kAccess, false, a) }

func (n *ServerNet) noteApproval(netID string, on bool) { n.note(kAppr+netID, false, onRec{On: on}) }

func (n *ServerNet) noteApprove(netID, dev string) {
	n.note(devKey(kOK, netID, dev), false, struct{}{})
}

func (n *ServerNet) noteFreeze(netID, dev string, on bool) {
	n.note(devKey(kFrz, netID, dev), false, onRec{On: on})
}

func (n *ServerNet) noteAlias(netID, dev, alias string) {
	n.note(devKey(kAlias, netID, dev), false, aliasRec{Alias: alias})
}

func (n *ServerNet) noteNetName(netID, name string) { n.note(kNName+netID, false, nameRec{Name: name}) }

// noteForget — устройство убрано: разовое действие для всех серверов; его
// одобрение, заморозка и имя из панели уходят вместе с ним (вернётся —
// начнёт заново).
func (n *ServerNet) noteForget(netID, dev string) {
	n.mu.Lock()
	if n.f.Net == "" {
		n.mu.Unlock()
		return
	}
	n.stampLocked(devKey(kForget, netID, dev), false, struct{}{})
	for _, p := range []string{kOK, kFrz, kAlias} {
		if _, ok := n.f.Items[devKey(p, netID, dev)]; ok {
			n.stampLocked(devKey(p, netID, dev), true, nil)
		}
	}
	n.saveLocked()
	n.mu.Unlock()
	n.poke()
}

// noteNetDelete — сеть удалена: разовое действие; всё о ней уходит.
func (n *ServerNet) noteNetDelete(netID string) {
	n.mu.Lock()
	if n.f.Net == "" {
		n.mu.Unlock()
		return
	}
	n.stampLocked(kNDel+netID, false, struct{}{})
	for k, it := range n.f.Items {
		if it.Del {
			continue
		}
		if k == kAppr+netID || k == kNName+netID {
			n.stampLocked(k, true, nil)
			continue
		}
		for _, p := range []string{kOK, kFrz, kAlias} {
			if strings.HasPrefix(k, p+netID+"/") {
				n.stampLocked(k, true, nil)
			}
		}
	}
	n.saveLocked()
	n.mu.Unlock()
	n.poke()
}

// ---------- сверка ----------

// forgetIfMeshdRestarted — meshd перезапущен (обновили, упал и поднялся):
// списки серверов и чужих устройств у него пустые, а панель помнит, что уже
// отдавала их, и пять минут не повторила бы. Забываем — отдадим сразу.
func (n *ServerNet) forgetIfMeshdRestarted(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	st, err := n.mesh.admin.State(cctx)
	cancel()
	if err != nil {
		return
	}
	n.mu.Lock()
	if st.StartedAt != n.meshdStart {
		n.meshdStart = st.StartedAt
		n.lastRemote, n.lastKnwn = "", ""
	}
	n.mu.Unlock()
}

// reconcile приводит этот сервер к общим данным.
func (n *ServerNet) reconcile(ctx context.Context) {
	n.reconMu.Lock()
	defer n.reconMu.Unlock()
	n.mu.Lock()
	inNet := n.f.Net != ""
	now := n.now().Unix()
	// Истёкшие коды: их разовые ключи уходят из authorized_keys.
	live := n.f.Invites[:0]
	for _, inv := range n.f.Invites {
		if inv.Expires > now {
			live = append(live, inv)
		}
	}
	expired := len(live) != len(n.f.Invites)
	n.f.Invites = live
	if expired {
		n.saveLocked()
	}
	items := n.snapshotLocked()
	n.mu.Unlock()

	n.syncLinkKeys()
	n.forgetIfMeshdRestarted(ctx)
	n.pushRemote(ctx)
	if !inNet {
		n.pushKnown(ctx, nil, "")
		return
	}
	n.applySelfName(ctx)
	n.refreshSelf(ctx)
	n.applyAccounts(items)
	n.applyAccess(ctx, items)
	n.applyNewDevice(items)
	n.applyMesh(ctx, items)
	n.mu.Lock()
	netID := n.f.Net
	servers := n.serversLocked()
	n.mu.Unlock()
	// Этот сервер — тоже в списке: по нему устройство узнаёт, в какой сети
	// его сервер (meshd 6 не советует его старым устройствам).
	n.pushKnown(ctx, servers, netID)
}

// syncLinkKeys — ключи meshlink поменялись (сервер пришёл, ушёл, код
// истёк) — переписать.
func (n *ServerNet) syncLinkKeys() {
	lines := n.linkLines(n.mesh.settings.Get().Mesh.Links)
	n.mu.Lock()
	same := n.lastKeys == "\x00"+strings.Join(lines, "\n")
	n.mu.Unlock()
	if same {
		return
	}
	if err := n.mesh.syncLinks(); err != nil {
		n.mesh.logf("сеть серверов: ключи meshlink не записаны: %v", err)
	}
}

// refreshSelf — имя, адрес или ключи этого сервера поменялись — сказать сети.
func (n *ServerNet) refreshSelf(ctx context.Context) {
	n.mu.Lock()
	signer, err := n.keyLocked()
	cur, _ := decodeItem[srvRec](n.f.Items[kSrv+n.f.Self])
	n.mu.Unlock()
	if err != nil {
		return
	}
	want := n.selfRec(signer, cur.MeshID)
	if id := n.meshID(ctx); id != "" {
		want.MeshID = id
	}
	if want.Host == "" && deviceReachable(cur.Host) {
		want.Host = cur.Host
	}
	if reflect.DeepEqual(cur, want) {
		return
	}
	n.mu.Lock()
	if n.f.Net != "" {
		n.stampLocked(kSrv+n.f.Self, false, want)
		n.saveLocked()
	}
	n.mu.Unlock()
	n.poke()
}

// applyAccounts — учётные записи из сети; здешние, которых в сети нет,
// уходят в сеть.
func (n *ServerNet) applyAccounts(items map[string]srvItem) {
	if n.srv == nil {
		return
	}
	n.mu.Lock()
	n.applying = true
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		n.applying = false
		n.mu.Unlock()
	}()
	for k, it := range items {
		if !strings.HasPrefix(k, kAcct) {
			continue
		}
		user := strings.TrimPrefix(k, kAcct)
		var err error
		if it.Del {
			err = n.srv.clients.ApplyShared(user, nil)
		} else if rec, ok := decodeItem[acctRec](it); ok {
			err = n.srv.clients.ApplyShared(user, &rec)
		}
		if err != nil {
			n.mesh.logf("сеть серверов: учётная запись %s: %v", user, err)
		}
	}
	n.mu.Lock()
	n.applying = false
	n.mu.Unlock()
	name := n.mesh.selfName(n.mesh.settings.Get().Mesh)
	for _, c := range n.srv.clients.List() {
		if _, ok := items[kAcct+c.Username]; !ok {
			n.note(kAcct+c.Username, false, recOfClient(c, name))
		}
	}
	if err := n.srv.pushPolicy(context.Background()); err != nil {
		_ = err // meshd не отвечает — примет при следующей сверке
	}
}

// applyAccess — правила доступа из сети.
func (n *ServerNet) applyAccess(ctx context.Context, items map[string]srvItem) {
	if n.srv == nil || n.srv.settings == nil {
		return
	}
	it, ok := items[kAccess]
	if !ok {
		n.noteAccess(n.srv.settings.Get().Access)
		return
	}
	want, ok := decodeItem[AccessSettings](it)
	if !ok {
		return
	}
	if want.clean() != nil {
		return
	}
	cur := n.srv.settings.Get().Access
	if accessEqual(cur, want) {
		return
	}
	if err := n.srv.settings.Update(func(st *Settings) error { st.Access = want; return nil }); err != nil {
		n.mesh.logf("сеть серверов: правила доступа не записаны: %v", err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	n.srv.pushPolicy(cctx)
	cancel()
}

func accessEqual(a, b AccessSettings) bool {
	return a.Enabled == b.Enabled && slices.Equal(a.Groups, b.Groups) && slices.Equal(a.Rules, b.Rules)
}

// applyMesh — одобрения, заморозки и имена в meshd этого сервера.
func (n *ServerNet) applyMesh(ctx context.Context, items map[string]srvItem) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ms, err := n.mesh.admin.State(ctx)
	if err != nil {
		return
	}
	a := n.mesh.admin
	for _, nw := range ms.Networks {
		if r, ok := decodeItem[onRec](items[kAppr+nw.ID]); ok && r.On != nw.Approval {
			a.SetApproval(ctx, nw.ID, r.On)
		}
		if r, ok := decodeItem[nameRec](items[kNName+nw.ID]); ok && r.Name != nw.Name {
			a.RenameNetwork(ctx, nw.ID, r.Name)
		}
		for _, d := range nw.Devices {
			if it, ok := items[devKey(kOK, nw.ID, d.ID)]; ok && !it.Del && d.Pending {
				a.Approve(ctx, nw.ID, d.ID)
			}
			if r, ok := decodeItem[onRec](items[devKey(kFrz, nw.ID, d.ID)]); ok && r.On != d.Frozen {
				a.Freeze(ctx, nw.ID, d.ID, r.On)
			}
			if r, ok := decodeItem[aliasRec](items[devKey(kAlias, nw.ID, d.ID)]); ok && r.Alias != d.Alias {
				a.Rename(ctx, nw.ID, d.ID, r.Alias)
			}
		}
	}
}

// pushKnown — какие серверы сети советовать устройствам этого сервера, и
// чьи учётные записи там есть (тогда устройство добавит сервер само).
func (n *ServerNet) pushKnown(ctx context.Context, servers map[string]srvRec, netID string) {
	var list []meshsvc.KnownServer
	var shared []string
	if servers != nil {
		ids := make([]string, 0, len(servers))
		for id := range servers {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			r := servers[id]
			// Адрес, по которому устройству не дойти (london.mesh от
			// необновлённой панели), не советуем: устройство записало бы его
			// в профиль, и сервер не добавился бы уже никогда (ТЗ 24).
			if validHost(r.Host) != nil || !deviceReachable(r.Host) {
				continue
			}
			list = append(list, meshsvc.KnownServer{Name: r.Name, Host: r.Host, Port: r.Port,
				ID: r.MeshID, HostKey: preferredHostKey(r.HostKeys), Net: netID})
		}
		if n.srv != nil {
			for _, c := range n.srv.clients.List() {
				shared = append(shared, c.Username)
			}
			sort.Strings(shared)
		}
	} else {
		// Сервер один: свой список больше не ведётся вручную — очищаем
		// только то, что когда-то записала сеть серверов.
		if n.lastKnwn == "" {
			return
		}
	}
	data, _ := json.Marshal([]any{list, shared})
	sig := string(data)
	if sig == n.lastKnwn && time.Since(n.knownAt) < 5*time.Minute {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := n.mesh.admin.SetKnownShared(cctx, list, shared); err == nil {
		n.lastKnwn, n.knownAt = sig, time.Now()
		if servers == nil {
			n.lastKnwn = ""
		}
	}
}

// preferredHostKey — один ключ сервера для устройства (короче строка):
// ed25519, если есть.
func preferredHostKey(keys []string) string {
	for _, k := range keys {
		if strings.HasPrefix(k, "ssh-ed25519 ") {
			return k
		}
	}
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}
