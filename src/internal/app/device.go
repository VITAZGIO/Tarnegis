package app

// Устройство по приглашению из панели (ТЗ 48) со стороны приложения:
// вход (Join), сверка профиля устройства при каждом подключении и раз в
// 10 минут, пока подключены к серверу из панели (deviceLoop). Что заводить,
// убирать и скрывать, решает config (ApplyDevice); формат и связь — invite.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/hostkey"
	"sshtunnel/internal/invite"
)

// deviceEvery — как часто сверять профиль на подключённом сервере из панели.
const deviceEvery = 10 * time.Minute

// DeviceSync — как прошла последняя сверка профиля (для экрана).
type DeviceSync struct {
	T   int64  `json:"t,omitempty"`
	Err string `json:"err,omitempty"`
	// Note — что поменялось последней применённой сверкой (одна строка).
	Note string `json:"note,omitempty"`
}

type deviceState struct {
	mu     sync.Mutex
	last   DeviceSync
	gen    int64 // подключение, на котором сверялись в последний раз
	at     time.Time
	busy   bool
	loopOn sync.Once
}

// ErrNoDevice — устройство не выдано панелью.
var ErrNoDevice = errors.New("устройство не добавлено по приглашению")

// DeviceSyncState — последняя сверка.
func (a *App) DeviceSyncState() DeviceSync {
	a.dev.mu.Lock()
	defer a.dev.mu.Unlock()
	return a.dev.last
}

// JoinInvite — «Добавить по приглашению»: ключ устройства, вход, профиль,
// серверы. Возвращает одну строку для человека.
func (a *App) JoinInvite(ctx context.Context, text, name, typ string) (string, error) {
	if typ == "" {
		typ = "pc"
	}
	r, err := invite.Redeem(ctx, text, invite.Options{KeyDir: config.Dir(), KnownHosts: config.KnownHostsPath(),
		Name: name, Type: typ})
	if err != nil {
		return "", err
	}
	a.exitMu.Lock()
	cfg := cloneConfig(a.Config())
	res := cfg.SetDevice(r.Device, r.Profile)
	wasEmpty := onlyEmpty(a.Config())
	if wasEmpty {
		// Первый запуск: пустой сервер по умолчанию не нужен, активным —
		// первый сервер из панели.
		cfg.Profiles = slices.DeleteFunc(cfg.Profiles, func(p config.Profile) bool { return !p.IsPanel() && p.Host == "" })
	}
	if p, ok := firstPanel(cfg); ok && (wasEmpty || cfg.Active().Host == "") {
		cfg.ActiveProfile = p.ID
	}
	_, err = a.SetConfig(cfg)
	a.exitMu.Unlock()
	if err != nil {
		return "", err
	}
	a.dev.mu.Lock()
	a.dev.last = DeviceSync{T: time.Now().Unix()}
	if r.ProfileErr != nil {
		a.dev.last.Err = r.ProfileErr.Error()
	}
	a.dev.mu.Unlock()
	a.startDeviceLoop()
	names := make([]string, 0, len(res.Added))
	for _, p := range res.Added {
		names = append(names, p.Name)
	}
	msg := "Устройство «" + r.Device.Name + "» добавлено"
	if len(names) > 0 {
		msg += ", серверы из панели: " + strings.Join(names, ", ")
	}
	a.Bus.Infof("%s", msg)
	if r.ProfileErr != nil {
		a.Bus.Warnf("Профиль устройства пока не прочитан (%v) — остальные серверы появятся при следующей сверке", r.ProfileErr)
	}
	return msg, nil
}

// SyncDevice читает профиль устройства и применяет его, если rev вырос.
func (a *App) SyncDevice(ctx context.Context) (config.DeviceResult, error) {
	cfg := a.Config()
	if cfg.Device == nil {
		return config.DeviceResult{}, ErrNoDevice
	}
	a.dev.mu.Lock()
	if a.dev.busy {
		a.dev.mu.Unlock()
		return config.DeviceResult{}, errors.New("сверка уже идёт")
	}
	a.dev.busy = true
	a.dev.mu.Unlock()
	defer func() {
		a.dev.mu.Lock()
		a.dev.busy = false
		a.dev.mu.Unlock()
	}()

	prof, err := a.fetchDevice(ctx, cfg)
	a.dev.mu.Lock()
	a.dev.last.T = time.Now().Unix()
	a.dev.last.Err = ""
	if err != nil {
		a.dev.last.Err = err.Error()
	}
	a.dev.mu.Unlock()
	if err != nil {
		return config.DeviceResult{}, err
	}
	return a.applyDevice(prof.Config())
}

// fetchDevice — профиль через подключённый туннель, если он к серверу из
// панели, иначе своим коротким SSH к первому видимому серверу из панели.
func (a *App) fetchDevice(ctx context.Context, cfg config.Config) (invite.Profile, error) {
	if id := a.EffectiveProfileID(); id != "" {
		if p, ok := cfg.ProfileByID(id); ok && p.FromPanel == cfg.Device.ID {
			prof, err := invite.FetchVia(ctx, a.DialServer)
			if err == nil {
				return prof, nil
			}
		}
	}
	signer, err := invite.LoadKey(cfg.Device.KeyPath)
	if err != nil {
		return invite.Profile{}, errors.New("нет ключа устройства: " + err.Error())
	}
	var last error = errors.New("нет серверов из панели")
	for _, p := range cfg.Profiles {
		if p.FromPanel != cfg.Device.ID || p.Hidden || p.Host == "" {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		prof, err := invite.Fetch(cctx, invite.Target{Host: p.Host, Port: p.SSHPort, User: cfg.Device.ID, Signer: signer,
			HostKey: hostkey.Callback(config.KnownHostsPath(), nil)})
		cancel()
		if err == nil {
			return prof, nil
		}
		last = err
	}
	return invite.Profile{}, last
}

// applyDevice применяет профиль: новые серверы — с записанным ключом,
// убранные — прочь; убрали подключённый — переход на другой и одна строка.
func (a *App) applyDevice(dp config.DeviceProfile) (config.DeviceResult, error) {
	a.exitMu.Lock()
	cfg := cloneConfig(a.Config())
	oldActive := cfg.ActiveProfile
	res := cfg.ApplyDevice(dp)
	if !res.Changed {
		a.exitMu.Unlock()
		return res, nil
	}
	invite.PinServers(config.KnownHostsPath(), res.Pins)
	_, err := a.SetConfig(cfg)
	a.exitMu.Unlock()
	if err != nil {
		return res, err
	}
	var notes []string
	for _, p := range res.Added {
		notes = append(notes, "добавлен сервер "+p.Name)
	}
	for _, n := range res.Removed {
		notes = append(notes, "убран сервер "+n)
	}
	if res.MeshOff {
		notes = append(notes, "сеть устройств выключена на сервере")
	}
	if res.MeshBack {
		notes = append(notes, "сеть устройств снова доступна")
	}
	if len(notes) > 0 {
		line := "Панель: " + strings.Join(notes, ", ")
		a.Bus.Infof("%s", line)
		a.dev.mu.Lock()
		a.dev.last.Note = line
		a.dev.mu.Unlock()
	}
	if res.Switched != "" && cfg.ActiveProfile != oldActive {
		a.Bus.Warnf("Подключённый сервер убран панелью — переключаюсь на %s", res.Switched)
		if a.Running() {
			a.machine.Restart()
			w := a.machine.Want()
			if err := a.settle(&w.Tunnel, &w.Mesh); err != nil {
				a.Bus.Warnf("Подключиться к %s не вышло: %v", res.Switched, err)
			}
		}
	}
	return res, nil
}

// SetDeviceHidden — «скрыть» сервер из панели у себя.
func (a *App) SetDeviceHidden(id string, hide bool) (string, error) {
	a.exitMu.Lock()
	cfg := cloneConfig(a.Config())
	switched, ok := cfg.SetHidden(id, hide)
	if !ok {
		a.exitMu.Unlock()
		return "", errors.New("скрыть можно только сервер из панели")
	}
	_, err := a.SetConfig(cfg)
	a.exitMu.Unlock()
	if err != nil {
		return "", err
	}
	if switched != "" {
		if a.Running() {
			a.machine.Restart()
			w := a.machine.Want()
			a.settle(&w.Tunnel, &w.Mesh)
		}
		return "скрытый сервер был активным — теперь активен " + switched, nil
	}
	return "", nil
}

// SetDeviceNet — блок «Сеть устройств» устройства (одни настройки на все
// серверы из панели).
func (a *App) SetDeviceNet(n config.DeviceNet) (string, error) {
	a.exitMu.Lock()
	cfg := cloneConfig(a.Config())
	if !cfg.SetDeviceNet(n) {
		a.exitMu.Unlock()
		return "", ErrNoDevice
	}
	// Панель сеть выключила — выбор всё равно сохраняется и заработает,
	// когда она вернёт доступ (config.syncDevice).
	note, err := a.SetConfig(cfg)
	a.exitMu.Unlock()
	return note, err
}

// startDeviceLoop — один фоновый сверщик на программу.
func (a *App) startDeviceLoop() {
	a.dev.loopOn.Do(func() { go a.deviceLoop() })
}

// deviceLoop: на каждом новом подключении к серверу из панели — сверка
// сразу (после подъёма связи), дальше раз в deviceEvery.
func (a *App) deviceLoop() {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for range t.C {
		cfg := a.Config()
		if cfg.Device == nil || !a.machine.LinkUp() {
			continue
		}
		id := a.EffectiveProfileID()
		p, ok := cfg.ProfileByID(id)
		if !ok || !p.IsPanel() {
			continue
		}
		a.mu.Lock()
		gen := a.gen
		a.mu.Unlock()
		a.dev.mu.Lock()
		due := gen != a.dev.gen || time.Since(a.dev.at) >= deviceEvery
		if due {
			a.dev.gen, a.dev.at = gen, time.Now()
		}
		a.dev.mu.Unlock()
		if !due {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		if _, err := a.SyncDevice(ctx); err != nil && !errors.Is(err, invite.ErrNoProfile) {
			a.Bus.Infof("Профиль устройства не сверен: %v", err)
		}
		cancel()
	}
}

// cloneConfig — копия, которую можно править, не задевая a.cfg.
func cloneConfig(c config.Config) config.Config {
	c.Profiles = slices.Clone(c.Profiles)
	for i := range c.Profiles {
		c.Profiles[i].ExitVia = slices.Clone(c.Profiles[i].ExitVia)
	}
	if c.Device != nil {
		d := *c.Device
		d.Servers = slices.Clone(d.Servers)
		d.Hidden = slices.Clone(d.Hidden)
		if d.Net != nil {
			n := *d.Net
			n.ExitVia = slices.Clone(n.ExitVia)
			d.Net = &n
		}
		c.Device = &d
	}
	return c
}

func onlyEmpty(c config.Config) bool {
	return len(c.Profiles) == 1 && c.Profiles[0].Host == "" && !c.Profiles[0].IsPanel()
}

func firstPanel(c config.Config) (config.Profile, bool) {
	for _, p := range c.Profiles {
		if p.IsPanel() && !p.Hidden {
			return p, true
		}
	}
	return config.Profile{}, false
}
