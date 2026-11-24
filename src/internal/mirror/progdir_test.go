package mirror

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Папка программы на каждой системе (ТЗ 39): CasaOS / ZimaOS — в
// /DATA/AppData, иначе в домашней папке; Android — в корне общей памяти.
func TestProgramDirFor(t *testing.T) {
	casa := func(p string) bool { return p == "/DATA/AppData" }
	none := func(string) bool { return false }
	cases := []struct {
		goos, home string
		isDir      func(string) bool
		want       string
	}{
		{"linux", "/home/user", casa, "/DATA/AppData/ssh_tunnel"},
		{"linux", "/root", none, "/root/ssh_tunnel"},
		{"linux", "", none, ""},
		{"linux", "", casa, "/DATA/AppData/ssh_tunnel"},
		{"windows", `C:\Users\user`, casa, `C:\Users\user\ssh_tunnel`},
		{"windows", `C:\Users\user\`, none, `C:\Users\user\ssh_tunnel`},
		{"windows", "", none, ""},
		{"android", "", none, "/storage/emulated/0/ssh_tunnel"},
		{"android", "/data/user/0/x", casa, "/storage/emulated/0/ssh_tunnel"},
	}
	for _, c := range cases {
		if got := programDirFor(c.goos, c.home, c.isDir); got != c.want {
			t.Errorf("%s, дом %q: %q, ждали %q", c.goos, c.home, got, c.want)
		}
	}
}

// Новая папка по умолчанию — <папка программы>/mirrored_folders/<имя>.
func TestNewFolderPath(t *testing.T) {
	prog := filepath.Join(t.TempDir(), "ssh_tunnel")
	if got, want := newFolderPath(prog, "Заметки"), filepath.Join(prog, "mirrored_folders", "Заметки"); got != want {
		t.Fatalf("%q, ждали %q", got, want)
	}
	if got := newFolderPath(prog, `a/b:c`); filepath.Base(got) != "a_b_c" {
		t.Fatalf("имя со слешами: %q", got)
	}
	if newFolderPath("", "x") != "" {
		t.Fatal("без папки программы — пусто")
	}
	m := &module{prog: prog}
	if got := m.newPath("Server"); got != filepath.Join(prog, "mirrored_folders", "Server") {
		t.Fatalf("newPath: %q", got)
	}
	if m.suggestBase() != filepath.Join(prog, "mirrored_folders") {
		t.Fatalf("suggest: %q", m.suggestBase())
	}
	// Раздать ещё не созданную папку в mirrored_folders — создаётся сама;
	// любой другой путь — нет.
	p := m.newPath("Новая")
	if err := m.makeNew(p); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(p); err != nil || !st.IsDir() {
		t.Fatalf("папка не создалась: %v", err)
	}
	other := filepath.Join(t.TempDir(), "нет")
	if err := m.makeNew(other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("создалась папка вне mirrored_folders")
	}
}

func snapRootJSON(t *testing.T, e *env) snapRootCfg {
	t.Helper()
	var c snapRootCfg
	if err := readJSON(filepath.Join(e.hubDir, snapRootFile), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// Обновление: снимки из данных программы один раз переезжают в
// <папка программы>/backups/<имя папки>/; после перезапуска — там же.
func TestSnapDefaultMigrates(t *testing.T) {
	e := newEnv(t)
	e.write(e.root, "x.md", "v0")
	old, err := e.hub.SnapNow(e.fid, SnapManual)
	if err != nil {
		t.Fatal(err)
	}
	inData := snapPath(t, e.hub, e.fid, old.File)

	def := filepath.Join(t.TempDir(), "ssh_tunnel", "backups")
	if err := e.hub.SetSnapRootDefault(def); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(def, "Тест")
	sl, err := e.hub.Snapshots(e.fid)
	if err != nil {
		t.Fatal(err)
	}
	if sl.Dir != want || len(sl.Snapshots) != 1 {
		t.Fatalf("снимки в %q (%d шт.), ждали %q", sl.Dir, len(sl.Snapshots), want)
	}
	if _, err := os.Stat(filepath.Join(want, old.File)); err != nil {
		t.Fatalf("снимок не переехал: %v", err)
	}
	if _, err := os.Stat(inData); !os.IsNotExist(err) {
		t.Fatal("в данных программы остался архив")
	}
	if c := snapRootJSON(t, e); c.Dir != "" || c.At == nil || *c.At != def {
		t.Fatalf("snaproot.json: %+v", c)
	}
	if e.hub.SnapRoot() != def || e.hub.SnapRootCustom() != "" || e.hub.SnapRootDefault() != def {
		t.Fatal("папки снимков не те")
	}

	// Перезапуск: снимок на месте, второй раз ничего не переезжает.
	e.hub.Close()
	e.startHub()
	if e.hub.SnapRoot() != def {
		t.Fatalf("после перезапуска снимки в %q", e.hub.SnapRoot())
	}
	if err := e.hub.SetSnapRootDefault(def); err != nil {
		t.Fatal(err)
	}
	e.hub.now = func() time.Time { return time.Now().Add(2 * time.Minute) } // другое имя архива
	s, err := e.hub.SnapNow(e.fid, SnapManual)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(want, s.File)); err != nil {
		t.Fatalf("новый снимок не в папке по умолчанию: %v", err)
	}
	if sl, _ := e.hub.Snapshots(e.fid); len(sl.Snapshots) != 2 {
		t.Fatalf("снимков %d, ждали 2", len(sl.Snapshots))
	}
}

// Переезд прошёл не до конца (архив остался в данных программы) — снимок
// всё равно виден и открывается.
func TestSnapLeftInData(t *testing.T) {
	e := newEnv(t)
	e.write(e.root, "x.md", "v0")
	s, err := e.hub.SnapNow(e.fid, SnapManual)
	if err != nil {
		t.Fatal(err)
	}
	inData := snapPath(t, e.hub, e.fid, s.File)
	b, err := os.ReadFile(inData)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.hub.SetSnapRootDefault(filepath.Join(t.TempDir(), "backups")); err != nil {
		t.Fatal(err)
	}
	sl, _ := e.hub.Snapshots(e.fid)
	os.Remove(filepath.Join(sl.Dir, s.File))
	os.WriteFile(inData, b, 0o600)
	if sl, _ := e.hub.Snapshots(e.fid); len(sl.Snapshots) != 1 {
		t.Fatal("оставшийся в данных снимок пропал из списка")
	}
	fh, _, err := e.hub.OpenSnapshot(e.fid, s.File)
	if err != nil {
		t.Fatal(err)
	}
	fh.Close()
}

// «Папка по умолчанию» ↔ «Своя папка…»: снимки переезжают в обе стороны;
// своя папка из прошлой версии (snaproot.json без «at») остаётся своей.
func TestSnapDefaultAndCustom(t *testing.T) {
	e := newEnv(t)
	e.write(e.root, "x.md", "v0")
	def := filepath.Join(t.TempDir(), "backups")
	if err := e.hub.SetSnapRootDefault(def); err != nil {
		t.Fatal(err)
	}
	s, err := e.hub.SnapNow(e.fid, SnapManual)
	if err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(t.TempDir(), "мои копии")
	got, err := e.hub.SetSnapRoot(custom)
	if err != nil || got != custom {
		t.Fatalf("своя папка: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(custom, "Тест", s.File)); err != nil {
		t.Fatalf("снимок не переехал в свою папку: %v", err)
	}
	if _, err := os.Stat(filepath.Join(def, "Тест")); !os.IsNotExist(err) {
		t.Fatal("в папке по умолчанию осталась подпапка")
	}
	got, err = e.hub.SetSnapRoot("")
	if err != nil || got != def {
		t.Fatalf("обратно по умолчанию: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(def, "Тест", s.File)); err != nil {
		t.Fatalf("снимок не вернулся в папку по умолчанию: %v", err)
	}
	if e.hub.SnapRootCustom() != "" {
		t.Fatal("своя папка не сбросилась")
	}

	// Файл прошлой версии со своей папкой: остаётся, ничего не переезжает.
	if _, err := e.hub.SetSnapRoot(custom); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"dir": custom})
	os.WriteFile(filepath.Join(e.hubDir, snapRootFile), b, 0o600)
	e.hub.Close()
	e.startHub()
	if err := e.hub.SetSnapRootDefault(def); err != nil {
		t.Fatal(err)
	}
	if e.hub.SnapRoot() != custom || e.hub.SnapRootCustom() != custom {
		t.Fatalf("своя папка из прошлой версии сменилась на %q", e.hub.SnapRoot())
	}
	if _, err := os.Stat(filepath.Join(custom, "Тест", s.File)); err != nil {
		t.Fatalf("снимок уехал из своей папки: %v", err)
	}
}

// Папка по умолчанию внутри раздаваемой папки (раздают саму папку
// программы) — снимки остаются в данных программы.
func TestSnapDefaultInsideShared(t *testing.T) {
	e := newEnv(t)
	def := filepath.Join(e.root, "backups")
	if err := e.hub.SetSnapRootDefault(def); err != nil {
		t.Fatal(err)
	}
	if e.hub.SnapRoot() != "" {
		t.Fatalf("снимки в раздаваемой папке: %q", e.hub.SnapRoot())
	}
	if _, err := os.Stat(def); !os.IsNotExist(err) {
		t.Fatal("в раздаваемой папке создалась папка снимков")
	}
}

// Размер папки: у сервера — по индексу, без исключённых; у подключённой —
// по последней сверке.
func TestFolderBytes(t *testing.T) {
	e := newEnv(t)
	e.write(e.root, "a.md", "aaaa")
	e.write(e.root, "d/b.md", "bbbbbbbb")
	e.write(e.root, "x.log", "лишнее")
	if err := e.hub.Scan(e.fid); err != nil {
		t.Fatal(err)
	}
	size := func(rel string) int64 {
		st, err := os.Stat(full(e.root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return st.Size()
	}
	n, b, _, err := e.hub.Counts(e.fid)
	if err != nil {
		t.Fatal(err)
	}
	if want := size("a.md") + size("d/b.md") + size("x.log"); n != 3 || b != want {
		t.Fatalf("сервер: %d файлов, %d байт; ждали 3 и %d", n, b, want)
	}
	if err := e.hub.SetIgnore(e.fid, []string{"*.log"}); err != nil {
		t.Fatal(err)
	}
	n, b, _, _ = e.hub.Counts(e.fid)
	if want := size("a.md") + size("d/b.md"); n != 2 || b != want {
		t.Fatalf("без исключённых: %d файлов, %d байт; ждали 2 и %d", n, b, want)
	}

	c := e.client("phone", ModeBoth)
	e.settle()
	if _, err := c.Sync(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if st := c.Status(); st.Files != 2 || st.Bytes != size("a.md")+size("d/b.md") {
		t.Fatalf("подключённая: %d файлов, %d байт", st.Files, st.Bytes)
	}
}
