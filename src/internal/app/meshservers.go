package app

// Сеть серверов (ТЗ 22) со стороны приложения: сервер сети, где есть
// учётная запись этого устройства, добавляется профилем сам (решает
// config.ApplyNetServers), его SSH-ключ сразу записывается в known_hosts, а
// человек видит одно уведомление.

import (
	"sshtunnel/internal/config"
	"sshtunnel/internal/hostkey"
	"sshtunnel/internal/mesh"
)

// onMeshServers — сервер прислал список сети серверов.
func (a *App) onMeshServers(ns mesh.NetServers) {
	a.exitMu.Lock()
	defer a.exitMu.Unlock()
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
	cfg := a.Config()
	if cfg.Device != nil {
		// Устройство из панели: его серверы решает профиль устройства —
		// список сети поменялся, значит, мог поменяться и он (ТЗ 49).
		defer a.kickDevice()
	}
	cfg.Profiles = append([]config.Profile(nil), cfg.Profiles...)
	cfg.MeshAutoSeen = append([]string(nil), cfg.MeshAutoSeen...)
	res := cfg.ApplyNetServers(list, ns.Nets)
	if len(res.Added) == 0 && len(res.Off) == 0 && len(res.MeshOn) == 0 {
		// Могли пометить «уже есть» — сохранить без шума.
		if len(cfg.MeshAutoSeen) != len(a.Config().MeshAutoSeen) {
			a.SetConfig(cfg)
		}
		return
	}
	for _, p := range res.Added {
		for _, s := range list {
			if s.AutoKey() == p.MeshAuto && s.HostKey != "" {
				hostkey.Pin(config.KnownHostsPath(), p.Host, p.SSHPort, s.HostKey)
			}
		}
	}
	if _, err := a.SetConfig(cfg); err != nil {
		a.Bus.Warnf("Сеть устройств: новый сервер сети не сохранился: %v", err)
		return
	}
	for _, p := range res.Added {
		a.Bus.Infof("В сеть добавлен сервер %s", p.Name)
	}
	for _, name := range res.MeshOn {
		a.Bus.Infof("Сервер %s в сети устройств — в его профиле включена сеть устройств", name)
	}
	for _, name := range res.Off {
		a.Bus.Warnf("Сервер %s убран из сети — его профиль выключен", name)
	}
}

