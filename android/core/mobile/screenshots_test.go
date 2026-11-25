package mobile

import (
	"testing"
	"time"
)

// ТЗ 52: какие картинки MediaStore считаются новым скриншотом.
func TestСкриншотПапка(t *testing.T) {
	yes := [][2]string{
		{"Pictures/Screenshots/", "Screenshot_20261125-101010.png"},
		{"DCIM/Screenshots/", "Screenshot_2026-11-25-10-10-10-123_com.vk.png"},
		{"/storage/emulated/0/Pictures/Screenshots/a.png", "a.png"},
		{"Pictures/ScreenCapture/", "x.png"},
		{"Pictures/Скриншоты/", "x.jpg"},
		{"Pictures/", "Screenshot_1.png"}, // прошивка без отдельной папки
		{"DCIM/", "Скриншот 1.png"},
	}
	for _, c := range yes {
		if !isScreenshotPath(c[0], c[1]) {
			t.Errorf("%q %q — не скриншот", c[0], c[1])
		}
	}
	no := [][2]string{
		{"DCIM/Camera/", "IMG_20261125.jpg"},
		{"Pictures/Telegram/", "photo.jpg"},
		{"Download/", "Screenshot_from_chat.png"}, // скачанный «скриншот» — не наш
		{"Pictures/Screenshots_tmp/", "x.png"},
		{"", ""},
	}
	for _, c := range no {
		if isScreenshotPath(c[0], c[1]) {
			t.Errorf("%q %q — принят за скриншот", c[0], c[1])
		}
	}
}

// Время: не раньше запуска и включения галочки, не старше минуты; каждый
// скриншот — один раз.
func TestСкриншотВремяИПовторы(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	f := &shotFilter{started: now.Add(-10 * time.Minute)}
	since := now.Add(-5 * time.Minute) // галочку включили 5 минут назад
	dir, name := "Pictures/Screenshots/", "s.png"

	if f.want(1, dir, name, now.Add(-6*time.Minute), since, now) {
		t.Error("скриншот до включения галочки отправлен")
	}
	if f.want(2, dir, name, now.Add(-2*time.Minute), since, now) {
		t.Error("скриншот старше минуты отправлен (переиндексация галереи)")
	}
	if !f.want(3, dir, name, now.Add(-2*time.Second), since, now) {
		t.Error("новый скриншот не отправлен")
	}
	if f.want(3, dir, name, now.Add(-2*time.Second), since, now) {
		t.Error("тот же скриншот отправлен второй раз")
	}
	if f.want(4, "DCIM/Camera/", "IMG.jpg", now, since, now) {
		t.Error("фото с камеры отправлено")
	}

	// Первый запуск: скриншоты, сделанные до запуска программы, не уходят,
	// даже если галочка включена давно.
	g := &shotFilter{started: now}
	if g.want(5, dir, name, now.Add(-20*time.Second), now.Add(-time.Hour), now) {
		t.Error("старый скриншот ушёл при первом запуске")
	}
	// Секунда запаса: DATE_ADDED в секундах.
	if !g.want(6, dir, name, now.Add(-500*time.Millisecond).Truncate(time.Second), now.Add(-time.Hour), now) {
		t.Error("скриншот той же секунды, что запуск, не ушёл")
	}

	// Память id ограничена и не теряет свежие.
	h := &shotFilter{started: now.Add(-time.Hour)}
	for i := int64(100); i < 100+shotSeenMax*3; i++ {
		if !h.want(i, dir, name, now, time.Time{}, now) {
			t.Fatalf("id %d не прошёл", i)
		}
	}
	if len(h.seen) > shotSeenMax {
		t.Fatalf("помнит %d id", len(h.seen))
	}
	if h.want(100+shotSeenMax*3-1, dir, name, now, time.Time{}, now) {
		t.Error("свежий id забыт")
	}
}

// Галочка выключена (или буфер не работает) — ничего не уходит.
func TestСкриншотыВыключеныПоУмолчанию(t *testing.T) {
	if ScreenshotsOn() || ScreenshotWanted(1, "Pictures/Screenshots/", "s.png", time.Now().Unix()) {
		t.Fatal("скриншоты уходят без галочки")
	}
}
