package mobile

// Другие серверы телефона (ТЗ 20): дополнительные серверы сети устройств и
// цепочка выхода — то же, что на ПК (ТЗ 12 и ТЗ 8), и тем же общим кодом.
//
// Список серверов хранит приложение (Settings.kt). Оно отдаёт его сюда
// целиком (SetProfiles) — в том же JSON, что профиль на ПК (config.Profile:
// id, name, host, sshPort, user, keyPath, meshEnabled, meshKey, meshOnlyThis,
// exitVia), — а выбирает, кого брать, общий код: config.Config.MeshServers и
// config.Config.ExitChain. Так правило «тот же ключ сети — тот же сервер
// сети» и «Выход через» на телефоне ровно такие же, как на ПК.
//
// Дополнительный сервер — лёгкое соединение (tunnel.MeshLinks): SSH
// keepalive раз в 60 с и «тихий» пинг meshd раз в 60 с, без пингов клиента и
// проверки NAT. По оценке ТЗ 12 это ≈ 2 обмена в минуту к ≈ 9,4 у основного,
// то есть два сервера ≈ 1,2× по батарее. Живьём не мерилось.
//
// Цепочка: туннель ведёт «через сервер» только через последний сервер
// цепочки (tunnel.Config.Exit, Tunnel.Dial), запасного пути нет. Имена
// с подставными адресами открываются по имени на последнем сервере — значит
// и DNS идёт через него; UDP — через его ретранслятор. Сервер цепочки лёг —
// интернета через VPN нет вовсе: интерфейс VPN остаётся, пакеты ловятся, а
// соединения получают отказ. Утечки мимо цепочки нет, потому что обходного
// пути в коде нет.

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"sshtunnel/internal/config"
	core2 "sshtunnel/internal/core"
	"sshtunnel/internal/hostkey"
	"sshtunnel/internal/mesh"
	"sshtunnel/internal/tunnel"
)

// SetProfiles — все серверы приложения (JSON-массив профилей, см. выше) и id
// активного. udpRelay — галочка «ретранслятор UDP» (на телефоне общая для
// всех серверов). Зовётся до StartCore (после Configure); на поднятом
// туннеле дополнительные серверы меняются сразу, а цепочка — со следующим
// подключением (её меняет переподключение, как на ПК).
func (t *Tunnel) SetProfiles(profilesJSON, activeID string, udpRelay bool) error {
	var list []config.Profile
	if s := strings.TrimSpace(profilesJSON); s != "" {
		if err := json.Unmarshal([]byte(s), &list); err != nil {
			return fmt.Errorf("список серверов не разобран: %v", err)
		}
	}
	cfg := config.Config{Profiles: list}
	active, ok := cfg.ProfileByID(activeID)
	if !ok {
		t.mu.Lock()
		t.extras, t.exit = nil, nil
		t.mu.Unlock()
		t.applyExtras()
		if activeID != "" && len(list) > 0 {
			return errors.New("активный сервер не найден в списке")
		}
		return nil
	}
	if len(active.ExitVia) > config.MaxExitHops {
		return fmt.Errorf("в цепочке может быть не больше %d серверов после этого", config.MaxExitHops)
	}
	var extras []config.Profile
	if active.MeshEnabled && active.MeshKey != "" {
		extras = cfg.MeshServers(active)
	}
	var hops []tunnel.Hop
	for _, h := range cfg.ExitChain(active) {
		if h.ID == active.ID {
			continue // сам себе не выход
		}
		hops = append(hops, tunnel.Hop{Name: h.Name, Host: h.Host, SSHPort: h.SSHPort, User: h.User,
			KeyPath: h.KeyPath, Cipher: h.Cipher, UDPRelay: udpRelay})
	}
	t.mu.Lock()
	t.extras, t.exit = extras, hops
	t.mu.Unlock()
	t.applyExtras()
	return nil
}

// meshServersLocked — дополнительные серверы для MeshLinks. Под t.mu.
func (t *Tunnel) meshServersLocked() []tunnel.MeshServer {
	out := make([]tunnel.MeshServer, 0, len(t.extras))
	for _, q := range t.extras {
		out = append(out, tunnel.MeshServer{
			Key: q.ID, Name: q.Name, Host: q.Host, SSHPort: q.SSHPort, User: q.User, KeyPath: q.KeyPath,
			KnownHostsPath: t.cfg.KnownHostsPath, IPVersion: q.IPVersion, Cipher: q.Cipher,
			ProtectSocket: t.protect,
		})
	}
	return out
}

// startMesh поднимает клиента сети устройств и его дополнительные серверы.
func (t *Tunnel) startMesh(mc *mesh.Config) {
	t.meshHost.Start(*mc)
	t.applyExtras()
}

// stopMesh гасит дополнительные серверы и клиента (как App.stopMesh на ПК).
func (t *Tunnel) stopMesh() {
	t.meshLinks.Stop()
	t.meshHost.Stop()
}

// applyExtras — дополнительные серверы: держать, пока клиент сети нужен и
// работает, иначе — ни одного соединения.
func (t *Tunnel) applyExtras() {
	// Running — под замком MeshHost, а его журнал берёт t.mu: спрашивать до
	// t.mu, иначе два потока могут ждать друг друга.
	running := t.meshHost.Running()
	t.mu.Lock()
	var list []tunnel.MeshServer
	if running && t.meshWantedLocked() != nil {
		list = t.meshServersLocked()
	}
	t.mu.Unlock()
	t.meshLinks.Set(list)
}

// MeshServersJSON — какие дополнительные серверы сети устройств сейчас
// держатся (id профилей), для проверки и экрана: ["id", …].
func (t *Tunnel) MeshServersJSON() string {
	keys := t.meshLinks.Keys()
	if keys == nil {
		keys = []string{}
	}
	b, _ := json.Marshal(keys)
	return string(b)
}

// ExitJSON — цепочка выхода для экрана (core.ExitPath: state up / connecting
// / down, path, hop, error, errorKind) или пусто, если цепочки нет или туннель
// выключен.
func (t *Tunnel) ExitJSON() string {
	t.mu.Lock()
	tun := t.core
	t.mu.Unlock()
	if tun == nil {
		return ""
	}
	st := tun.ExitStatus()
	if len(st.Path) == 0 {
		return ""
	}
	out := core2.ExitPath{Path: st.Path, State: core2.ExitConnecting}
	switch {
	case st.Up:
		out.State = core2.ExitUp
	case st.Err != nil:
		out.State = core2.ExitDown
		out.Error = st.Err.Error()
		var ed *tunnel.ExitDownError
		if errors.As(st.Err, &ed) {
			out.Hop = ed.Name
			out.ErrorKind = errorKind(ed.Err)
		}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// ---------- сеть серверов (ТЗ 22) ----------

// NetServersListener — приложению: сервер прислал список сети серверов
// (op servers). Приложение зовёт ApplyNetServers и сохраняет ответ (ТЗ 26).
// Зовётся в своей горутине, не под замком.
type NetServersListener interface {
	OnNetServers()
}

// SetNetServersListener — nil снимает. Если список уже пришёл раньше, чем
// приложение подписалось, слушатель зовётся сразу.
func (t *Tunnel) SetNetServersListener(l NetServersListener) {
	t.mu.Lock()
	t.netListener = l
	had := t.netServers != nil
	t.mu.Unlock()
	if l != nil && had {
		go l.OnNetServers()
	}
}

// onNetServers запоминает последний список сети серверов от сервера на
// связи и говорит приложению: оно заберёт его через ApplyNetServers.
func (t *Tunnel) onNetServers(ns mesh.NetServers) {
	t.mu.Lock()
	t.netServers = &ns
	l := t.netListener
	t.mu.Unlock()
	t.noteNetList(ns)
	if l != nil {
		l.OnNetServers()
	} else {
		t.netNote("nolistener", "сеть серверов: список пришёл раньше, чем приложение подписалось, — разберу при подписке")
	}
}

// ApplyNetServers — то же, что на ПК (config.Config.ApplyNetServers):
// серверы сети, где есть учётная запись телефона, добавляются профилями
// сами, убранные из сети — выключаются. Принимает профили приложения (тот же
// JSON, что SetProfiles) и список уже добавлявшихся («id сети/id сервера»,
// JSON-массив), отдаёт JSON {"profiles","seen","added","off"} или "" —
// ничего не поменялось. Приложение сохраняет profiles и seen и показывает
// по уведомлению на каждое имя из added и off.
func (t *Tunnel) ApplyNetServers(profilesJSON, seenJSON string) (string, error) {
	t.mu.Lock()
	ns := t.netServers
	t.mu.Unlock()
	if ns == nil {
		return "", nil
	}
	cfg := config.Config{}
	if err := json.Unmarshal([]byte(profilesJSON), &cfg.Profiles); err != nil {
		return "", fmt.Errorf("список серверов не разобран: %v", err)
	}
	if strings.TrimSpace(seenJSON) != "" {
		if err := json.Unmarshal([]byte(seenJSON), &cfg.MeshAutoSeen); err != nil {
			return "", fmt.Errorf("список добавленных серверов не разобран: %v", err)
		}
	}
	seenBefore := len(cfg.MeshAutoSeen)
	list := make([]config.NetServerAd, 0, len(ns.Servers))
	// Ключи серверов глазами сети — свидетели для смены ключа (ТЗ 46).
	wit := make([]hostkey.Witness, 0, len(ns.Servers))
	for _, s := range ns.Servers {
		wit = append(wit, hostkey.Witness{Host: s.Host, Port: s.Port, Key: s.HostKey, Via: s.Via})
	}
	hostkey.SetWitnesses(wit)
	for _, s := range ns.Servers {
		list = append(list, config.NetServerAd{ID: s.ID, Name: s.Name, Host: s.Host, Port: s.Port,
			HostKey: s.HostKey, Net: s.Net, Acct: s.Acct, Via: s.Via})
	}
	before := config.Config{Profiles: slices.Clone(cfg.Profiles), MeshAutoSeen: slices.Clone(cfg.MeshAutoSeen)}
	res := cfg.ApplyNetServers(list, ns.Nets)
	done := map[string]bool{}
	for _, p := range res.Added {
		done[p.MeshAuto] = true
	}
	t.noteNetSkips(before, list, done)
	if len(res.Added) == 0 && len(res.Off) == 0 && len(res.MeshOn) == 0 && len(cfg.MeshAutoSeen) == seenBefore {
		return "", nil
	}
	added := []string{}
	for _, p := range res.Added {
		added = append(added, p.Name)
		// Как на ПК: ключ сервера пришёл от сервера сети по SSH — сразу в
		// known_hosts, первое соединение ему уже не «верит на слово».
		for _, s := range list {
			if s.AutoKey() == p.MeshAuto && s.HostKey != "" {
				hostkey.Pin(t.cfg.KnownHostsPath, p.Host, p.SSHPort, s.HostKey)
			}
		}
	}
	off := res.Off
	if off == nil {
		off = []string{}
	}
	// meshOn (v1.10.5): в профиле, заведённом человеком раньше, программа
	// включила сеть устройств — приложение сохраняет и это.
	meshOn := res.MeshOn
	if meshOn == nil {
		meshOn = []string{}
	}
	out, err := json.Marshal(map[string]any{"profiles": cfg.Profiles, "seen": cfg.MeshAutoSeen,
		"added": added, "off": off, "meshOn": meshOn})
	return string(out), err
}
