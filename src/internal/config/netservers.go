package config

// Сеть серверов (ТЗ 22) на устройстве: серверы сети, где есть учётная
// запись этого устройства, программа добавляет сама — тем же пользователем и
// ключом, что и профиль, через который о них узнала. Убранный из сети сервер
// выключается. Решает только этот код; экран и мост его не повторяют.

import (
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

// AutoKey — как профиль сервера сети помечен в MeshAuto и MeshAutoSeen.
func (a NetServerAd) AutoKey() string { return a.Net + "/" + a.ID }

// NetServersResult — что поменялось: добавленные профили и выключенные
// (имена).
type NetServersResult struct {
	Added []Profile
	Off   []string
}

// ApplyNetServers добавляет профили серверов сети, где есть учётная запись
// этого устройства, и выключает добавленные так же профили серверов, которых
// в своей сети больше нет. nets — сети, чей список известен целиком.
func (c *Config) ApplyNetServers(list []NetServerAd, nets []string) NetServersResult {
	var res NetServersResult
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
		if slices.Contains(c.MeshAutoSeen, a.AutoKey()) {
			continue // уже добавляли: удалён или выключен человеком — его решение
		}
		port := a.Port
		if port <= 0 {
			port = 22
		}
		probe := Profile{Host: a.Host, SSHPort: port}
		if _, ok := c.profileByKey(ServerKey(probe)); ok {
			c.MeshAutoSeen = append(c.MeshAutoSeen, a.AutoKey())
			continue // профиль этого сервера уже есть
		}
		base, ok := c.profileByKey(a.Via)
		if !ok || !base.MeshEnabled || base.MeshKey == "" {
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
		c.MeshAutoSeen = append(c.MeshAutoSeen, a.AutoKey())
		res.Added = append(res.Added, p)
	}
	return res
}

func (c *Config) profileByKey(key string) (Profile, bool) {
	if key == "" {
		return Profile{}, false
	}
	for _, p := range c.Profiles {
		if strings.EqualFold(ServerKey(p), key) {
			return p, true
		}
	}
	return Profile{}, false
}
