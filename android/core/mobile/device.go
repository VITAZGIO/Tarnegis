package mobile

// Устройство по приглашению из панели (ТЗ 48) на телефоне — тем же общим
// кодом, что на ПК: invite (формат и связь) и config (что заводить, убирать,
// скрывать). Профили хранит приложение (Settings.kt): оно отдаёт их сюда в
// JSON профиля ПК вместе с устройством и забирает обратно — решает Go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/hostkey"
	"sshtunnel/internal/invite"
)

// InviteInfo — разбор приглашения для экрана: JSON {host,port,name,exp,expired}
// или ошибка человеку.
func InviteInfo(text string) (string, error) {
	inv, err := invite.Parse(text)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"host": inv.Host, "port": inv.Port, "name": inv.Name,
		"exp": inv.Exp, "expired": inv.Expired(time.Now())})
	return string(out), nil
}

// IsInvite — текст похож на приглашение (сканер QR и «Вставить»).
func IsInvite(text string) bool { return invite.IsInvite(text) }

// DeviceJoin — вход по приглашению: ключ устройства создаётся в keyDir,
// ключи серверов пишутся в knownHosts. Ответ — JSON {"device","profile",
// "profileErr"}: приложение передаёт его целиком в DeviceApply (op "set").
func DeviceJoin(text, name, keyDir, knownHosts string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r, err := invite.Redeem(ctx, text, invite.Options{KeyDir: keyDir, KnownHosts: knownHosts, Name: name, Type: "phone"})
	if err != nil {
		return "", err
	}
	perr := ""
	if r.ProfileErr != nil {
		perr = r.ProfileErr.Error()
	}
	out, err := json.Marshal(map[string]any{"device": r.Device, "profile": r.Profile, "profileErr": perr})
	return string(out), err
}

// DeviceFetch читает профиль устройства своим коротким SSH-соединением:
// по очереди с видимых серверов из панели (профили — JSON профиля ПК, как в
// SetProfiles). Ответ — JSON профиля для DeviceApply (op "apply").
func DeviceFetch(deviceJSON, profilesJSON, knownHosts string) (string, error) {
	var d config.Device
	if err := json.Unmarshal([]byte(deviceJSON), &d); err != nil || d.ID == "" {
		return "", errors.New("устройство не добавлено по приглашению")
	}
	var list []config.Profile
	_ = json.Unmarshal([]byte(profilesJSON), &list)
	signer, err := invite.LoadKey(d.KeyPath)
	if err != nil {
		return "", fmt.Errorf("нет ключа устройства: %v", err)
	}
	var last error = errors.New("нет серверов из панели")
	tried, gone := 0, 0
	for _, p := range list {
		if p.FromPanel != d.ID || p.Host == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		prof, err := invite.Fetch(ctx, invite.Target{Host: p.Host, Port: p.SSHPort, User: d.ID, Signer: signer,
			HostKey: hostkey.Callback(knownHosts, nil), KnownHosts: knownHosts})
		cancel()
		if err == nil {
			out, err := json.Marshal(prof.Config())
			return string(out), err
		}
		tried++
		if invite.Gone(err) {
			gone++
		}
		last = err
	}
	if tried > 0 && gone == tried {
		// Ни один сервер устройство не знает — удалено в панели: его
		// серверы можно удалить у себя (ТЗ 49). Приложение узнаёт по тексту.
		return "", errors.New(deviceGoneText)
	}
	return "", last
}

const deviceGoneText = "устройство удалено в панели сервера — его серверы можно удалить у себя"

// DeviceGoneText — текст ошибки DeviceFetch «устройство удалено в панели».
func DeviceGoneText() string { return deviceGoneText }

// deviceState — то, что приложение отдаёт в DeviceApply и получает назад.
type deviceState struct {
	Profiles []config.Profile `json:"profiles"`
	Active   string           `json:"active"`
	Device   *config.Device   `json:"device"`
	// Op: "set" (после DeviceJoin: Join — его ответ), "apply" (Profile —
	// ответ DeviceFetch), "hide" (ID, Hidden), "net" (Net), "view",
	// "remove" (ID: сервер из панели, ТЗ 49/51), "restore" (Key: вернуть
	// удалённый или скрытый у себя, ТЗ 51).
	Op      string               `json:"op"`
	Join    json.RawMessage      `json:"join,omitempty"`
	Profile config.DeviceProfile `json:"profile"`
	ID      string               `json:"id,omitempty"`
	Key     string               `json:"key,omitempty"`
	Hidden  bool                 `json:"hidden,omitempty"`
	Net     *config.DeviceNet    `json:"net,omitempty"`
	// KnownHosts — куда записать ключи новых серверов.
	KnownHosts string `json:"knownHosts,omitempty"`
}

// DeviceApply — решение общего кода над профилями телефона. Ответ — JSON
// {"profiles","active","device","changed","added","removed","switched",
// "note","net","meshAllowed"}: приложение сохраняет profiles (новые —
// с ключом устройства), active и device.
func DeviceApply(stateJSON string) (string, error) {
	var st deviceState
	if err := json.Unmarshal([]byte(stateJSON), &st); err != nil {
		return "", fmt.Errorf("состояние не разобрано: %v", err)
	}
	cfg := config.Config{Profiles: st.Profiles, ActiveProfile: st.Active, Device: st.Device}
	activeWas := st.Active
	var res config.DeviceResult
	note := ""
	from := st.Profile.From // откуда профиль: его ключ подтвердило соединение
	switch st.Op {
	case "set":
		var j struct {
			Device     config.Device        `json:"device"`
			Profile    config.DeviceProfile `json:"profile"`
			ProfileErr string               `json:"profileErr"`
		}
		if err := json.Unmarshal(st.Join, &j); err != nil || j.Device.ID == "" {
			return "", errors.New("ответ входа по приглашению не разобран")
		}
		emptyOnly := len(cfg.Profiles) == 1 && cfg.Profiles[0].Host == "" && !cfg.Profiles[0].IsPanel()
		res = cfg.SetDevice(j.Device, j.Profile)
		from = j.Profile.From
		if emptyOnly {
			// Первый запуск: пустой сервер не нужен.
			out := cfg.Profiles[:0:0]
			for _, p := range cfg.Profiles {
				if p.IsPanel() || p.Host != "" {
					out = append(out, p)
				}
			}
			cfg.Profiles = out
		}
		if a, ok := cfg.ProfileByID(cfg.ActiveProfile); !ok || a.Host == "" || emptyOnly {
			for _, p := range cfg.Profiles {
				if p.IsPanel() && !p.Hidden {
					cfg.ActiveProfile = p.ID
					break
				}
			}
		}
		if j.ProfileErr != "" {
			note = j.ProfileErr
		}
	case "apply":
		res = cfg.ApplyDevice(st.Profile)
	case "hide":
		sw, ok := cfg.SetHidden(st.ID, st.Hidden)
		if !ok {
			return "", errors.New("скрыть можно только сервер из панели")
		}
		res.Switched, res.Changed = sw, true
	case "net":
		if st.Net == nil || !cfg.SetDeviceNet(*st.Net) {
			return "", errors.New("устройство не добавлено по приглашению")
		}
		res.Changed = true
	case "view":
		cfg.SyncDevice()
	case "remove":
		if !cfg.RemoveProfile(st.ID) {
			return "", errors.New("такого сервера нет")
		}
		cfg.SyncDevice()
		res.Changed = true
		if cfg.ActiveProfile != activeWas {
			if p, ok := cfg.ProfileByID(cfg.ActiveProfile); ok {
				res.Switched = p.Name
			}
		}
	case "restore":
		name, ok := cfg.RestoreServer(st.Key)
		if !ok {
			return "", errors.New("такого сервера в профиле устройства нет")
		}
		res.Changed = true
		note = "сервер " + name + " снова в списке"
	default:
		return "", fmt.Errorf("неизвестное действие %q", st.Op)
	}
	invite.PinProfile(st.KnownHosts, res.Pins, from)
	added, removed := []string{}, res.Removed
	for _, p := range res.Added {
		added = append(added, p.Name)
	}
	if removed == nil {
		removed = []string{}
	}
	var notes []string
	if len(added) > 0 {
		notes = append(notes, "добавлены серверы: "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		notes = append(notes, "убраны серверы: "+strings.Join(removed, ", "))
	}
	if res.MeshOff {
		notes = append(notes, "сеть устройств выключена на сервере")
	}
	if res.MeshBack {
		notes = append(notes, "сеть устройств снова доступна")
	}
	if res.Switched != "" {
		notes = append(notes, "активный сервер теперь "+res.Switched)
	}
	if note == "" {
		note = strings.Join(notes, "; ")
	}
	if cfg.Profiles == nil {
		cfg.Profiles = []config.Profile{} // ноль серверов — «[]», не null
	}
	out, err := json.Marshal(map[string]any{"profiles": cfg.Profiles, "active": cfg.ActiveProfile, "device": cfg.Device,
		"changed": res.Changed, "added": added, "removed": removed, "switched": res.Switched, "note": note,
		"net": cfg.DeviceNetOf(), "meshAllowed": cfg.DeviceMeshAllowed(), "netMissing": cfg.DeviceNetMissing(),
		"netMissingText": cfg.DeviceNetMissingText(), "removedServers": removedOrEmpty(cfg.RemovedServers())})
	return string(out), err
}

func removedOrEmpty(v []config.DeviceServer) []config.DeviceServer {
	if v == nil {
		return []config.DeviceServer{}
	}
	return v
}

// DeviceExport — устройство для выгрузки всех настроек (config.DeviceForExport):
// без пути к ключу, цепочка — ключами серверов. "" — устройства нет.
func DeviceExport(deviceJSON, profilesJSON string) string {
	cfg := config.Config{}
	if strings.TrimSpace(deviceJSON) == "" {
		return ""
	}
	var d config.Device
	if json.Unmarshal([]byte(deviceJSON), &d) != nil || d.ID == "" {
		return ""
	}
	_ = json.Unmarshal([]byte(profilesJSON), &cfg.Profiles)
	cfg.Device = &d
	out, _ := json.Marshal(cfg.DeviceForExport())
	return string(out)
}

// DeviceImport — устройство из выгрузки на профили, уже заведённые из неё
// (config.DeviceFromExport). "" — устройства в выгрузке нет.
func DeviceImport(deviceJSON, profilesJSON string) string {
	if strings.TrimSpace(deviceJSON) == "" {
		return ""
	}
	var d config.Device
	if json.Unmarshal([]byte(deviceJSON), &d) != nil || d.ID == "" {
		return ""
	}
	var list []config.Profile
	_ = json.Unmarshal([]byte(profilesJSON), &list)
	nd := config.DeviceFromExport(&d, list)
	if nd == nil {
		return ""
	}
	out, _ := json.Marshal(nd)
	return string(out)
}

// RemovedMarks — отметки «удалён человеком» для профиля (JSON профиля ПК,
// как в SetProfiles): телефон дописывает их в свой список «уже добавляли»,
// и сеть серверов удалённый сервер не возвращает (ТЗ 50). Ответ — JSON-массив.
func RemovedMarks(profileJSON string) string {
	var p config.Profile
	_ = json.Unmarshal([]byte(profileJSON), &p)
	out, _ := json.Marshal(config.RemovedMarks(p))
	if string(out) == "null" {
		return "[]"
	}
	return string(out)
}
