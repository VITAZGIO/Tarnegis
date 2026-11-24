package updater

// Обновление сети устройств (ТЗ 40) — сторона устройства.
//
// Сеть передаёт только номер версии («обнови себя до vX»). Файл каждое
// устройство берёт само — из релиза vX проекта на GitHub — и сверяет с
// SHA256SUMS.txt того же релиза, как Prepare. Вниз не обновляемся; черновики
// и предрелизы не берём. Автоматически — не чаще раза в час на версию:
// сломанный релиз не должен гонять устройство по кругу.
//
// Auto решает, что делать с версией сети, которую сказали серверы:
//   - устройство старше и автообновление включено — обновиться самому;
//   - автообновление выключено — показать строку «Есть новая версия сети
//     vX — обновить?» (Offer);
//   - в панели нажали «Обновить» у этого устройства (force) — обновиться,
//     даже если автообновление выключено.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// releasesTagURL — релиз по номеру (GitHub: /releases/tags/<tag>).
var releasesTagURL = "https://api.github.com/repos/VITAZGIO/ssh_tunnel/releases/tags/"

// AutoRetry — не чаще раза в столько пробовать одну и ту же версию сам.
const AutoRetry = time.Hour

// releaseFull — релиз с отметками «черновик» и «предрелиз».
type releaseFull struct {
	Release
	Draft      bool `json:"draft"`
	Prerelease bool `json:"prerelease"`
}

// ReleaseByVersion — официальный релиз ver («1.10.7» или «v1.10.7») с
// GitHub. Нет такого, черновик или предрелиз — ошибка.
func ReleaseByVersion(ctx context.Context, ver string) (Release, error) {
	ver = strings.TrimPrefix(strings.TrimSpace(ver), "v")
	if _, ok := parse(ver); !ok || strings.ContainsAny(ver, "/?#% ") {
		return Release{}, errors.New("неверный номер версии")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesTagURL+"v"+ver, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ssh_tunnel/"+Version+" ("+runtime.GOOS+")")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("не удалось связаться с GitHub: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0":
		return Release{}, ErrRateLimited
	case resp.StatusCode == http.StatusNotFound:
		return Release{}, fmt.Errorf("у проекта нет релиза v%s", ver)
	case resp.StatusCode != http.StatusOK:
		return Release{}, fmt.Errorf("GitHub ответил %s", resp.Status)
	}
	var rel releaseFull
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("не разобрал ответ GitHub: %w", err)
	}
	if rel.Draft || rel.Prerelease {
		return Release{}, fmt.Errorf("v%s — не официальный релиз (черновик или предрелиз)", ver)
	}
	if Compare(rel.Tag, ver) != 0 {
		return Release{}, fmt.Errorf("GitHub вернул не тот релиз (%s вместо v%s)", rel.Tag, ver)
	}
	return rel.Release, nil
}

// PrepareVersion — как Prepare, но релиз ver, а не последний: скачать файл
// этой системы рядом с программой, сверить с SHA256SUMS.txt. Только вверх.
func PrepareVersion(ctx context.Context, ver string) (newFile, exe string, err error) {
	if c := Compare(Version, ver); c == unknownVersion {
		return "", "", errors.New("версия этой сборки неизвестна (собрана не из релиза) — обнови вручную")
	} else if c >= 0 {
		return "", "", fmt.Errorf("уже стоит %s — вниз не обновляемся", strings.TrimPrefix(Version, "v"))
	}
	rel, err := ReleaseByVersion(ctx, ver)
	if err != nil {
		return "", "", err
	}
	return prepareRelease(ctx, rel)
}

// DownloadVersion — файл релиза ver в «Загрузки» со сверкой контрольной
// суммы: там, где поставить себя программа не может (Android, VPN-окно без
// службы). Возвращает путь.
func DownloadVersion(ctx context.Context, ver string) (string, error) {
	if c := Compare(Version, ver); c == unknownVersion || c >= 0 {
		return "", fmt.Errorf("не новее установленной (%s)", strings.TrimPrefix(Version, "v"))
	}
	rel, err := ReleaseByVersion(ctx, ver)
	if err != nil {
		return "", err
	}
	bin, sums, err := releaseFiles(rel)
	if err != nil {
		return "", err
	}
	dir := DownloadsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return fetchChecked(ctx, bin, sums, freeName(dir, bin.Name))
}

// DownloadVersionTo — то же, но в заданный файл (телефон: в свою папку).
func DownloadVersionTo(ctx context.Context, ver, dst string) error {
	rel, err := ReleaseByVersion(ctx, ver)
	if err != nil {
		return err
	}
	bin, sums, err := releaseFiles(rel)
	if err != nil {
		return err
	}
	_, err = fetchChecked(ctx, bin, sums, dst)
	return err
}

func releaseFiles(rel Release) (bin, sums Asset, err error) {
	bin, ok := AssetFor(rel, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return bin, sums, errors.New("у релиза нет файла для этой системы")
	}
	for _, a := range rel.Assets {
		if a.Name == "SHA256SUMS.txt" {
			sums = a
		}
	}
	if sums.URL == "" {
		return bin, sums, errors.New("у релиза нет контрольных сумм — обнови вручную")
	}
	return bin, sums, nil
}

// fetchChecked качает bin в dst и сверяет с суммой из sums; не совпало —
// файла нет.
func fetchChecked(ctx context.Context, bin, sums Asset, dst string) (string, error) {
	got, err := fetch(ctx, bin.URL, dst)
	if err == nil {
		var want string
		want, err = sumOf(ctx, sums.URL, bin.Name)
		if err == nil && !strings.EqualFold(want, got) {
			err = errors.New("контрольная сумма не совпала — файл повреждён или подменён")
		}
	}
	if err != nil {
		os.Remove(dst)
		return "", err
	}
	return dst, nil
}

// NetDecision — что делать по «Проверить обновления», зная версию сети:
//   - "self" — на серверах уже версия новее этой (устройство её пропустило):
//     обновиться самому, не спрашивая;
//   - "network" — на GitHub новее, чем у серверов, и у устройства роль «Моё
//     устройство»: спросить «Обновить сеть до vX?»;
//   - "" — как раньше (только это устройство).
func NetDecision(installed, latest, netVer string, mine bool) string {
	if netVer == "" {
		return ""
	}
	if Compare(installed, netVer) == -1 && Compare(netVer, latest) >= 0 {
		return "self"
	}
	if mine && Compare(netVer, latest) == -1 && Compare(installed, latest) != unknownVersion {
		return "network"
	}
	if Compare(installed, netVer) == -1 {
		return "self" // GitHub новее сети, но роли нет: хотя бы до версии сети
	}
	return ""
}

// AutoState — автообновление для окна (GET /api/update/net).
type AutoState struct {
	// Enabled — «Автообновление» в настройках; EnabledT — когда меняли.
	Enabled  bool  `json:"enabled"`
	EnabledT int64 `json:"enabledT,omitempty"`
	// Installed — эта версия; NetVersion — версия сети по серверам; Mine —
	// у устройства роль «Моё устройство».
	Installed  string `json:"installed"`
	NetVersion string `json:"netVersion,omitempty"`
	Mine       bool   `json:"mine,omitempty"`
	// Offer — есть версия сети новее, а автообновление выключено: строка
	// «Есть новая версия сети vX — обновить?».
	Offer string `json:"offer,omitempty"`
	// Stage — "" / download / install / ready (скачано, поставить может
	// только человек) / error; Target — до какой версии; Path — где лежит
	// скачанный файл (ready).
	Stage  string `json:"stage,omitempty"`
	Target string `json:"target,omitempty"`
	Error  string `json:"error,omitempty"`
	Path   string `json:"path,omitempty"`
	// CanInstall — эта сборка ставит себя сама.
	CanInstall bool `json:"canInstall"`
}

// AutoRun обновляет программу до ver: "" и nil — поставлено (программа
// перезапускается); путь — файл скачан, поставить может только человек.
type AutoRun func(ctx context.Context, ver string) (path string, err error)

// Auto — автообновление по версии сети.
type Auto struct {
	mu        sync.Mutex
	installed string
	enabled   func() (bool, int64)
	run       AutoRun
	onChange  func()
	now       func() time.Time
	tried     map[string]time.Time
	st        AutoState
	busy      bool
}

// NewAuto: enabled — галочка «Автообновление» и когда её меняли; run —
// как обновиться (DefaultRun); onChange — состояние поменялось (окну и
// серверам). nil-функции — ничего.
func NewAuto(enabled func() (bool, int64), run AutoRun, onChange func()) *Auto {
	if enabled == nil {
		enabled = func() (bool, int64) { return true, 0 }
	}
	if onChange == nil {
		onChange = func() {}
	}
	return &Auto{installed: Version, enabled: enabled, run: run, onChange: onChange, now: time.Now,
		tried: map[string]time.Time{}}
}

// State — для окна.
func (a *Auto) State() AutoState {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.st
	st.Installed = strings.TrimPrefix(a.installed, "v")
	st.Enabled, st.EnabledT = a.enabled()
	st.CanInstall = CanInstall()
	if st.Enabled {
		st.Offer = ""
	}
	return st
}

// StatusText — как прошло последнее обновление, коротко (для панели).
func (a *Auto) StatusText() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch a.st.Stage {
	case "download", "install":
		return "обновляется до " + a.st.Target
	case "ready":
		return "скачано " + a.st.Target + " — поставить вручную"
	case "error":
		return "ошибка " + a.st.Target + ": " + a.st.Error
	}
	return ""
}

// Net — серверы сказали версию сети: update — просят обновиться сейчас,
// force — это нажали в панели (автообновление не смотрится).
func (a *Auto) Net(ver string, update, force, mine bool) {
	ver = strings.TrimPrefix(strings.TrimSpace(ver), "v")
	a.mu.Lock()
	a.st.NetVersion, a.st.Mine = ver, mine
	behind := Compare(a.installed, ver) == -1
	a.st.Offer = ""
	on, _ := a.enabled()
	a.mu.Unlock()
	switch {
	case !behind:
	case force:
		a.start(ver, true)
		return
	case on:
		a.start(ver, false)
		return
	default:
		a.mu.Lock()
		a.st.Offer = ver
		a.mu.Unlock()
	}
	a.onChange()
}

// UpdateNow — человек согласился обновиться до ver (строка-предложение,
// «Проверить обновления»): без часового ограничения.
func (a *Auto) UpdateNow(ver string) error {
	ver = strings.TrimPrefix(strings.TrimSpace(ver), "v")
	if Compare(a.installed, ver) != -1 {
		return fmt.Errorf("уже стоит %s — вниз не обновляемся", strings.TrimPrefix(a.installed, "v"))
	}
	if !a.start(ver, true) {
		return errors.New("обновление уже идёт")
	}
	return nil
}

// start — запустить обновление до ver; explicit — нажал человек (часового
// ограничения нет). false — уже идёт или рано повторять.
func (a *Auto) start(ver string, explicit bool) bool {
	a.mu.Lock()
	if a.busy || a.run == nil {
		a.mu.Unlock()
		return false
	}
	if t, ok := a.tried[ver]; ok && !explicit && a.now().Sub(t) < AutoRetry {
		a.mu.Unlock()
		return false
	}
	a.tried[ver] = a.now()
	a.busy = true
	a.st.Offer = ""
	a.st.Stage, a.st.Target, a.st.Error, a.st.Path = "download", ver, "", ""
	a.mu.Unlock()
	a.onChange()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		path, err := a.run(ctx, ver)
		a.mu.Lock()
		a.busy = false
		switch {
		case err != nil:
			a.st.Stage, a.st.Error = "error", err.Error()
		case path != "":
			a.st.Stage, a.st.Path = "ready", path
		default:
			a.st.Stage = "install" // программа сейчас перезапустится
		}
		a.mu.Unlock()
		a.onChange()
	}()
	return true
}

// DefaultRun — обновление на компьютере: поставить себя (Install) там, где
// умеет; иначе скачать в «Загрузки» и сказать человеку. stop — остановить
// программу перед перезапуском.
func DefaultRun(stop func()) AutoRun {
	return func(ctx context.Context, ver string) (string, error) {
		if !CanInstall() {
			return DownloadVersion(ctx, ver)
		}
		newFile, exe, err := PrepareVersion(ctx, ver)
		if err != nil {
			return "", err
		}
		if err := Install(newFile, exe, stop); err != nil {
			os.Remove(newFile)
			return "", err
		}
		return "", nil
	}
}
