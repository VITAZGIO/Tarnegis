package config

// Устройство, выданное панелью (ТЗ 47/48). Устройство — учётная запись в
// сети серверов: какие серверы ему можно и есть ли у него сеть устройств,
// решает панель (профиль устройства, invite.Profile), а программа только
// следует. Свои у устройства — предпочтения: включена ли у него сеть
// устройств, «только этот сервер», «Выход через». Они одни на устройство,
// а не на каждый сервер (раньше их приходилось повторять в каждом профиле).
//
// Профили серверов из панели помечены FromPanel: адрес, порт, пользователь
// и ключ берутся из устройства и правке не поддаются; удалить такой профиль
// нельзя — только скрыть у себя. Серверы, добавленные руками, живут как
// раньше и этого кода не касаются.
//
// Решает только этот код: экран, API и мост телефона его не повторяют.

import (
	"slices"
	"strings"
	"time"
)

// Device — устройство из панели.
type Device struct {
	// ID — учётная запись устройства (пользователь SSH, «tun_ab12cd»).
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// KeyPath — закрытый ключ устройства, созданный на нём самом.
	KeyPath string `json:"keyPath"`
	// Join — «адрес:порт» сервера, через который вошли по приглашению.
	Join string `json:"join,omitempty"`

	// То, что решила панель (последний профиль — для запуска без связи).
	Rev     int64          `json:"rev"`
	Mesh    bool           `json:"mesh"`
	Frozen  bool           `json:"frozen,omitempty"`
	Servers []DeviceServer `json:"servers"`
	// ProfileFrom, ProfileVer — откуда и от какой версии панели последний
	// профиль (ТЗ 50).
	ProfileFrom string `json:"profileFrom,omitempty"`
	ProfileVer  string `json:"profileVer,omitempty"`

	// Hidden — серверы из панели, скрытые у себя (ServerKey).
	Hidden []string `json:"hidden,omitempty"`
	// Net — сеть устройств на этом устройстве (предпочтения). nil — ещё не
	// задана: действуют значения из профилей, как до ТЗ 48 (DeviceNetOf).
	Net *DeviceNet `json:"net,omitempty"`
}

// DeviceServer — сервер из профиля устройства.
type DeviceServer struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	HostKey string `json:"hk"`
	Flag    string `json:"flag,omitempty"`
}

// Key — «адрес:порт», как ServerKey профиля.
func (s DeviceServer) Key() string { return ServerKey(Profile{Host: s.Host, SSHPort: s.Port}) }

// DeviceNet — предпочтения сети устройств, одни на устройство. Имя
// устройства, входящие и прямые соединения — в Prefs (общие с ТЗ 32), режим
// при запуске — MeshStartMode.
type DeviceNet struct {
	// On — сеть устройств включена на этом устройстве. Панель её
	// выключила (Device.Mesh == false) — On сохраняется и снова действует,
	// когда панель вернёт доступ.
	On bool `json:"on"`
	// Key — ключ сети устройств.
	Key string `json:"key,omitempty"`
	// OnlyThis — сеть устройств только через активный сервер.
	OnlyThis bool `json:"onlyThis,omitempty"`
	// ExitVia — «Выход через» для серверов из панели: id профилей по
	// порядку (сам сервер из своей цепочки выпадает).
	ExitVia []string `json:"exitVia,omitempty"`
	// ExitT, ExitBy — когда и кто задал цепочку (как Profile.ExitT): правка
	// цепочки одного профиля из панели (экран, панель сервера, ТЗ 16) новее —
	// она и становится цепочкой устройства.
	ExitT  int64  `json:"exitT,omitempty"`
	ExitBy string `json:"exitBy,omitempty"`
}

// DeviceProfile — профиль устройства от панели (invite.Profile.Config).
type DeviceProfile struct {
	ID      string
	Name    string
	Rev     int64
	Servers []DeviceServer
	Mesh    bool
	Frozen  bool
	MeshKey string
	// From — сервер, с которого прочитан профиль; Ver — версия его панели
	// (пусто — панель старше ТЗ 50). Для строки «обнови сервер …».
	From, Ver string
}

// DeviceResult — что поменялось.
type DeviceResult struct {
	// Changed — профиль применён (rev вырос или первый раз).
	Changed bool
	Added   []Profile
	Removed []string
	// Pins — ключи серверов, которые надо записать в known_hosts.
	Pins []DeviceServer
	// Switched — активный сервер убрали, и программа перешла на этот
	// (имя); пусто — не переходила.
	Switched string
	// MeshOff — панель выключила сеть устройств (было можно — стало нельзя);
	// MeshBack — вернула.
	MeshOff, MeshBack bool
	// NetJoined — устройство перешло в сеть панели (ключ из профиля новый).
	NetJoined bool
}

// panelKey — какой сервер профиля устройства этот профиль.
func panelKey(p Profile) string {
	if p.PanelKey != "" {
		return p.PanelKey
	}
	return ServerKey(p)
}

// IsPanel — профиль сервера из панели.
func (p Profile) IsPanel() bool { return p.FromPanel != "" }

// SetDevice ставит устройство после входа по приглашению и применяет его
// первый профиль (rev не сравнивается). Прежнее устройство (другая
// учётная запись) уходит вместе со своими профилями.
func (c *Config) SetDevice(d Device, dp DeviceProfile) DeviceResult {
	if c.Device != nil && c.Device.ID != d.ID {
		c.dropPanelProfiles(c.Device.ID)
	}
	if d.Net == nil {
		n := c.DeviceNetOf()
		n.On = true
		// Сеть устройства из панели — только сеть панели (ТЗ 49): ключ из
		// профиля, своего устройство не заводит.
		n.Key = dp.MeshKey
		d.Net = &n
	}
	d.Rev = -1
	c.Device = &d
	return c.applyDevice(dp)
}

// ApplyDevice применяет профиль устройства, если его rev вырос. Профиль
// другой учётной записи не применяется.
func (c *Config) ApplyDevice(dp DeviceProfile) DeviceResult {
	if c.Device == nil || dp.ID != c.Device.ID {
		return DeviceResult{}
	}
	// Ключ сети панели другой (панель обновили, а rev тот же) — тоже правка.
	keyNew := dp.MeshKey != "" && (c.Device.Net == nil || c.Device.Net.Key != dp.MeshKey)
	if dp.Rev <= c.Device.Rev && !keyNew {
		return DeviceResult{}
	}
	return c.applyDevice(dp)
}

func (c *Config) applyDevice(dp DeviceProfile) DeviceResult {
	d := c.Device
	res := DeviceResult{Changed: true}
	wasMesh := d.Rev >= 0 && d.Mesh && !d.Frozen
	oldNames := map[string]string{}
	for _, s := range d.Servers {
		oldNames[s.Key()] = s.Name
	}
	d.Rev, d.Mesh, d.Frozen = dp.Rev, dp.Mesh, dp.Frozen
	d.ProfileFrom, d.ProfileVer = dp.From, dp.Ver
	if dp.Name != "" {
		d.Name = dp.Name
	}
	if d.Net != nil {
		// Ключ сети — только от панели. Профиль без ключа (панель старше
		// ТЗ 49) — сети устройств нет, а не своя сеть из одного устройства.
		res.NetJoined = dp.MeshKey != "" && d.Net.Key != dp.MeshKey
		d.Net.Key = dp.MeshKey
	}
	// Повторы адреса в профиле — один сервер.
	seen := map[string]bool{}
	var list []DeviceServer
	for _, s := range dp.Servers {
		if s.Port <= 0 {
			s.Port = 22
		}
		k := s.Key()
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		list = append(list, s)
	}
	d.Servers = list
	nowMesh := d.Mesh && !d.Frozen
	res.MeshOff = wasMesh && !nowMesh
	res.MeshBack = !wasMesh && nowMesh && d.Rev >= 0 && len(oldNames) > 0

	// Убранные панелью — удалить (их адрес и вход не наши, править нечего).
	activeGone := ""
	out := c.Profiles[:0:0]
	for _, p := range c.Profiles {
		if p.FromPanel == d.ID && !seen[panelKey(p)] {
			res.Removed = append(res.Removed, p.Name)
			if p.ID == c.ActiveProfile {
				activeGone = p.ID
			}
			continue
		}
		out = append(out, p)
	}
	c.Profiles = out

	// Новые — завести, уже заведённые — освежить.
	for _, s := range list {
		k := s.Key()
		i := slices.IndexFunc(c.Profiles, func(p Profile) bool { return p.FromPanel == d.ID && panelKey(p) == k })
		if i < 0 {
			// Профиль этого сервера под этой же учётной записью человек уже
			// завёл сам (до ТЗ 48 или руками) — он и становится профилем из
			// панели, второй не нужен.
			i = slices.IndexFunc(c.Profiles, func(p Profile) bool {
				return p.FromPanel == "" && ServerKey(p) == k && p.User == d.ID
			})
			if i >= 0 {
				c.Profiles[i].FromPanel, c.Profiles[i].PanelKey = d.ID, k
			}
		}
		if i < 0 {
			p := defaultProfile(len(c.Profiles) + 1)
			p.Name, p.Flag = s.Name, s.Flag
			if p.Name == "" {
				p.Name = s.Host
			}
			p.Host, p.SSHPort = s.Host, s.Port
			p.User, p.KeyPath, p.FromPanel, p.PanelKey = d.ID, d.KeyPath, d.ID, k
			c.Profiles = append(c.Profiles, p)
			i = len(c.Profiles) - 1
			res.Added = append(res.Added, p)
		} else {
			p := &c.Profiles[i]
			// Имя из панели меняется, только если человек его не правил.
			if old, ok := oldNames[k]; ok && s.Name != "" && p.Name == old {
				p.Name = s.Name
			}
			if p.Flag == "" {
				p.Flag = s.Flag
			}
		}
		if s.HostKey != "" {
			res.Pins = append(res.Pins, s)
		}
	}
	if _, ok := c.ProfileByID(c.ActiveProfile); !ok || activeGone != "" {
		if p, ok := c.firstVisible(); ok {
			c.ActiveProfile = p.ID
			if activeGone != "" {
				res.Switched = p.Name
			}
		}
	}
	c.syncDevice()
	return res
}

// firstVisible — активным после удаления: сначала нескрытый сервер из
// панели, потом любой нескрытый, потом любой.
func (c *Config) firstVisible() (Profile, bool) {
	for _, p := range c.Profiles {
		if p.IsPanel() && !c.hidden(p) {
			return p, true
		}
	}
	for _, p := range c.Profiles {
		if !c.hidden(p) {
			return p, true
		}
	}
	if len(c.Profiles) > 0 {
		return c.Profiles[0], true
	}
	return Profile{}, false
}

func (c *Config) hidden(p Profile) bool {
	return c.Device != nil && p.FromPanel == c.Device.ID && slices.Contains(c.Device.Hidden, panelKey(p))
}

// SetHidden скрывает (или возвращает) сервер из панели у себя. Скрыли
// активный — активным становится другой видимый. Возвращает имя нового
// активного, если он сменился.
func (c *Config) SetHidden(id string, hide bool) (switched string, ok bool) {
	p, found := c.ProfileByID(id)
	if !found || c.Device == nil || p.FromPanel != c.Device.ID {
		return "", false
	}
	k := panelKey(p)
	c.Device.Hidden = slices.DeleteFunc(slices.Clone(c.Device.Hidden), func(s string) bool { return s == k })
	if hide {
		c.Device.Hidden = append(c.Device.Hidden, k)
	}
	c.syncDevice()
	if hide && c.ActiveProfile == id {
		if q, ok := c.firstVisible(); ok && q.ID != id {
			c.ActiveProfile = q.ID
			switched = q.Name
		}
	}
	return switched, true
}

// SetDeviceNet меняет предпочтения сети устройств на устройстве.
func (c *Config) SetDeviceNet(n DeviceNet) bool {
	if c.Device == nil {
		return false
	}
	old := c.DeviceNetOf()
	// Ключ сети — от панели (ТЗ 49), не от экрана.
	n.Key = old.Key
	n.ExitVia = slices.Clone(n.ExitVia)
	n.ExitT, n.ExitBy = old.ExitT, old.ExitBy
	if strings.Join(n.ExitVia, "\x00") != strings.Join(old.ExitVia, "\x00") {
		now := time.Now().Unix()
		for _, p := range c.Profiles {
			if p.FromPanel == c.Device.ID && p.ExitT >= now {
				now = p.ExitT + 1
			}
		}
		if now <= n.ExitT {
			now = n.ExitT + 1
		}
		n.ExitT, n.ExitBy = now, ""
	}
	c.Device.Net = &n
	c.syncDevice()
	return true
}

// DeviceNetOf — действующие предпочтения сети устройств устройства. Блок
// ещё не задан (конфиг до ТЗ 48) — читаются старые значения из профилей:
// первого профиля из панели, иначе активного.
func (c Config) DeviceNetOf() DeviceNet {
	if c.Device != nil && c.Device.Net != nil {
		n := *c.Device.Net
		n.ExitVia = slices.Clone(n.ExitVia)
		return n
	}
	src, ok := Profile{}, false
	if c.Device != nil {
		for _, p := range c.Profiles {
			if p.FromPanel == c.Device.ID {
				src, ok = p, true
				break
			}
		}
	}
	if !ok {
		src = c.rawActive()
	}
	return DeviceNet{On: src.MeshEnabled, Key: src.MeshKey, OnlyThis: src.MeshOnlyThis, ExitVia: slices.Clone(src.ExitVia),
		ExitT: src.ExitT, ExitBy: src.ExitBy}
}

// DeviceMeshAllowed — пускает ли панель устройство в сеть устройств.
func (c Config) DeviceMeshAllowed() bool {
	return c.Device == nil || (c.Device.Mesh && !c.Device.Frozen)
}

// DeviceNetMissing — устройству можно в сеть устройств и оно её включило,
// но панель не выдала ключ сети (сервер старше ТЗ 49): сети нет.
func (c Config) DeviceNetMissing() bool {
	if c.Device == nil || !c.DeviceMeshAllowed() {
		return false
	}
	n := c.DeviceNetOf()
	return n.On && n.Key == ""
}

// DeviceNetMissingText — что сказать человеку при DeviceNetMissing: какой
// сервер отдал профиль без сети и какая на нём версия панели (ТЗ 50).
func (c Config) DeviceNetMissingText() string {
	if c.Device == nil || c.Device.ProfileFrom == "" {
		return "панель не выдала сеть устройств — обнови сервер"
	}
	ver := c.Device.ProfileVer
	if ver == "" {
		ver = "старше v1.10.24"
	}
	return "панель сервера " + c.Device.ProfileFrom + " не выдала сеть устройств — обнови этот сервер (панель " + ver + ")"
}

// dropPanelProfiles — убрать профили прежнего устройства.
func (c *Config) dropPanelProfiles(id string) {
	c.Profiles = slices.DeleteFunc(c.Profiles, func(p Profile) bool { return p.FromPanel == id })
}

// SyncDevice — syncDevice для тех, кто держит конфиг в памяти без Save
// (App.SetConfig). Профили правятся на месте — зовущий передаёт свою копию.
func (c *Config) SyncDevice() {
	if c.Device != nil {
		c.Profiles = slices.Clone(c.Profiles)
	}
	c.syncDevice()
}

// syncDevice переписывает в профили из панели то, что решают устройство и
// панель: адрес, порт, пользователь, ключ, скрытость и действующую сеть
// устройств. Так все, кто читает профиль как есть (туннель, сеть устройств,
// мост телефона), видят одно и то же. Зовётся из normalize — правка профиля
// из панели руками (POST /api/config) этих полей не меняет.
func (c *Config) syncDevice() {
	d := c.Device
	if d == nil {
		return
	}
	byKey := map[string]DeviceServer{}
	for _, s := range d.Servers {
		byKey[s.Key()] = s
	}
	// Профиль сервера из панели удалить нельзя: пропал (настройки целиком от
	// экрана, правка файла) — заводится снова.
	for _, s := range d.Servers {
		k := s.Key()
		if !slices.ContainsFunc(c.Profiles, func(p Profile) bool { return p.FromPanel == d.ID && panelKey(p) == k }) {
			p := defaultProfile(len(c.Profiles) + 1)
			p.Name, p.Flag = s.Name, s.Flag
			if p.Name == "" {
				p.Name = s.Host
			}
			p.FromPanel, p.PanelKey = d.ID, k
			c.Profiles = append(c.Profiles, p)
		}
	}
	// Цепочку одного профиля из панели поменяли позже, чем цепочку
	// устройства (экран по профилю, панель сервера), — она и есть новая
	// цепочка устройства: «A через B» значит «все через B».
	if n := d.Net; n != nil {
		for _, p := range c.Profiles {
			if p.FromPanel == d.ID && p.ExitT > n.ExitT {
				n.ExitVia, n.ExitT, n.ExitBy = slices.Clone(p.ExitVia), p.ExitT, p.ExitBy
			}
		}
	}
	for i := range c.Profiles {
		p := &c.Profiles[i]
		if p.FromPanel == "" {
			continue
		}
		if p.FromPanel != d.ID {
			p.FromPanel, p.PanelKey = "", "" // от прежнего устройства — теперь обычный профиль
			p.Hidden = false
			continue
		}
		if p.PanelKey == "" {
			p.PanelKey = ServerKey(*p)
		}
		if s, ok := byKey[p.PanelKey]; ok {
			p.Host, p.SSHPort = s.Host, s.Port
		}
		p.User, p.KeyPath = d.ID, d.KeyPath
		p.Hidden = slices.Contains(d.Hidden, p.PanelKey)
		p.Panel, p.ClientID, p.MeshAuto = "", "", ""
		if d.Net == nil {
			continue // старые значения профиля, как до ТЗ 48
		}
		n := d.Net
		// Без ключа от панели сети нет (своего ключа устройство из панели
		// не заводит, ТЗ 49).
		p.MeshEnabled = n.On && d.Mesh && !d.Frozen && n.Key != ""
		p.MeshKey = n.Key
		p.MeshOnlyThis = n.OnlyThis
		via := slices.DeleteFunc(slices.Clone(n.ExitVia), func(id string) bool { return id == p.ID || id == "" })
		if len(via) > MaxExitHops {
			via = via[:MaxExitHops]
		}
		p.ExitVia, p.ExitT, p.ExitBy = via, n.ExitT, n.ExitBy
	}
}

// DeviceForExport — устройство для выгрузки всех настроек: без пути к
// ключу (ключ едет в профилях его серверов), цепочка — ключами серверов
// «адрес:порт», а не id профилей.
func (c Config) DeviceForExport() *Device {
	if c.Device == nil {
		return nil
	}
	d := *c.Device
	d.KeyPath = ""
	d.Servers, d.Hidden = slices.Clone(d.Servers), slices.Clone(d.Hidden)
	n := c.DeviceNetOf()
	var via []string
	for _, id := range n.ExitVia {
		if p, ok := c.ProfileByID(id); ok {
			via = append(via, panelKey(p))
		}
	}
	n.ExitVia = via
	d.Net = &n
	return &d
}

// DeviceFromExport — устройство из выгрузки на профили, уже собранные из
// неё же: ключ — тот, что приехал с его серверами, цепочка — снова id.
func DeviceFromExport(d *Device, profiles []Profile) *Device {
	if d == nil || d.ID == "" {
		return nil
	}
	out := *d
	out.KeyPath = ""
	for _, p := range profiles {
		if p.FromPanel == d.ID && p.KeyPath != "" {
			out.KeyPath = p.KeyPath
			break
		}
	}
	if out.Net != nil {
		n := *out.Net
		var ids []string
		for _, k := range n.ExitVia {
			for _, p := range profiles {
				if panelKey(p) == k {
					ids = append(ids, p.ID)
					break
				}
			}
		}
		n.ExitVia = ids
		out.Net = &n
	}
	return &out
}
