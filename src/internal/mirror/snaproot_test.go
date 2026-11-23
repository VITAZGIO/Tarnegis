package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Общая папка снимков устройства: <папка>/<имя папки>/<дата>.zip, старые
// снимки переезжают туда и обратно, настройка переживает перезапуск, а
// внутри раздаваемой папки её задать нельзя.
func TestSnapRootFolderPerShare(t *testing.T) {
	e := newEnv(t)
	e.write(e.root, "x.md", "v0")
	clock := time.Date(2026, 11, 23, 21, 45, 0, 0, time.Local)
	e.hub.now = func() time.Time { return clock }

	old, err := e.hub.SnapNow(e.fid, SnapManual)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(old.File, "Тест ") {
		t.Fatalf("в данных программы снимок назван %q, ждали «Тест <дата>»", old.File)
	}

	root := filepath.Join(t.TempDir(), "ssh_tunnel", "backups")
	if _, err := e.hub.SetSnapRoot(root); err != nil {
		t.Fatal(err)
	}
	sl, err := e.hub.Snapshots(e.fid)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "Тест")
	if sl.Dir != want {
		t.Fatalf("снимки в %q, ждали %q", sl.Dir, want)
	}
	if len(sl.Snapshots) != 1 {
		t.Fatalf("после переезда снимков %d, ждали 1", len(sl.Snapshots))
	}
	if _, err := os.Stat(filepath.Join(want, sl.Snapshots[0].File)); err != nil {
		t.Fatalf("старый снимок не переехал: %v", err)
	}
	if _, err := os.Stat(snapPath(t, e.hub, e.fid, old.File)); !os.IsNotExist(err) {
		t.Fatal("в данных программы остался старый архив")
	}

	clock = clock.Add(time.Minute)
	s, err := e.hub.SnapNow(e.fid, SnapManual)
	if err != nil {
		t.Fatal(err)
	}
	if s.File != "2026-11-23 21-46.zip" {
		t.Fatalf("в своей подпапке снимок назван %q, ждали дату", s.File)
	}
	if _, err := os.Stat(filepath.Join(want, s.File)); err != nil {
		t.Fatal(err)
	}
	fh, got, err := e.hub.OpenSnapshot(e.fid, s.File)
	if err != nil {
		t.Fatal(err)
	}
	fh.Close()
	if n := snapDownloadName(got); n != "Тест 2026-11-23 21-46.zip" {
		t.Fatalf("скачанный снимок назван %q", n)
	}
	if _, err := e.hub.RestoreSnapshot(e.fid, "тест", s.File); err != nil {
		t.Fatalf("восстановление из папки снимков: %v", err)
	}

	// Перезапуск: папка снимков та же.
	e.hub.Close()
	e.startHub()
	if e.hub.SnapRoot() != root {
		t.Fatalf("после перезапуска папка снимков %q", e.hub.SnapRoot())
	}

	// Внутри раздаваемой папки — нельзя.
	if _, err := e.hub.SetSnapRoot(filepath.Join(e.root, "backups")); err == nil {
		t.Fatal("папку снимков разрешили внутри раздаваемой папки")
	}
	if _, err := e.hub.SetSnapRoot("backups"); err == nil {
		t.Fatal("разрешили относительный путь")
	}

	// Обратно в данные программы — все снимки с собой.
	if _, err := e.hub.SetSnapRoot(""); err != nil {
		t.Fatal(err)
	}
	sl, err = e.hub.Snapshots(e.fid)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := e.hub.folder(e.fid)
	if sl.Dir != f.snapDir() {
		t.Fatalf("после возврата снимки в %q", sl.Dir)
	}
	for _, x := range sl.Snapshots {
		if _, err := os.Stat(filepath.Join(sl.Dir, x.File)); err != nil {
			t.Fatalf("снимок %s не вернулся: %v", x.File, err)
		}
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatal("пустая подпапка в папке снимков осталась")
	}
}

// Подсказка: «backups» рядом с папкой, где лежат раздаваемые папки.
func TestSnapRootSuggest(t *testing.T) {
	e := newEnv(t)
	got := e.hub.SuggestSnapRoot()
	want := filepath.Join(filepath.Dir(filepath.Dir(e.root)), "backups")
	if got != want {
		t.Fatalf("подсказка %q, ждали %q", got, want)
	}
}

// «Раз в 3 дня» и «раз в неделю»: снимок не раньше, чем через N дней после
// прошлого по расписанию; пропущенное догоняется одним снимком.
func TestSnapEvery(t *testing.T) {
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.Local) }
	last := at(20, 3).Unix() // прошлый снимок 20.11 в 03:00
	cases := []struct {
		every int
		now   time.Time
		due   bool
	}{
		{1, at(20, 12), false}, {1, at(21, 3), true},
		{3, at(22, 12), false}, {3, at(23, 2), false}, {3, at(23, 3), true}, {3, at(30, 12), true},
		{7, at(26, 12), false}, {7, at(27, 3), true},
	}
	for _, c := range cases {
		cfg := SnapConfig{Daily: true, Every: c.every, Time: "03:00", Keep: 3}
		if got := snapDue(c.now, cfg, last); got != c.due {
			t.Errorf("раз в %d дн., сейчас %s: пора=%v, ждали %v", c.every, c.now.Format("02.01 15:04"), got, c.due)
		}
	}
	if snapDue(at(30, 12), SnapConfig{Daily: false, Every: 1, Time: "03:00"}, last) {
		t.Error("выключенное расписание сработало")
	}
	e := newEnv(t)
	if _, err := e.hub.SetSnapConfig(e.fid, SnapConfig{Daily: true, Every: 2}); err == nil {
		t.Error("приняли «раз в 2 дня»")
	}
	c, err := e.hub.SetSnapConfig(e.fid, SnapConfig{Daily: true, Every: 7})
	if err != nil || c.Every != 7 {
		t.Fatalf("раз в неделю не сохранилось: %+v, %v", c, err)
	}
}
