package mobile

// Обновление сети устройств на телефоне (ТЗ 40). Поставить себя молча
// Android не даёт, поэтому по версии сети ядро только скачивает APK этой
// версии из релиза на GitHub (со сверкой SHA256SUMS.txt) в папку
// приложения и говорит приложению: оно показывает уведомление «Обновление
// vX готово — установить», которое открывает системный установщик.
//
// Галочку «Автообновление» хранит приложение (Settings) и передаёт сюда
// SetAutoUpdate; панель сервера может её поменять — тогда UpdateListener
// получает OnAutoUpdate и приложение сохраняет новое значение.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"sshtunnel/internal/mesh"
	"sshtunnel/internal/updater"
)

// UpdateListener — приложению: APK скачан (path, version) или панель
// поменяла автообновление. Зовётся в своей горутине.
type UpdateListener interface {
	OnUpdateReady(path, version string)
	OnAutoUpdate(on bool, t int64)
}

type netUpd struct {
	mu       sync.Mutex
	on       bool
	t        int64
	dir      string
	listener UpdateListener
	auto     *updater.Auto
}

// netUpdateTimeout — сколько ждать ответа панели на «обновить сеть».
const netUpdateTimeout = time.Minute

var phoneUpd = &netUpd{on: true}

// phoneNoAuto — на телефоне автообновления нет: тихо поставить APK Android
// не даёт, а скачанный в папку приложения файл человеку не достать.
// Приложение при запуске само спрашивает «Вышла vX — обновить?» и ставит
// старым путём (скачать с прогрессом → установщик). Серверам телефон
// сообщает «автообновление выключено»; «Обновить» из панели (force) по-
// прежнему скачивает и показывает уведомление «установить».
func phoneNoAuto() (bool, int64) { return false, 0 }

func (u *netUpd) setting() (bool, int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.on, u.t
}

func (u *netUpd) get() *updater.Auto {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.auto == nil {
		u.auto = updater.NewAuto(phoneNoAuto, u.download, nil)
	}
	return u.auto
}

// download — APK версии ver в папку обновлений приложения.
func (u *netUpd) download(ctx context.Context, ver string) (string, error) {
	u.mu.Lock()
	dir, l := u.dir, u.listener
	u.mu.Unlock()
	if dir == "" {
		return "", errors.New("приложение не задало папку для обновлений")
	}
	if updater.Compare(updater.Version, ver) != -1 {
		return "", errors.New("не новее установленной — вниз не обновляемся")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "ssh_tunnel-"+ver+".apk")
	if err := updater.DownloadVersionTo(ctx, ver, path); err != nil {
		return "", err
	}
	if l != nil {
		go l.OnUpdateReady(path, ver)
	}
	return path, nil
}

// SetUpdateListener — nil снимает. dir — куда класть APK (cacheDir/updates,
// его отдаёт FileProvider).
func (t *Tunnel) SetUpdateListener(l UpdateListener, dir string) {
	phoneUpd.mu.Lock()
	phoneUpd.listener, phoneUpd.dir = l, dir
	phoneUpd.mu.Unlock()
}

// SetAutoUpdate — галочка «Автообновление» и когда её меняли (unix).
func (t *Tunnel) SetAutoUpdate(on bool, changed int64) {
	phoneUpd.mu.Lock()
	phoneUpd.on, phoneUpd.t = on, changed
	phoneUpd.mu.Unlock()
	if c := t.meshHost.Client(); c != nil {
		c.ReportUpdate()
	}
}

// NetUpdateState — GET /api/update/net (JSON updater.AutoState).
func (t *Tunnel) NetUpdateState() string {
	b, _ := json.Marshal(phoneUpd.get().State())
	return string(b)
}

// RequestNetUpdate — «Обновить сеть до ver» (только «Моё устройство»); этот
// телефон тоже скачает обновление.
func (t *Tunnel) RequestNetUpdate(ver string) error {
	c := t.meshHost.Client()
	if c == nil {
		return errors.New("сеть устройств выключена")
	}
	ctx, cancel := context.WithTimeout(context.Background(), netUpdateTimeout)
	defer cancel()
	if err := c.RequestNetUpdate(ctx, ver); err != nil {
		return err
	}
	if updater.Compare(updater.Version, ver) == -1 {
		phoneUpd.get().UpdateNow(ver)
	}
	return nil
}

// UpdateNow — скачать обновление до версии сети ver (строка-предложение).
func (t *Tunnel) UpdateNow(ver string) error { return phoneUpd.get().UpdateNow(ver) }

// meshUpdateHooks — обновление сети в настройках клиента сети устройств.
func (t *Tunnel) meshUpdateHooks(mc *mesh.Config) {
	a := phoneUpd.get()
	mc.Build = "vpn"
	mc.AutoUpdate = phoneNoAuto
	mc.UpdateStatus = a.StatusText
	mc.OnNetVersion = func(v mesh.NetVersion) { a.Net(v.Version, v.Update, v.Force, v.Mine) }
	mc.OnAutoUpdate = func(on bool, changed int64) {
		phoneUpd.mu.Lock()
		newer := changed > phoneUpd.t
		if newer {
			phoneUpd.on, phoneUpd.t = on, changed
		}
		l := phoneUpd.listener
		phoneUpd.mu.Unlock()
		if newer && l != nil {
			l.OnAutoUpdate(on, changed)
		}
	}
}

// NetDecision — что делать по «Проверить обновления» с версией сети (см.
// updater.NetDecision): "self", "network" или "".
func NetDecision(installed, latest, netVer string, mine bool) string {
	return updater.NetDecision(installed, latest, netVer, mine)
}
