package app

// Обновление сети устройств (ТЗ 40) — что делает программа на компьютере.
// Серверы говорят версию сети (mesh.Config.OnNetVersion), решает
// updater.Auto: старше и автообновление включено — обновиться самому
// (DefaultRun: файл из релиза на GitHub, сверка SHA256SUMS.txt, Install и
// перезапуск), выключено — предложить строкой в окне. Галочка
// «Автообновление» — в config (AutoUpdateOff / AutoUpdateT), её может
// поменять и панель сервера: побеждает новая правка.

import (
	"context"
	"errors"
	"time"

	"sshtunnel/internal/mesh"
	"sshtunnel/internal/updater"
)

// AutoUpdate — обновление по версии сети (для webui).
func (a *App) AutoUpdate() *updater.Auto { return a.upd }

// autoUpdateSetting — галочка «Автообновление» и когда её меняли.
func (a *App) autoUpdateSetting() (bool, int64) {
	cfg := a.Config()
	return !cfg.AutoUpdateOff, cfg.AutoUpdateT
}

// onAutoChanged — состояние обновления поменялось: рассказать серверам.
func (a *App) onAutoChanged() {
	if c := a.meshHost.Client(); c != nil {
		c.ReportUpdate()
	}
}

// meshUpdateHooks — обновление сети в настройках клиента сети устройств.
func (a *App) meshUpdateHooks(mc *mesh.Config) {
	mc.Build = "proxy"
	if updater.VPN {
		mc.Build = "vpn"
	}
	mc.AutoUpdate = a.autoUpdateSetting
	mc.UpdateStatus = a.upd.StatusText
	mc.OnNetVersion = a.onNetVersion
	mc.OnAutoUpdate = a.onPanelAutoUpdate
}

// onNetVersion — серверы сказали версию сети.
func (a *App) onNetVersion(v mesh.NetVersion) {
	before := a.upd.State()
	a.upd.Net(v.Version, v.Update, v.Force, v.Mine)
	if before.NetVersion != v.Version && updater.Compare(updater.Version, v.Version) == -1 {
		a.Bus.Infof("Сеть устройств: в сети версия %s (сказал %s)", v.Version, v.Server)
	}
}

// onPanelAutoUpdate — панель сервера поменяла автообновление этого устройства.
func (a *App) onPanelAutoUpdate(on bool, t int64) {
	if changed, _ := a.SetAutoUpdate(on, t); changed {
		state := "выключила"
		if on {
			state = "включила"
		}
		a.Bus.Infof("Панель сервера %s автообновление этого устройства", state)
	}
}

// SetAutoUpdate — галочка «Автообновление» (t — когда; 0 — сейчас). Правка
// старше уже сохранённой не применяется. true — поменялось.
func (a *App) SetAutoUpdate(on bool, t int64) (bool, error) {
	a.exitMu.Lock()
	defer a.exitMu.Unlock()
	cfg := a.Config()
	if t == 0 {
		t = time.Now().Unix()
		if t <= cfg.AutoUpdateT {
			t = cfg.AutoUpdateT + 1
		}
	}
	if t <= cfg.AutoUpdateT {
		return false, nil
	}
	cfg.AutoUpdateOff, cfg.AutoUpdateT = !on, t
	if _, err := a.SetConfig(cfg); err != nil {
		return false, err
	}
	a.onAutoChanged()
	if st := a.upd.State(); on && st.Offer != "" {
		// Включили, а версия сети уже новее — обновиться сейчас.
		a.upd.Net(st.Offer, true, false, st.Mine)
	}
	return true, nil
}

// RequestNetUpdate — «Обновить сеть до ver»: попросить серверы (там, где у
// устройства роль «Моё устройство») и обновиться самому.
func (a *App) RequestNetUpdate(ctx context.Context, ver string) error {
	c := a.meshHost.Client()
	if c == nil {
		return errors.New("сеть устройств выключена — обновить можно только это устройство")
	}
	if err := c.RequestNetUpdate(ctx, ver); err != nil {
		return err
	}
	if updater.Compare(updater.Version, ver) == -1 {
		if err := a.upd.UpdateNow(ver); err != nil {
			a.Bus.Warnf("Обновление: %v", err)
		}
	}
	return nil
}
