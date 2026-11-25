package mobile

// Скриншот с телефона — сразу в общий буфер (ТЗ 52). Kotlin замечает новые
// картинки в MediaStore (ContentObserver) и спрашивает здесь, отправлять
// ли: решение — одно место, его проверяют тесты.
//
// Отправляется только:
//   - картинка из папки скриншотов (Pictures/Screenshots, DCIM/Screenshots и
//     то, как её называют производители);
//   - добавленная после включения галочки и после запуска программы, и не
//     раньше минуты назад — включение не рассылает старые скриншоты, а
//     переиндексация галереи не рассылает их снова;
//   - один раз (MediaStore сообщает об одной картинке несколько раз: создана,
//     дописана, снят флаг «пишется»).

import (
	"strings"
	"sync"
	"time"

	"sshtunnel/internal/clipshare"
)

// ScreenshotMaxBytes — больше этого скриншот уменьшается перед отправкой
// (Kotlin): 8 МБ хватает любому экрану в PNG, а общий буфер держит 50 МБ.
const ScreenshotMaxBytes = 8 << 20

// shotFresh — насколько старый (по дате добавления) скриншот ещё «новый».
const shotFresh = time.Minute

// shotSeenMax — сколько id помнить (дальше — забыть самые старые разом).
const shotSeenMax = 256

// screenshotDirs — имена папок скриншотов (в нижнем регистре): Android,
// Samsung, Xiaomi, Huawei/Honor, OnePlus, Tecno/HiOS, старые прошивки.
var screenshotDirs = map[string]bool{
	"screenshots": true, "screenshot": true, "screen_shots": true, "screenshots_tmp": false,
	"screencapture": true, "screencaptures": true, "screen captures": true, "скриншоты": true,
}

type shotFilter struct {
	mu      sync.Mutex
	started time.Time
	seen    map[int64]bool
	order   []int64
}

var shots = &shotFilter{started: time.Now()}

// isScreenshotPath — путь (RELATIVE_PATH или DATA) и имя файла похожи на
// скриншот.
func isScreenshotPath(path, name string) bool {
	p := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	for _, seg := range strings.Split(p, "/") {
		if screenshotDirs[strings.TrimSpace(seg)] {
			return true
		}
	}
	// Некоторые прошивки кладут скриншоты прямо в Pictures или DCIM — тогда
	// узнаём по имени файла.
	n := strings.ToLower(name)
	if strings.HasPrefix(n, "screenshot") || strings.HasPrefix(n, "скриншот") {
		return strings.Contains(p, "pictures") || strings.Contains(p, "dcim")
	}
	return false
}

// want — отправлять ли картинку id, добавленную в added. since — когда
// включили галочку.
func (f *shotFilter) want(id int64, path, name string, added, since, now time.Time) bool {
	if !isScreenshotPath(path, name) {
		return false
	}
	// Дата добавления в MediaStore — в секундах: секунда запаса.
	from := f.started
	if since.After(from) {
		from = since
	}
	if added.Before(from.Add(-time.Second)) || now.Sub(added) > shotFresh {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen == nil {
		f.seen = map[int64]bool{}
	}
	if f.seen[id] {
		return false
	}
	if len(f.order) >= shotSeenMax {
		for _, old := range f.order[:shotSeenMax/2] {
			delete(f.seen, old)
		}
		f.order = append([]int64(nil), f.order[shotSeenMax/2:]...)
	}
	f.seen[id] = true
	f.order = append(f.order, id)
	return true
}

// ScreenshotsOn — галочка «Скриншоты сразу в общий буфер» включена и буфер
// работает: Kotlin только тогда смотрит в MediaStore.
func ScreenshotsOn() bool { return clipshare.CurrentSettings().Screenshots }

// ScreenshotWanted — отправлять ли новую картинку MediaStore: id — _ID,
// path — RELATIVE_PATH (Android 10+) или DATA, name — DISPLAY_NAME,
// addedSec — DATE_ADDED (секунды).
func ScreenshotWanted(id int64, path, name string, addedSec int64) bool {
	s := clipshare.CurrentSettings()
	if !s.Screenshots {
		return false
	}
	return shots.want(id, path, name, time.Unix(addedSec, 0), time.Unix(s.ScreenshotsSince, 0), time.Now())
}

// ScreenshotLimit — ScreenshotMaxBytes для Kotlin.
func ScreenshotLimit() int64 { return ScreenshotMaxBytes }
