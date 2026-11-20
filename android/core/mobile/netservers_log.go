package mobile

// Журнал сети серверов на телефоне (ТЗ 33). Телефон на v1.10.6 не добавлял
// серверы сети, а по журналу было не понять почему: ни что сервер прислал,
// ни почему каждый сервер пропущен. Решает по-прежнему только
// config.ApplyNetServers — здесь лишь объяснение для человека тем же
// порядком проверок; каждая строка пишется один раз, пока не изменится.

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"sshtunnel/internal/config"
	"sshtunnel/internal/mesh"
)

// netNote пишет строку в журнал приложения, если она новая для ключа key.
func (t *Tunnel) netNote(key, text string) {
	t.mu.Lock()
	if t.netNotes == nil {
		t.netNotes = map[string]string{}
	}
	same := t.netNotes[key] == text
	t.netNotes[key] = text
	t.mu.Unlock()
	if same || text == "" {
		return
	}
	if cb := t.callbacks(); cb != nil {
		cb.OnLog(text)
	}
}

// noteNetList — что прислал сервер (op servers).
func (t *Tunnel) noteNetList(ns mesh.NetServers) {
	var names []string
	acct := 0
	for _, s := range ns.Servers {
		n := s.Name
		if n == "" {
			n = s.Host
		}
		if s.Acct {
			acct++
		} else {
			n += " (нет учётной записи)"
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		t.netNote("list", "сеть серверов: других серверов в сети нет")
		return
	}
	t.netNote("list", fmt.Sprintf("сеть серверов: сервер прислал список — %s; с учётной записью телефона: %d",
		strings.Join(names, ", "), acct))
}

// noteNetSkips — почему серверы списка не добавлены (cfg — профили до
// ApplyNetServers, added — добавленные или включённые им).
func (t *Tunnel) noteNetSkips(cfg config.Config, list []config.NetServerAd, added map[string]bool) {
	for _, a := range list {
		name := a.Name
		if name == "" {
			name = a.Host
		}
		why := netSkipReason(cfg, a)
		if added[a.AutoKey()] {
			why = ""
		}
		if why == "" {
			t.netNote("skip/"+a.AutoKey(), "")
			continue
		}
		t.netNote("skip/"+a.AutoKey(), "сеть серверов: "+name+" не добавлен — "+why)
	}
}

// netSkipReason повторяет проверки config.ApplyNetServers словами. ""
// — сервер добавляется (или его профиль уже есть и в сети).
func netSkipReason(cfg config.Config, a config.NetServerAd) string {
	if !a.Acct {
		return "учётной записи телефона на нём нет (на панели: «Доступ» → учётная запись для всех серверов сети)"
	}
	if a.ID == "" || a.Net == "" {
		return "сервер не сообщил свой id или сеть (старая панель)"
	}
	h := strings.ToLower(strings.TrimSuffix(a.Host, "."))
	if a.Host == "" || strings.ContainsAny(a.Host, " /@\n") || strings.HasSuffix(h, ".mesh") {
		return "у сервера нет адреса, доступного снаружи сети устройств (" + a.Host + ")"
	}
	port := a.Port
	if port <= 0 {
		port = 22
	}
	key := net.JoinHostPort(strings.ToLower(strings.TrimSpace(a.Host)), strconv.Itoa(port))
	if slices.Contains(cfg.MeshAutoSeen, a.AutoKey()+"@"+key) {
		return "его уже добавляли раньше, а профиль удалён или выключен вручную — вернуть можно только вручную"
	}
	for _, p := range cfg.Profiles {
		if strings.EqualFold(config.ServerKey(p), key) {
			return "" // профиль уже есть: сеть в нём включается сама (meshOn)
		}
	}
	base, ok := viaProfile(cfg, a.Via)
	if !ok {
		return "не найден профиль сервера, через который пришёл список (" + a.Via + ")"
	}
	if !base.MeshEnabled || base.MeshKey == "" {
		return "в профиле " + base.Name + " выключена сеть устройств"
	}
	return ""
}

// viaProfile — профиль по ключу соединения сети устройств (как
// config.profileByKey): id профиля или адрес без порта.
func viaProfile(cfg config.Config, key string) (config.Profile, bool) {
	if key == "" {
		return config.Profile{}, false
	}
	for _, p := range cfg.Profiles {
		if p.ID == key {
			return p, true
		}
	}
	var found *config.Profile
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		if strings.EqualFold(strings.TrimSpace(p.Host), strings.TrimSpace(key)) || strings.EqualFold(config.ServerKey(*p), key) {
			if found == nil || (p.MeshEnabled && !found.MeshEnabled) {
				found = p
			}
		}
	}
	if found == nil {
		return config.Profile{}, false
	}
	return *found, true
}
