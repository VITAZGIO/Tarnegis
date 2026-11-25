package config

// Сеть серверов (ТЗ 22) на устройстве: серверы сети, где есть учётная
// запись этого устройства, программа добавляет сама — тем же пользователем и
// ключом, что и профиль, через который о них узнала. Убранный из сети сервер
// выключается. Решает только этот код; экран и мост его не повторяют.

import (
	"net"
	"slices"
	"strings"
)

// NetServerAd — сервер сети серверов, о котором рассказал сервер на связи.
type NetServerAd struct {
	ID, Name, Host string
	Port           int
	HostKey        string
	Net            string
	Acct           bool
	// Via — ключ «адрес:порт» профиля, через который пришёл список.
	Via string
}

// AutoKey — как профиль сервера сети помечен в MeshAuto.
func (a NetServerAd) AutoKey() string { return a.Net + "/" + a.ID }

// seenKey — отметка в MeshAutoSeen: сервер и адрес, по которому его
// добавляли (ТЗ 24). Сервер сменил адрес — решение принимается заново:
// раньше отметка без адреса навсегда закрывала сервер, который однажды
// пришёл с адресом, недоступным устройству (london.mesh).
func (a NetServerAd) seenKey(key string) string { return a.AutoKey() + "@" + key }

// seen — сервер уже добавляли (удалён или выключен человеком — его
// решение). Отметка старого вида (без адреса, v1.10.1) считается, только
// если профиль, добавленный программой, ещё есть: иначе она могла остаться
// от ветки «профиль уже есть», которая сервер не добавляла.
func (c *Config) seen(a NetServerAd, key string) bool {
	if slices.Contains(c.MeshAutoSeen, a.seenKey(key)) {
		return true
	}
	if !slices.Contains(c.MeshAutoSeen, a.AutoKey()) {
		return false
	}
	for _, p := range c.Profiles {
		if p.MeshAuto == a.AutoKey() {
			return true
		}
	}
	return false
}

// removedPrefix — отметка в MeshAutoSeen «сервер удалён человеком» (ТЗ 50):
// «del@адрес:порт». Такой сервер сеть серверов больше не добавляет, пока
// человек не заведёт его снова сам (тогда отметка снимается). Живёт в том же
// списке, что и отметки «уже добавляли», — телефон хранит и передаёт его
// как есть.
const removedPrefix = "del@"

// RemovedMark — отметка «удалён человеком» для сервера key (ServerKey).
func RemovedMark(key string) string { return removedPrefix + strings.ToLower(key) }

func (c *Config) removedByUser(key string) bool {
	return key != "" && slices.Contains(c.MeshAutoSeen, RemovedMark(key))
}

// markRemoved — человек удалил профиль p: сеть серверов его не вернёт.
func (c *Config) markRemoved(p Profile) {
	k := ServerKey(p)
	if k == "" {
		return
	}
	if p.MeshAuto != "" {
		// Отметка «уже добавляли» нового вида: старая (без адреса) без
		// профиля не действует, и сервер вернулся бы.
		if s := p.MeshAuto + "@" + k; !slices.Contains(c.MeshAutoSeen, s) {
			c.MeshAutoSeen = append(c.MeshAutoSeen, s)
		}
	}
	if !c.removedByUser(k) {
		c.MeshAutoSeen = append(c.MeshAutoSeen, RemovedMark(k))
	}
}

// RemovedMarks — отметки, которые ставит удаление профиля p (для телефона:
// он удаляет профили сам и дописывает их в свой список отметок).
func RemovedMarks(p Profile) []string {
	if p.IsPanel() {
		return nil
	}
	var c Config
	c.markRemoved(p)
	return c.MeshAutoSeen
}

// clearRemoved — профиль этого сервера снова есть (человек завёл сам):
// отметка «удалён» снимается.
func (c *Config) clearRemoved() {
	if !slices.ContainsFunc(c.MeshAutoSeen, func(s string) bool { return strings.HasPrefix(s, removedPrefix) }) {
		return
	}
	have := map[string]bool{}
	for _, p := range c.Profiles {
		if k := ServerKey(p); k != "" {
			have[RemovedMark(k)] = true
		}
	}
	c.MeshAutoSeen = slices.DeleteFunc(slices.Clone(c.MeshAutoSeen), func(s string) bool { return have[s] })
}

// NetServersResult — что поменялось: добавленные профили и выключенные
// (имена).
type NetServersResult struct {
	Added []Profile
	Off   []string
	// MeshOn — имена профилей, заведённых человеком раньше, в которых
	// программа включила сеть устройств (сервер вошёл в сеть).
	MeshOn []string
}

// ApplyNetServers добавляет профили серверов сети, где есть учётная запись
// этого устройства, и выключает добавленные так же профили серверов, которых
// в своей сети больше нет. nets — сети, чей список известен целиком.
func (c *Config) ApplyNetServers(list []NetServerAd, nets []string) NetServersResult {
	var res NetServersResult
	c.clearRemoved()
	present := map[string]bool{}
	for _, a := range list {
		present[a.AutoKey()] = true
	}
	// Убранные из сети — выключить (не удалять: человек мог его поправить).
	for i := range c.Profiles {
		p := &c.Profiles[i]
		if p.MeshAuto == "" || !p.MeshEnabled {
			continue
		}
		net, _, _ := strings.Cut(p.MeshAuto, "/")
		if slices.Contains(nets, net) && !present[p.MeshAuto] {
			p.MeshEnabled = false
			res.Off = append(res.Off, p.Name)
		}
	}
	for _, a := range list {
		if !a.Acct || a.ID == "" || a.Net == "" || a.Host == "" || strings.ContainsAny(a.Host, " /@\n") {
			continue
		}
		// Имя .mesh снаружи сети устройств не работает — такой адрес сервер
		// мог отдать по ошибке (старая панель); по нему ничего не делаем.
		if strings.HasSuffix(strings.ToLower(strings.TrimSuffix(a.Host, ".")), ".mesh") {
			continue
		}
		port := a.Port
		if port <= 0 {
			port = 22
		}
		probe := Profile{Host: a.Host, SSHPort: port}
		key := ServerKey(probe)
		if c.seen(a, key) || c.removedByUser(key) {
			continue // уже добавляли или человек удалил — его решение
		}
		base, ok := c.profileByKey(a.Via)
		if !ok || !base.MeshEnabled || base.MeshKey == "" {
			continue
		}
		// Серверы устройства из панели (ТЗ 48) решает панель: её профиль
		// устройства, а не этот список.
		if base.FromPanel != "" {
			continue
		}
		if _, ok := c.profileByKey(key); ok {
			// Профиль этого сервера уже есть (человек завёл сам). Сеть
			// устройств в нём выключена — включаем с ключом этой сети, один
			// раз: выключит человек снова — отметка оставит его решение.
			// Включённую с другим ключом не трогаем — это другая сеть.
			for i := range c.Profiles {
				q := &c.Profiles[i]
				if !strings.EqualFold(ServerKey(*q), key) {
					continue
				}
				if !q.MeshEnabled {
					q.MeshEnabled, q.MeshKey, q.MeshOnlyThis = true, base.MeshKey, false
					c.MeshAutoSeen = append(c.MeshAutoSeen, a.seenKey(key))
					res.MeshOn = append(res.MeshOn, q.Name)
				}
				break
			}
			continue
		}
		p := base
		p.ID = newProfileID()
		p.Name = a.Name
		if p.Name == "" {
			p.Name = a.Host
		}
		p.Flag = ""
		p.Host, p.SSHPort = a.Host, port
		p.MeshEnabled, p.MeshOnlyThis = true, false
		p.MeshAuto = a.AutoKey()
		p.ExitVia, p.ExitT, p.ExitBy = nil, 0, ""
		p.FilterApps = slices.Clone(base.FilterApps)
		p.DirectHosts = slices.Clone(base.DirectHosts)
		p.TunnelHosts = slices.Clone(base.TunnelHosts)
		c.Profiles = append(c.Profiles, p)
		c.MeshAutoSeen = append(c.MeshAutoSeen, a.seenKey(key))
		res.Added = append(res.Added, p)
	}
	return res
}

func (c *Config) profileByKey(key string) (Profile, bool) {
	if key == "" {
		return Profile{}, false
	}
	// Дополнительное соединение сети устройств ключуется id профиля: список
	// серверов, пришедший через второй сервер, тоже находит свой профиль.
	for _, p := range c.Profiles {
		if p.ID != "" && p.ID == key {
			return p, true
		}
	}
	// Ключ соединения сети устройств (Via) — адрес профиля без порта: ищем
	// по адресу, сначала профиль с включённой сетью устройств.
	if _, _, err := net.SplitHostPort(key); err != nil {
		host := strings.ToLower(strings.TrimSpace(key))
		var found *Profile
		for i := range c.Profiles {
			p := &c.Profiles[i]
			if strings.ToLower(strings.TrimSpace(p.Host)) != host {
				continue
			}
			if found == nil || (p.MeshEnabled && !found.MeshEnabled) {
				found = p
			}
		}
		if found != nil {
			return *found, true
		}
		return Profile{}, false
	}
	for _, p := range c.Profiles {
		if strings.EqualFold(ServerKey(p), key) {
			return p, true
		}
	}
	return Profile{}, false
}
