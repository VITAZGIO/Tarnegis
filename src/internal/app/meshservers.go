package app

// Сеть серверов (ТЗ 22) со стороны приложения: сервер сети, где есть
// учётная запись этого устройства, добавляется профилем сам (решает
// config.ApplyNetServers), его SSH-ключ сразу записывается в known_hosts, а
// человек видит одно уведомление.

import (
	"os"
	"strconv"
	"strings"

	"sshtunnel/internal/config"
	"sshtunnel/internal/mesh"
)

// onMeshServers — сервер прислал список сети серверов.
func (a *App) onMeshServers(ns mesh.NetServers) {
	a.exitMu.Lock()
	defer a.exitMu.Unlock()
	list := make([]config.NetServerAd, 0, len(ns.Servers))
	for _, s := range ns.Servers {
		list = append(list, config.NetServerAd{ID: s.ID, Name: s.Name, Host: s.Host, Port: s.Port,
			HostKey: s.HostKey, Net: s.Net, Acct: s.Acct, Via: s.Via})
	}
	cfg := a.Config()
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
				pinHostKey(config.KnownHostsPath(), p.Host, p.SSHPort, s.HostKey)
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

// pinHostKey записывает SSH-ключ сервера в known_hosts, если про этот
// адрес там ещё ничего нет: ключ пришёл от сервера сети по SSH, это
// надёжнее, чем поверить первому соединению.
func pinHostKey(path, host string, port int, key string) {
	f := strings.Fields(key)
	if len(f) < 2 {
		return
	}
	name := host
	if port != 22 && port > 0 {
		name = "[" + host + "]:" + strconv.Itoa(port)
	}
	data, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(data), "\n") {
		if hosts, _, ok := strings.Cut(strings.TrimSpace(l), " "); ok {
			for _, h := range strings.Split(hosts, ",") {
				if h == name {
					return
				}
			}
		}
	}
	line := name + " " + f[0] + " " + f[1] + "\n"
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		line = "\n" + line
	}
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	fh.WriteString(line)
	fh.Close()
}
