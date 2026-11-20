package mirror

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// ТЗ 33: удаление папок доходит до всех устройств. Раньше файлы удалялись,
// а пустые папки оставались на других устройствах (или воскресали).

func isDirAt(root, rel string) bool {
	fi, err := os.Stat(full(root, rel))
	return err == nil && fi.IsDir()
}

func touchRaw(t *testing.T, root, rel, what string) {
	t.Helper()
	p := full(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(what), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Непустая папка с вложенной удалена на A — на B нет ни файлов, ни папок,
// и сервер не возвращает её обратно.
func TestDirDeleteNonEmpty(t *testing.T) {
	e := newEnv(t)
	a, b := e.client("A", ModeBoth), e.client("B", ModeBoth)
	e.write(a.Path, "Наука/заметка.md", "1")
	e.write(a.Path, "Наука/Глава/вторая.md", "2")
	e.write(a.Path, "Наука/Глава/Пустая/.keep", "3")
	e.write(a.Path, "Остаётся.md", "4")
	e.settle()
	if !isDirAt(b.Path, "Наука/Глава/Пустая") {
		t.Fatal("папка не доехала до B")
	}
	e.remove(a.Path, "Наука/заметка.md")
	e.remove(a.Path, "Наука/Глава/вторая.md")
	e.remove(a.Path, "Наука/Глава/Пустая/.keep")
	if err := os.RemoveAll(full(a.Path, "Наука")); err != nil {
		t.Fatal(err)
	}
	e.settle()
	for _, root := range []string{a.Path, b.Path, e.root} {
		if exists(root, "Наука") {
			t.Fatalf("удалённая папка осталась в %s: %v", root, keys(tree(t, root)))
		}
	}
	if !exists(b.Path, "Остаётся.md") {
		t.Fatal("пропал соседний файл")
	}
	e.converged()
	e.noLoss()
}

// На B в папке лежат недокачанный .part и системный мусор — папка всё
// равно удаляется и не воскресает на сервере и у A.
func TestDirDeleteWithJunk(t *testing.T) {
	e := newEnv(t)
	a, b := e.client("A", ModeBoth), e.client("B", ModeBoth)
	e.write(a.Path, "Наука/заметка.md", "1")
	e.settle()
	touchRaw(t, b.Path, "Наука/"+tmpPrefix+"0123456789abcdef.part", "недокачано")
	touchRaw(t, b.Path, "Наука/desktop.ini", "[.ShellClassInfo]")
	touchRaw(t, e.root, "Наука/Thumbs.db", "мусор на сервере")
	e.remove(a.Path, "Наука/заметка.md")
	if err := os.RemoveAll(full(a.Path, "Наука")); err != nil {
		t.Fatal(err)
	}
	e.settle()
	for _, root := range []string{a.Path, b.Path, e.root} {
		if exists(root, "Наука") {
			t.Fatalf("папка с мусором осталась в %s", root)
		}
	}
	e.converged()
	e.noLoss()
}

// На B в удалённой папке новый настоящий файл — папка возвращается с ним
// везде (своё важнее удаления).
func TestDirDeleteNewFileWins(t *testing.T) {
	e := newEnv(t)
	a, b := e.client("A", ModeBoth), e.client("B", ModeBoth)
	e.write(a.Path, "Наука/заметка.md", "1")
	e.settle()
	e.remove(a.Path, "Наука/заметка.md")
	if err := os.RemoveAll(full(a.Path, "Наука")); err != nil {
		t.Fatal(err)
	}
	if err := e.sync(a); err != nil {
		t.Fatal(err)
	}
	if exists(e.root, "Наука") {
		t.Fatal("сервер не удалил папку")
	}
	e.write(b.Path, "Наука/новая.md", "написано на B до сверки")
	e.settle()
	for _, root := range []string{a.Path, b.Path, e.root} {
		if !exists(root, "Наука/новая.md") {
			t.Fatalf("новый файл B не дошёл до %s: %v", root, keys(tree(t, root)))
		}
	}
	if exists(b.Path, "Наука/заметка.md") {
		t.Fatal("удалённый файл вернулся")
	}
	e.converged()
	e.noLoss()
}

// Исключённый из синхронизации файл в удалённой папке: папка остаётся на
// этом устройстве, но на сервер не возвращается, и сверка не крутится.
func TestDirDeleteKeepsIgnored(t *testing.T) {
	e := newEnv(t)
	a, b := e.client("A", ModeBoth), e.client("B", ModeBoth)
	e.write(a.Path, "Наука/заметка.md", "1")
	e.settle()
	touchRaw(t, b.Path, "Наука/черновик.swp", "своё у B")
	e.remove(a.Path, "Наука/заметка.md")
	if err := os.RemoveAll(full(a.Path, "Наука")); err != nil {
		t.Fatal(err)
	}
	e.settle()
	if !exists(b.Path, "Наука/черновик.swp") {
		t.Fatal("исключённый файл пропал")
	}
	if exists(e.root, "Наука") || exists(a.Path, "Наука") {
		t.Fatal("папка вернулась на сервер из-за исключённого файла")
	}
	if did, err := b.Sync(context.Background(), 0, false); did || err != nil {
		t.Fatalf("лишняя работа на B: %v %v", did, err)
	}
}

// Хвосты прошлых версий: на B пустые папки, которые сервер помнит
// удалёнными, — со своим состоянием сверки «совпадает» или без записи.
// После обновления B убирает их сам.
func TestDirDeleteOldLeftovers(t *testing.T) {
	e := newEnv(t)
	a, b := e.client("A", ModeBoth), e.client("B", ModeBoth)
	e.write(a.Path, "Наука/заметка.md", "1")
	e.write(a.Path, "Учеба/Конференция/доклад.md", "2")
	e.settle()
	e.remove(a.Path, "Наука/заметка.md")
	e.remove(a.Path, "Учеба/Конференция/доклад.md")
	os.RemoveAll(full(a.Path, "Наука"))
	os.RemoveAll(full(a.Path, "Учеба/Конференция"))
	e.settle()
	// Вернуть хвосты, как их оставляли старые версии.
	for _, d := range []string{"Наука", "Учеба/Конференция"} {
		if err := os.MkdirAll(full(b.Path, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b.mu.Lock()
	b.st.Synced["Наука/"] = repFile{Ver: b.st.Remote["Наука/"].Ver, Hash: DirHash}
	delete(b.st.Synced, "Учеба/Конференция/")
	_ = b.save()
	b.mu.Unlock()
	e.settle()
	for _, d := range []string{"Наука", "Учеба/Конференция"} {
		for _, root := range []string{a.Path, b.Path, e.root} {
			if exists(root, d) {
				t.Fatalf("хвост %s остался в %s", d, root)
			}
		}
	}
	if !isDirAt(b.Path, "Учеба") {
		t.Fatal("удалена живая папка Учеба")
	}
	e.converged()
}

// Новая пустая папка, созданная на B заново после удаления, уходит на
// сервер, а не удаляется.
func TestDirRecreatedAfterDelete(t *testing.T) {
	e := newEnv(t)
	a, b := e.client("A", ModeBoth), e.client("B", ModeBoth)
	if err := os.MkdirAll(full(a.Path, "Папка"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.settle()
	os.Remove(full(a.Path, "Папка"))
	e.settle()
	if exists(b.Path, "Папка") {
		t.Fatal("удаление не дошло")
	}
	if err := os.MkdirAll(full(b.Path, "Папка"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.settle()
	if !isDirAt(a.Path, "Папка") || !isDirAt(b.Path, "Папка") {
		t.Fatal("созданная заново папка не дошла")
	}
}
