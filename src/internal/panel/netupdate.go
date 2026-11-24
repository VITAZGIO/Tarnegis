package panel

// Обновление сети устройств (ТЗ 40) — сторона панели.
//
//   - Версия сети — своя версия панели или запись «Обновить на всех
//     серверах» (upd:<версия>), что новее; панель отдаёт её своему meshd
//     (/v1/netver), тот говорит устройствам. Устройство старше и с
//     автообновлением обновляется само, файл берёт с GitHub.
//   - Устройство с ролью «Моё устройство» просит «обновить сеть до vX»
//     (meshd проверяет роль по SSH-пользователю и копит просьбы). Панель
//     забирает их раз в несколько секунд и принимает только номер, который
//     новее версии сети и равен последнему официальному релизу проекта на
//     GitHub; тогда — «Обновить на всех серверах» до него (ТЗ 31), а сервер
//     вне сети серверов просто обновляет себя кнопкой «Обновить».
//   - Список устройств сети с версиями, «Обновить» по одному и всем, галочка
//     «Получать автообновление» — ручки ниже (server_netupdate.go).

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"sshtunnel/internal/meshsvc"
)

// netUpdAdmin — что умеет meshd 8; у старого нет (ErrUnsupported).
type netUpdAdmin interface {
	SetNetVersion(ctx context.Context, ver string) error
	DevUpdate(ctx context.Context, netID, device string) error
	SetAutoUpdate(ctx context.Context, netID, device string, on bool) (int64, error)
	NetUpdates(ctx context.Context, answers []meshsvc.NetUpdateAnswer) ([]meshsvc.NetUpdateReq, error)
}

const (
	// netUpdEvery — как часто забирать просьбы у meshd.
	netUpdEvery = 5 * time.Second
	// netVerRepush — повторить версию сети meshd (он мог перезапуститься).
	netVerRepush = time.Minute
)

// netUpd — состояние обновления сети на этом сервере (в памяти).
type netUpd struct {
	mu       sync.Mutex
	local    string // «обновить сеть» на сервере вне сети серверов
	pushed   string
	pushedAt time.Time
	answers  []meshsvc.NetUpdateAnswer
	// latest — последний официальный релиз (в тестах подменяется).
	latest func(ctx context.Context) (string, error)
}

func (n *ServerNet) netAdmin() (netUpdAdmin, bool) {
	a, ok := n.mesh.admin.(netUpdAdmin)
	return a, ok
}

// NetTarget — версия сети: версия панели, «обновить на всех» или просьба
// устройства на одиночном сервере — что новее.
func (n *ServerNet) NetTarget() string {
	v := cleanVersion(n.panelVersion())
	n.mu.Lock()
	if t, _, _ := n.updTargetLocked(); t != "" && (v == "" || versionLess(v, t)) {
		v = t
	}
	n.mu.Unlock()
	n.nu.mu.Lock()
	if l := n.nu.local; l != "" && (v == "" || versionLess(v, l)) {
		v = l
	}
	n.nu.mu.Unlock()
	return v
}

// pushNetVersion — версия сети своему meshd, если она другая или давно.
func (n *ServerNet) pushNetVersion(ctx context.Context, force bool) error {
	na, ok := n.netAdmin()
	if !ok {
		return meshsvc.ErrUnsupported
	}
	v := n.NetTarget()
	n.nu.mu.Lock()
	same := v == n.nu.pushed && n.now().Sub(n.nu.pushedAt) < netVerRepush
	n.nu.mu.Unlock()
	if same && !force {
		return nil
	}
	if err := na.SetNetVersion(ctx, v); err != nil {
		return err
	}
	n.nu.mu.Lock()
	n.nu.pushed, n.nu.pushedAt = v, n.now()
	n.nu.mu.Unlock()
	return nil
}

// netUpdLoop — версия сети и просьбы устройств, пока жив ctx.
func (n *ServerNet) netUpdLoop(ctx context.Context) {
	for ctx.Err() == nil {
		wait := netUpdEvery
		if err := n.netUpdTick(ctx); errors.Is(err, meshsvc.ErrUnsupported) || errors.Is(err, meshsvc.ErrNotRunning) {
			wait = time.Minute // старый meshd или его нет — обновят, заработает
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// netUpdTick — один круг: версия сети, ответы, новые просьбы.
func (n *ServerNet) netUpdTick(ctx context.Context) error {
	na, ok := n.netAdmin()
	if !ok {
		return meshsvc.ErrUnsupported
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := n.pushNetVersion(cctx, false); err != nil {
		return err
	}
	for i := 0; i < 3; i++ {
		n.nu.mu.Lock()
		ans := n.nu.answers
		n.nu.answers = nil
		n.nu.mu.Unlock()
		reqs, err := na.NetUpdates(cctx, ans)
		if err != nil {
			n.nu.mu.Lock()
			n.nu.answers = append(ans, n.nu.answers...)
			n.nu.mu.Unlock()
			return err
		}
		if len(reqs) == 0 {
			return nil
		}
		for _, q := range reqs {
			a := meshsvc.NetUpdateAnswer{Call: q.Call}
			if err := n.netUpdateRequest(cctx, q); err != nil {
				a.Error = err.Error()
				n.mesh.logf("обновить сеть: просьба %s (%s) до %s отклонена: %v", q.Name, q.User, q.Version, err)
			} else {
				n.mesh.logf("обновить сеть: %s (%s) попросило обновить сеть до %s", q.Name, q.User, q.Version)
			}
			n.nu.mu.Lock()
			n.nu.answers = append(n.nu.answers, a)
			n.nu.mu.Unlock()
		}
	}
	return nil
}

// userMine — у SSH-пользователя роль «Моё устройство» (по учётным записям
// панели). Учётных записей не видно (тесты без Server) — верим meshd.
func (n *ServerNet) userMine(user string) bool {
	if n.srv == nil || n.srv.clients == nil {
		return true
	}
	for _, c := range n.srv.clients.List() {
		if c.Username == user {
			return slices.Contains(c.Groups, groupMine)
		}
	}
	return false
}

// latestRelease — номер последнего официального релиза на GitHub.
func (n *ServerNet) latestRelease(ctx context.Context) (string, error) {
	if n.nu.latest != nil {
		return n.nu.latest(ctx)
	}
	rel, err := selfUpdateLatest(ctx)
	if err != nil {
		return "", err
	}
	return cleanVersion(rel.Tag), nil
}

// netUpdateRequest — проверить просьбу устройства и обновить сеть. Ошибка —
// отказ (её текст уйдёт устройству).
func (n *ServerNet) netUpdateRequest(ctx context.Context, q meshsvc.NetUpdateReq) error {
	ver := cleanVersion(q.Version)
	if ver == "" {
		return errors.New("неверный номер версии")
	}
	if !n.userMine(q.User) {
		return errors.New("обновить сеть может только устройство с ролью «Моё устройство»")
	}
	if cur := n.NetTarget(); cur != "" && !versionLess(cur, ver) {
		return errors.New("сеть уже на версии " + cur + " — вниз и вбок не обновляем")
	}
	latest, err := n.latestRelease(ctx)
	if err != nil {
		return errors.New("не проверить релиз на GitHub: " + err.Error())
	}
	if latest != ver {
		return errors.New("v" + ver + " — не последний официальный релиз проекта (последний — v" + latest + ")")
	}
	if n.InNet() {
		return n.UpdateAll(ctx, ver)
	}
	n.nu.mu.Lock()
	n.nu.local = ver
	n.nu.mu.Unlock()
	n.pushNetVersion(ctx, true)
	if err := n.selfUpdateFn(); err != nil && !errors.Is(err, errUpdateRunning) {
		n.mesh.logf("обновить сеть: панель не обновилась: %v", err)
	}
	return nil
}

// ---------- список устройств для страницы ----------

// Шаги устройства в списке «Устройства сети».
const (
	devUpdOK      = "ok"      // не старше версии сети
	devUpdBehind  = "behind"  // отстаёт (на связи — можно «Обновить»)
	devUpdOffline = "offline" // отстаёт и не на связи — обновится, когда вернётся
	devUpdOff     = "off"     // отстаёт, автообновление выключено
	devUpdUnknown = "unknown" // версия неизвестна (старый клиент или сборка не из релиза)
)

// NetDev — устройство в списке обновления сети.
type NetDev struct {
	Net      string `json:"net"`
	NetName  string `json:"netName,omitempty"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	Platform string `json:"platform,omitempty"`
	Build    string `json:"build,omitempty"`
	Online   bool   `json:"online"`
	Here     bool   `json:"here"`
	LastSeen int64  `json:"lastSeen,omitempty"`
	// AutoUpd — "on" / "off" / "" (старый клиент: не участвует).
	AutoUpd string `json:"autoUpd,omitempty"`
	UpdSt   string `json:"updSt,omitempty"`
	State   string `json:"state"`
	// CanUpdate — кнопка «Обновить»: на связи с этим сервером и отстаёт.
	CanUpdate bool `json:"canUpdate,omitempty"`
}

// NetDevs — ответ GET /api/update/devices.
type NetDevs struct {
	Target  string   `json:"target,omitempty"`
	Devices []NetDev `json:"devices"`
	// Old — meshd этого сервера старый: команд устройствам нет.
	Old bool `json:"old,omitempty"`
}

// netDevState — шаг устройства по его версии и связи.
func netDevState(d *NetDev, target string) {
	v := strings.TrimPrefix(d.Version, "v")
	switch {
	case target == "":
		d.State = devUpdOK
	case cleanVersion(v) == "":
		d.State = devUpdUnknown
	case !versionLess(v, target):
		d.State = devUpdOK
	case !d.Online:
		d.State = devUpdOffline
	case d.AutoUpd == "off":
		d.State = devUpdOff
	default:
		d.State = devUpdBehind
	}
	d.CanUpdate = d.Here && d.Online && (d.State == devUpdBehind || d.State == devUpdOff) && d.AutoUpd != ""
}

// NetDevices — устройства сети с версиями (как в разделе «Устройства»).
func (n *ServerNet) NetDevices(ctx context.Context) NetDevs {
	target := n.NetTarget()
	out := NetDevs{Target: target, Devices: []NetDev{}}
	if _, ok := n.netAdmin(); !ok {
		out.Old = true
	}
	st := n.mesh.State(ctx)
	for _, net := range st.Networks {
		for _, d := range net.Devices {
			nd := NetDev{Net: net.ID, NetName: net.Name, ID: d.ID, Name: d.Display, Version: d.App,
				Platform: d.Platform, Build: d.Build, Online: d.Online, Here: d.Here || d.Src == "" && d.Online,
				LastSeen: d.LastSeen, AutoUpd: d.AutoUpd, UpdSt: d.UpdSt}
			if nd.Name == "" {
				nd.Name = d.Name
			}
			netDevState(&nd, target)
			out.Devices = append(out.Devices, nd)
		}
	}
	sort.SliceStable(out.Devices, func(i, j int) bool {
		a, b := out.Devices[i], out.Devices[j]
		if a.Online != b.Online {
			return a.Online
		}
		return a.Name < b.Name
	})
	return out
}

// UpdateDevice — «Обновить» у одного устройства.
func (n *ServerNet) UpdateDevice(ctx context.Context, netID, device string) error {
	na, ok := n.netAdmin()
	if !ok {
		return meshsvc.ErrUnsupported
	}
	if err := n.pushNetVersion(ctx, false); err != nil {
		return err
	}
	return na.DevUpdate(ctx, netID, device)
}

// UpdateDevices — «Обновить все устройства»: каждому отстающему на связи.
// Возвращает, скольким ушла команда.
func (n *ServerNet) UpdateDevices(ctx context.Context) (int, error) {
	na, ok := n.netAdmin()
	if !ok {
		return 0, meshsvc.ErrUnsupported
	}
	if err := n.pushNetVersion(ctx, false); err != nil {
		return 0, err
	}
	sent := 0
	var last error
	for _, d := range n.NetDevices(ctx).Devices {
		if !d.CanUpdate {
			continue
		}
		if err := na.DevUpdate(ctx, d.Net, d.ID); err != nil {
			last = err
			continue
		}
		sent++
	}
	if sent == 0 && last != nil {
		return 0, last
	}
	return sent, nil
}

// SetDeviceAutoUpdate — галочка «Получать автообновление» в карточке.
func (n *ServerNet) SetDeviceAutoUpdate(ctx context.Context, netID, device string, on bool) error {
	na, ok := n.netAdmin()
	if !ok {
		return meshsvc.ErrUnsupported
	}
	_, err := na.SetAutoUpdate(ctx, netID, device, on)
	return err
}
