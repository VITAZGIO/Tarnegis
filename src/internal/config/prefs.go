package config

import (
	"slices"
	"strings"
)

// Prefs — настройки программы, одни на все серверы (ТЗ 32, решено с
// Виталием 20.11.2026: «как в готовых VPN-приложениях»). Раньше они жили в
// каждом профиле сервера, и при смене активного сервера (или после профиля,
// добавленного панелью / «сетью серверов») человек видел чужой — часто
// пустой — список «всегда напрямую».
//
// Группа «правила» (списки, программы, локальная сеть через туннель) и
// тонкая настройка связи могут быть своими у сервера: Profile.OwnRules.
// Имя устройства и сеть устройств, раздача в локальную сеть — только общие:
// разные имена на разных серверах были скрытой ошибкой.
type Prefs struct {
	// Правила (у сервера с OwnRules — свои).
	FilterMode     string   `json:"filterMode,omitempty"`
	FilterApps     []string `json:"filterApps,omitempty"`
	DirectHosts    []string `json:"directHosts,omitempty"`
	TunnelHosts    []string `json:"tunnelHosts,omitempty"`
	LocalViaTunnel bool     `json:"localViaTunnel,omitempty"`
	// Тонкая настройка связи (у сервера с OwnRules — своя). Ноль и "" —
	// «авто», как раньше.
	PoolSize          int    `json:"poolSize,omitempty"`
	KeepAliveSec      int    `json:"keepAliveSec,omitempty"`
	ConnectTimeoutSec int    `json:"connectTimeoutSec,omitempty"`
	Cipher            string `json:"cipher,omitempty"`
	IPVersion         string `json:"ipVersion,omitempty"`
	// Только общие.
	MeshName      string `json:"meshName,omitempty"`
	MeshIncoming  bool   `json:"meshIncoming,omitempty"`
	MeshDirectOff bool   `json:"meshDirectOff,omitempty"`
	ShareLAN      bool   `json:"shareLan,omitempty"`
}

// PrefsVersion — значение Config.PrefsV после переезда настроек в Prefs.
const PrefsVersion = 1

// migrationNotes — что заметил последний переезд (Load): разные имена
// устройства в профилях и т. п. Программа пишет это в журнал.
var migrationNotes []string

// TakeMigrationNotes отдаёт и забывает заметки последнего переезда.
func TakeMigrationNotes() []string {
	n := migrationNotes
	migrationNotes = nil
	return n
}

// MigratePrefs — переезд конфига старого формата (PrefsV == 0): списки —
// объединение списков всех серверов (ничего не теряется, активный первым),
// режим программ, программы и одиночные значения — активного сервера, имя
// устройства — активного (другие имена — в заметки). «Свои правила» у всех
// выключены. Уже переехавший конфиг не трогает.
func (c *Config) MigratePrefs() {
	if c.PrefsV >= PrefsVersion {
		return
	}
	c.PrefsV = PrefsVersion
	if len(c.Profiles) == 0 {
		return
	}
	active := c.rawActive()
	order := []Profile{active}
	for _, p := range c.Profiles {
		if p.ID != active.ID {
			order = append(order, p)
		}
	}
	pr := prefsOf(active)
	pr.DirectHosts, pr.TunnelHosts = nil, nil
	for _, p := range order {
		pr.DirectHosts = unionEntries(pr.DirectHosts, p.DirectHosts)
		pr.TunnelHosts = unionEntries(pr.TunnelHosts, p.TunnelHosts)
		if n := strings.TrimSpace(p.MeshName); n != "" && p.ID != active.ID && n != strings.TrimSpace(active.MeshName) {
			migrationNotes = append(migrationNotes,
				"Имя устройства теперь одно на все серверы: «"+strings.TrimSpace(active.MeshName)+
					"» (у сервера «"+p.Name+"» было «"+n+"»)")
		}
	}
	c.Prefs = pr
	for i := range c.Profiles {
		c.Profiles[i].OwnRules = false
	}
}

// unionEntries — a и записи из b, которых в a ещё нет (без учёта регистра).
func unionEntries(a, b []string) []string {
	seen := map[string]bool{}
	for _, s := range a {
		seen[strings.ToLower(strings.TrimSpace(s))] = true
	}
	out := slices.Clone(a)
	for _, s := range b {
		k := strings.ToLower(strings.TrimSpace(s))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

// prefsOf — значения настроек из профиля (как они лежали до ТЗ 32).
func prefsOf(p Profile) Prefs {
	return Prefs{
		FilterMode: p.FilterMode, FilterApps: slices.Clone(p.FilterApps),
		DirectHosts: slices.Clone(p.DirectHosts), TunnelHosts: slices.Clone(p.TunnelHosts),
		LocalViaTunnel: p.LocalViaTunnel,
		PoolSize:       p.PoolSize, KeepAliveSec: p.KeepAliveSec, ConnectTimeoutSec: p.ConnectTimeoutSec,
		Cipher: p.Cipher, IPVersion: p.IPVersion,
		MeshName: p.MeshName, MeshIncoming: p.MeshIncoming, MeshDirectOff: p.MeshDirectOff, ShareLAN: p.ShareLAN,
	}
}

// setRules кладёт правила и тонкую настройку связи из pr в профиль (его
// собственная копия при OwnRules).
func setRules(p *Profile, pr Prefs) {
	p.FilterMode, p.FilterApps = pr.FilterMode, pr.FilterApps
	p.DirectHosts, p.TunnelHosts, p.LocalViaTunnel = pr.DirectHosts, pr.TunnelHosts, pr.LocalViaTunnel
	p.PoolSize, p.KeepAliveSec, p.ConnectTimeoutSec = pr.PoolSize, pr.KeepAliveSec, pr.ConnectTimeoutSec
	p.Cipher, p.IPVersion = pr.Cipher, pr.IPVersion
}

// Resolve — профиль p с действующими значениями: общие настройки программы
// поверх его полей; правила и тонкая настройка — свои, если у него
// OwnRules. Конфиг старого формата (PrefsV == 0, например собранный в коде
// телефона или в тесте) — p как есть.
func (c Config) Resolve(p Profile) Profile {
	if c.PrefsV < PrefsVersion {
		return p
	}
	pr := c.Prefs
	if !p.OwnRules {
		setRules(&p, pr)
		// Ноль — «по умолчанию», как у нового профиля.
		if p.PoolSize < 1 {
			p.PoolSize = defaultPoolSize
		}
		if p.FilterMode == "" {
			p.FilterMode = "all"
		}
	}
	p.MeshName, p.MeshIncoming, p.MeshDirectOff, p.ShareLAN = pr.MeshName, pr.MeshIncoming, pr.MeshDirectOff, pr.ShareLAN
	return p
}

// EditRules меняет правила сервера id там, где они живут: у сервера с
// «своими правилами» — его копию, иначе — общие настройки программы.
func (c *Config) EditRules(id string, f func(r *Prefs)) {
	for i := range c.Profiles {
		p := &c.Profiles[i]
		if p.ID != id {
			continue
		}
		if c.PrefsV < PrefsVersion || p.OwnRules {
			r := prefsOf(*p)
			f(&r)
			setRules(p, r)
			return
		}
	}
	f(&c.Prefs)
}

// PutRules — правила и тонкую настройку связи из p (обычно — Active(),
// поправленный флагами консольной версии) положить туда, где они живут у
// сервера p.ID: общие настройки или его «свои правила».
func (c *Config) PutRules(p Profile) {
	c.EditRules(p.ID, func(r *Prefs) {
		r.FilterMode, r.FilterApps = p.FilterMode, p.FilterApps
		r.DirectHosts, r.TunnelHosts, r.LocalViaTunnel = p.DirectHosts, p.TunnelHosts, p.LocalViaTunnel
		r.PoolSize, r.KeepAliveSec, r.ConnectTimeoutSec = p.PoolSize, p.KeepAliveSec, p.ConnectTimeoutSec
		r.Cipher, r.IPVersion = p.Cipher, p.IPVersion
	})
}

// FillPrefsFrom — сервер пришёл из файла или от панели (импорт, выгрузка
// настроек): его правила и прочие общие настройки заполняют общие, только
// если там пусто. Свои настройки человека чужой профиль не перетирает.
func (c *Config) FillPrefsFrom(p Profile) (changed bool) {
	if c.PrefsV < PrefsVersion {
		return false
	}
	pr := &c.Prefs
	if len(pr.DirectHosts) == 0 && len(p.DirectHosts) > 0 {
		pr.DirectHosts, changed = slices.Clone(p.DirectHosts), true
	}
	if len(pr.TunnelHosts) == 0 && len(p.TunnelHosts) > 0 {
		pr.TunnelHosts, changed = slices.Clone(p.TunnelHosts), true
	}
	if len(pr.FilterApps) == 0 && (pr.FilterMode == "" || pr.FilterMode == "all") &&
		p.FilterMode != "" && p.FilterMode != "all" && len(p.FilterApps) > 0 {
		pr.FilterMode, pr.FilterApps, changed = p.FilterMode, slices.Clone(p.FilterApps), true
	}
	if pr.MeshName == "" && p.MeshName != "" {
		pr.MeshName, changed = p.MeshName, true
	}
	return changed
}

// SetOwnRules включает или выключает «Свои правила для этого сервера».
// Включение начинает с копии общих настроек — дальше их правят отдельно.
func (c *Config) SetOwnRules(id string, on bool) {
	for i := range c.Profiles {
		p := &c.Profiles[i]
		if p.ID != id || p.OwnRules == on {
			continue
		}
		if on {
			pr := c.Prefs
			pr.FilterApps = slices.Clone(pr.FilterApps)
			pr.DirectHosts = slices.Clone(pr.DirectHosts)
			pr.TunnelHosts = slices.Clone(pr.TunnelHosts)
			setRules(p, pr)
		}
		p.OwnRules = on
	}
}

func (pr *Prefs) normalize() {
	switch pr.FilterMode {
	case "only", "except":
	default:
		pr.FilterMode = "all"
	}
	if pr.PoolSize < 0 {
		pr.PoolSize = 0
	}
	if pr.PoolSize > 16 {
		pr.PoolSize = 16
	}
	pr.KeepAliveSec = clampOrZero(pr.KeepAliveSec, MinKeepAliveSec, MaxKeepAliveSec)
	pr.ConnectTimeoutSec = clampOrZero(pr.ConnectTimeoutSec, MinConnectTimeoutSec, MaxConnectTimeoutSec)
	switch pr.Cipher {
	case "aes", "chacha":
	default:
		pr.Cipher = ""
	}
	switch pr.IPVersion {
	case "4", "6":
	default:
		pr.IPVersion = ""
	}
}
