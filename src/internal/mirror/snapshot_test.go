package mirror

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Тесты ТЗ 36: снимки папки и подключение к непустой папке.

// zipTree — файлы архива снимка (без snapshot.json) и его meta.
func zipTree(t *testing.T, p string) (map[string]string, snapMeta) {
	t.Helper()
	zr, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	meta, _, err := readSnapMeta(&zr.Reader)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, zf := range zr.File {
		if zf.Name == strings.TrimPrefix(zr.Comment, snapComment) {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[zf.Name] = string(b)
	}
	return out, meta
}

// commitDirect — устройство dev записывает rel на сервер (загрузка и commit).
func commitDirect(t *testing.T, h *Hub, fid, dev, rel, content string, base int64) CommitReply {
	t.Helper()
	hs := sum(content)
	if _, err := h.WritePart(fid, dev, rel, hs, 0, strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	rep, err := h.Commit(fid, dev, rel, hs, base)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func snapPath(t *testing.T, h *Hub, fid, file string) string {
	t.Helper()
	f, err := h.folder(fid)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(f.snapDir(), file)
}

// Снимок, пока другое устройство пишет: архив — ровно состояние папки на
// одном номере изменения Hub (ни одного файла «из будущего» или «из
// прошлого»), и записи при этом не падают.
func TestSnapConsistentWhileWriting(t *testing.T) {
	e := newEnv(t)
	if _, err := e.hub.SetSnapConfig(e.fid, SnapConfig{Keep: 30}); err != nil {
		t.Fatal(err)
	}
	type rec struct {
		ver     int64
		path    string
		content string
	}
	var mu sync.Mutex
	var hist []rec
	paths := []string{"a.md", "b.md", "папка/c.md"}
	base := map[string]int64{}
	for _, p := range paths {
		c := p + " #0"
		r := commitDirect(t, e.hub, e.fid, "W", p, c, 0)
		base[p] = r.Ver
		hist = append(hist, rec{r.Ver, p, c})
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for n := 1; ; n++ {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			for _, p := range paths {
				c := fmt.Sprintf("%s #%d %s", p, n, strings.Repeat("x", n%50*100))
				hs := sum(c)
				if _, err := e.hub.WritePart(e.fid, "W", p, hs, 0, strings.NewReader(c)); err != nil {
					done <- err
					return
				}
				r, err := e.hub.Commit(e.fid, "W", p, hs, base[p])
				if err != nil {
					done <- fmt.Errorf("запись во время снимка упала: %w", err)
					return
				}
				if r.Conflict {
					done <- fmt.Errorf("конфликт у единственного писателя: %+v", r)
					return
				}
				base[p] = r.Ver
				mu.Lock()
				hist = append(hist, rec{r.Ver, p, c})
				mu.Unlock()
			}
		}
	}()
	var snaps []Snapshot
	for i := 0; i < 6; i++ {
		s, err := e.hub.SnapNow(e.fid, SnapManual)
		if err != nil {
			t.Fatal(err)
		}
		snaps = append(snaps, s)
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, s := range snaps {
		got, meta := zipTree(t, snapPath(t, e.hub, e.fid, s.File))
		if meta.Seq != s.Seq {
			t.Fatalf("номер в snapshot.json %d, в списке %d", meta.Seq, s.Seq)
		}
		want := map[string]string{"папка/": ""}
		best := map[string]int64{}
		mu.Lock()
		for _, r := range hist {
			if r.ver <= s.Seq && r.ver > best[r.path] {
				best[r.path], want[r.path] = r.ver, r.content
			}
		}
		mu.Unlock()
		if len(got) != len(want) {
			t.Fatalf("снимок %d: файлы %v, ждали %v", s.Seq, keys(got), keys(want))
		}
		for p, c := range want {
			if got[p] != c {
				t.Fatalf("снимок на номере %d: %s = %.20q…, ждали %.20q… — архив не совпал с одним номером изменения", s.Seq, p, got[p], c)
			}
		}
		if len(meta.Files) != 3 || len(meta.Dirs) != 1 {
			t.Fatalf("meta: %d файлов, %d папок", len(meta.Files), len(meta.Dirs))
		}
		for _, x := range meta.Files {
			if sum(got[x.Path]) != x.Hash {
				t.Fatalf("хеш %s в snapshot.json не тот", x.Path)
			}
		}
	}
}

// Ротация: N «по расписанию» и «вручную» + не больше 3 «перед
// восстановлением»; лишние архивы удалены с диска.
func TestSnapRotation(t *testing.T) {
	e := newEnv(t)
	e.write(e.root, "x.md", "v0")
	if _, err := e.hub.SetSnapConfig(e.fid, SnapConfig{Keep: 2}); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 23, 12, 0, 0, 0, time.Local)
	e.hub.now = func() time.Time { return clock }
	tick := func() { clock = clock.Add(time.Minute) }
	var first Snapshot
	for i := 0; i < 4; i++ {
		tick()
		s, err := e.hub.SnapNow(e.fid, SnapManual)
		if err != nil {
			t.Fatal(err)
		}
		if i == 3 {
			first = s
		}
	}
	for i := 0; i < 5; i++ {
		tick()
		if _, err := e.hub.RestoreSnapshot(e.fid, "тест", first.File); err != nil {
			t.Fatal(err)
		}
	}
	sl, err := e.hub.Snapshots(e.fid)
	if err != nil {
		t.Fatal(err)
	}
	reg, pre := 0, 0
	var total int64
	for _, s := range sl.Snapshots {
		if s.Reason == SnapPre {
			pre++
		} else {
			reg++
		}
		total += s.Size
	}
	if reg != 2 || pre != 3 {
		t.Fatalf("осталось %d обычных и %d «перед восстановлением», ждали 2 и 3: %+v", reg, pre, sl.Snapshots)
	}
	if total != sl.Total || total == 0 {
		t.Fatalf("общий размер %d, сумма %d", sl.Total, total)
	}
	zips, _ := filepath.Glob(filepath.Join(sl.Dir, "*.zip"))
	if len(zips) != 5 {
		t.Fatalf("на диске %d архивов, ждали 5: %v", len(zips), zips)
	}
	for i := 1; i < len(sl.Snapshots); i++ {
		if sl.Snapshots[i-1].Time < sl.Snapshots[i].Time {
			t.Fatal("список не «новые сверху»")
		}
	}
}

// Пропущенная ночь (устройство выключено) — один снимок при следующем
// запуске, сколько бы ночей ни пропало.
func TestSnapMissedNight(t *testing.T) {
	e := newEnv(t)
	e.write(e.root, "x.md", "v0")
	clock := time.Date(2026, 11, 20, 10, 0, 0, 0, time.Local)
	e.hub.now = func() time.Time { return clock }
	if _, err := e.hub.SetSnapConfig(e.fid, SnapConfig{Daily: true, Time: "03:00", Keep: 10}); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		sl, err := e.hub.Snapshots(e.fid)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, s := range sl.Snapshots {
			if s.Reason == SnapAuto && s.Error == "" {
				n++
			}
		}
		return n
	}
	clock = clock.Add(time.Hour)
	e.hub.SnapTick()
	if n := count(); n != 0 {
		t.Fatalf("только включили днём — снимок сразу не нужен, а их %d", n)
	}
	// Три ночи устройство было выключено.
	clock = time.Date(2026, 11, 23, 9, 0, 0, 0, time.Local)
	e.hub.SnapTick()
	e.hub.SnapTick()
	if n := count(); n != 1 {
		t.Fatalf("после пропущенных ночей %d снимков, ждали один", n)
	}
	clock = time.Date(2026, 11, 24, 2, 59, 0, 0, time.Local)
	e.hub.SnapTick()
	if n := count(); n != 1 {
		t.Fatalf("до 03:00 снимков %d", n)
	}
	clock = time.Date(2026, 11, 24, 3, 0, 30, 0, time.Local)
	e.hub.SnapTick()
	e.hub.SnapTick()
	if n := count(); n != 2 {
		t.Fatalf("в 03:00 снимков %d, ждали 2", n)
	}
}

// Восстановление: удаление, замена, добавление и пустые папки доходят до
// второго устройства обычной сверкой; неотправленная правка на устройстве
// не пропадает, а становится конфликт-копией.
func TestSnapRestore(t *testing.T) {
	e := newEnv(t)
	a, b := e.client("A", ModeBoth), e.client("B", ModeBoth)
	x0 := e.write(e.root, "x.md", "v0")
	y0 := e.write(e.root, "sub/y.md", "v0")
	if err := os.MkdirAll(full(e.root, "пустая"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.settle()
	s, err := e.hub.SnapNow(e.fid, SnapManual)
	if err != nil {
		t.Fatal(err)
	}
	// После снимка папку «засорили» с A.
	e.write(a.Path, "x.md", "правка A")
	e.remove(a.Path, "sub/y.md")
	e.write(a.Path, "мусор/z.md", "лишнее")
	os.Remove(full(a.Path, "пустая"))
	e.settle()
	if exists(e.root, "пустая") || !exists(e.root, "мусор/z.md") {
		t.Fatal("засорение не дошло до сервера")
	}
	// У B — своя правка x, ещё не отправленная.
	bEdit := e.write(b.Path, "x.md", "правка B не отправлена")

	res, err := e.hub.RestoreSnapshot(e.fid, "server", s.File)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 1 || res.Deleted != 1 || res.Added != 1 {
		t.Fatalf("итог %+v, ждали изменено 1, удалено 1, добавлено 1", res)
	}
	if !strings.Contains(res.Text, "изменено 1, удалено 1, добавлено 1") || res.Pre == "" {
		t.Fatalf("текст %q, pre %q", res.Text, res.Pre)
	}
	if read(t, e.root, "x.md") != x0 || read(t, e.root, "sub/y.md") != y0 || exists(e.root, "мусор/z.md") {
		t.Fatal("сервер не вернулся к снимку")
	}
	if !isDirAt(e.root, "пустая") {
		t.Fatal("пустая папка не вернулась")
	}
	e.settle()
	ta := tree(t, a.Path)
	if ta["x.md"] != x0 || ta["sub/y.md"] != y0 {
		t.Fatalf("до A не дошло: %v", keys(ta))
	}
	if _, ok := ta["мусор/z.md"]; ok {
		t.Fatal("лишний файл остался у A")
	}
	if !isDirAt(a.Path, "пустая") || !isDirAt(b.Path, "пустая") {
		t.Fatal("пустая папка не дошла до устройств")
	}
	found := false
	for p, c := range tree(t, e.root) {
		if strings.Contains(p, "конфликт") && c == bEdit {
			found = true
		}
	}
	if !found {
		t.Fatalf("неотправленная правка B пропала: %v", keys(tree(t, e.root)))
	}
	e.converged()
	e.noLoss()
	// Удалённое при восстановлении — в корзине.
	vs, _ := e.hub.Versions(e.fid, "мусор/z.md")
	if len(vs) == 0 || !vs[0].Deleted {
		t.Fatal("удалённый при восстановлении файл не в корзине")
	}
}

// Нет места на диске: снимок не сделан, ошибка в списке, старые снимки
// целы; восстановление без снимка «перед» не идёт.
func TestSnapDiskFull(t *testing.T) {
	e := newEnv(t)
	e.write(e.root, "x.md", strings.Repeat("данные ", 200))
	if _, err := e.hub.SetSnapConfig(e.fid, SnapConfig{Keep: 2}); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 23, 12, 0, 0, 0, time.Local)
	e.hub.now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	var good []Snapshot
	for i := 0; i < 2; i++ {
		s, err := e.hub.SnapNow(e.fid, SnapManual)
		if err != nil {
			t.Fatal(err)
		}
		good = append(good, s)
	}
	snapCap = 100
	defer func() { snapCap = 0 }()
	if _, err := e.hub.SnapNow(e.fid, SnapManual); err == nil || !strings.Contains(err.Error(), "места") {
		t.Fatalf("ждали ошибку «не хватило места», а %v", err)
	}
	e.write(e.root, "x.md", "после")
	before := tree(t, e.root)
	if _, err := e.hub.RestoreSnapshot(e.fid, "тест", good[0].File); err == nil {
		t.Fatal("восстановление прошло без снимка «перед восстановлением»")
	}
	if !sameTree(before, tree(t, e.root)) {
		t.Fatal("папку тронули, хотя снимок «перед» не сделан")
	}
	snapCap = 0
	sl, _ := e.hub.Snapshots(e.fid)
	ok, bad := 0, 0
	for _, s := range sl.Snapshots {
		if s.Error != "" {
			bad++
		} else {
			ok++
		}
	}
	if ok != 2 || bad == 0 {
		t.Fatalf("ждали 2 целых снимка и ошибку в списке: %+v", sl.Snapshots)
	}
	for _, s := range good {
		if _, err := os.Stat(snapPath(t, e.hub, e.fid, s.File)); err != nil {
			t.Fatalf("старый снимок удалён: %v", err)
		}
	}
	left, _ := filepath.Glob(filepath.Join(sl.Dir, tmpPrefix+"*"))
	if len(left) != 0 {
		t.Fatalf("остались временные файлы: %v", left)
	}
}

// В корне папки свой snapshot.json — он в архиве как есть, служебный
// ложится под другим именем, восстановление их не путает.
func TestSnapOwnSnapshotJSON(t *testing.T) {
	e := newEnv(t)
	own := e.write(e.root, "snapshot.json", `{"моё":1}`)
	s, err := e.hub.SnapNow(e.fid, SnapManual)
	if err != nil {
		t.Fatal(err)
	}
	got, meta := zipTree(t, snapPath(t, e.hub, e.fid, s.File))
	if got["snapshot.json"] != own || len(meta.Files) != 1 {
		t.Fatalf("свой snapshot.json: %q, meta %d", got["snapshot.json"], len(meta.Files))
	}
	e.write(e.root, "snapshot.json", "другое")
	if _, err := e.hub.RestoreSnapshot(e.fid, "тест", s.File); err != nil {
		t.Fatal(err)
	}
	if read(t, e.root, "snapshot.json") != own {
		t.Fatal("не восстановился")
	}
}

// Подключение к непустой папке с «Взять с сервера»: свои файлы — в архив,
// из папки убраны, потом приходит содержимое сервера, а на сервер не
// уходит ничего своего.
func TestJoinTakeFromServer(t *testing.T) {
	e := newEnv(t)
	e.write(e.root, "общее.md", "сервер")
	e.write(e.root, "папка/два.md", "сервер")
	server := tree(t, e.root)

	local := t.TempDir()
	dir := filepath.Join(local, "Моя папка")
	if err := os.MkdirAll(filepath.Join(dir, "пустая"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Мусор и пустые папки — не повод спрашивать.
	os.WriteFile(filepath.Join(dir, "Thumbs.db"), []byte("x"), 0o644)
	if ne, err := checkNonEmpty(dir, time.Now()); err != nil || ne != nil {
		t.Fatalf("папка только с мусором и пустой папкой — не пустая? %+v %v", ne, err)
	}
	if ne, err := checkNonEmpty(filepath.Join(local, "нет такой"), time.Now()); err != nil || ne != nil {
		t.Fatalf("несуществующая папка: %+v %v", ne, err)
	}
	os.WriteFile(filepath.Join(dir, "моё.md"), []byte("своё 1"), 0o644)
	os.MkdirAll(filepath.Join(dir, "глубоко", "внутри"), 0o755)
	os.WriteFile(filepath.Join(dir, "глубоко", "внутри", "моё2.md"), []byte("своё 2"), 0o644)
	ne, err := checkNonEmpty(dir, time.Now())
	if err != nil || ne == nil || ne.Files != 2 {
		t.Fatalf("непустая папка: %+v %v", ne, err)
	}
	if filepath.Dir(ne.Zip) != local || !strings.Contains(filepath.Base(ne.Zip), "Моя папка до подключения ") {
		t.Fatalf("архив не рядом с папкой: %s", ne.Zip)
	}
	n, err := moveToZip(dir, ne.Zip)
	if err != nil || n != 2 {
		t.Fatalf("moveToZip: %d %v", n, err)
	}
	zr, err := zip.OpenReader(ne.Zip)
	if err != nil {
		t.Fatal(err)
	}
	inZip := map[string]string{}
	for _, zf := range zr.File {
		rc, _ := zf.Open()
		var buf bytes.Buffer
		io.Copy(&buf, rc)
		rc.Close()
		inZip[zf.Name] = buf.String()
	}
	zr.Close()
	if inZip["моё.md"] != "своё 1" || inZip["глубоко/внутри/моё2.md"] != "своё 2" || len(inZip) != 2 {
		t.Fatalf("в архиве %v", inZip)
	}
	if left, _ := localContent(dir); len(left) != 0 {
		t.Fatalf("в папке остались свои файлы: %v", left)
	}
	if exists(dir, "глубоко") || exists(dir, "пустая") {
		t.Fatal("пустые папки остались — ушли бы на сервер")
	}

	c := e.client("Новое", ModeBoth)
	c.Path = dir
	e.settle()
	got := tree(t, dir)
	if !sameTree(got, server) {
		t.Fatalf("у устройства %v, на сервере %v", keys(got), keys(server))
	}
	if !sameTree(tree(t, e.root), server) {
		t.Fatalf("на сервер ушло своё: %v", keys(tree(t, e.root)))
	}
}

// «Объединить» — как раньше: свои файлы уходят в общую папку.
func TestJoinMergeKeepsOldBehaviour(t *testing.T) {
	e := newEnv(t)
	e.write(e.root, "общее.md", "сервер")
	c := e.client("Новое", ModeBoth)
	e.write(c.Path, "моё.md", "своё")
	e.settle()
	got := fileKeys(tree(t, e.root))
	sort.Strings(got)
	if strings.Join(got, ",") != "моё.md,общее.md" {
		t.Fatalf("на сервере %v", got)
	}
}
