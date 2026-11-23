package mirror

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Подключение зеркальной папки к непустой папке устройства (ТЗ 36, часть B).
// Раньше всё, что лежало в папке, молча уходило на сервер и смешивалось с
// общей папкой на всех устройствах. Теперь программа спрашивает:
//   - «Взять с сервера» (по умолчанию): свои файлы — в архив рядом с папкой
//     («<папка> до подключения <дата>.zip»), потом — содержимое сервера;
//   - «Объединить»: как раньше, всё уходит в общую папку;
//   - «Отмена».

// Ответы на вопрос о непустой папке (поле existing в op join).
const (
	JoinAsk    = ""       // спросить, если папка не пустая
	JoinServer = "server" // взять с сервера, своё — в архив
	JoinMerge  = "merge"  // объединить (как раньше)
)

// NonEmpty — в выбранной папке уже есть файлы.
type NonEmpty struct {
	Path   string   `json:"path"`
	Files  int      `json:"files"`
	Bytes  int64    `json:"bytes"`
	Sample []string `json:"sample"` // первые несколько файлов
	Zip    string   `json:"zip"`    // куда лягут свои файлы при «Взять с сервера»
}

// localContent — свои файлы папки, которые ушли бы на сервер: без мусора,
// исключённого и пустых папок. Папки нет — пусто.
func localContent(abs string) (map[string]stat, error) {
	disk, err := scanDir(abs, NewIgnore(nil))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]stat{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]stat{}
	for p, st := range disk {
		if isDir(p) {
			continue
		}
		if junkName(filepath.Base(filepath.FromSlash(p))) {
			continue
		}
		out[p] = st
	}
	return out, nil
}

// checkNonEmpty — nil, если папка пустая (или её нет).
func checkNonEmpty(abs string, now time.Time) (*NonEmpty, error) {
	files, err := localContent(abs)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	ne := &NonEmpty{Path: abs, Files: len(files), Sample: []string{}}
	keys := make([]string, 0, len(files))
	for p, st := range files {
		keys = append(keys, p)
		ne.Bytes += st.Size
	}
	sort.Strings(keys)
	if len(keys) > 5 {
		keys = keys[:5]
	}
	ne.Sample = keys
	ne.Zip = joinZipPath(abs, now)
	return ne, nil
}

// joinZipPath — «<папка> до подключения ГГГГ-ММ-ДД ЧЧ-ММ.zip» рядом с
// папкой (на одном уровне с ней — не внутри: иначе архив ушёл бы на сервер).
func joinZipPath(abs string, now time.Time) string {
	base := strings.Trim(filepath.Base(abs), ". ")
	if base == "" || base == string(filepath.Separator) {
		base = "папка"
	}
	stem := filepath.Join(filepath.Dir(abs), base+" до подключения "+now.Format("2006-01-02 15-04"))
	for i := 1; ; i++ {
		p := stem + ".zip"
		if i > 1 {
			p = fmt.Sprintf("%s (%d).zip", stem, i)
		}
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
	}
}

// moveToZip убирает свои файлы папки abs в архив zipPath: сначала архив
// целиком (и на диск), потом удаление файлов и опустевших папок. Не удалось
// записать архив — ничего не удаляется. Исключённое (раскладка окон
// Obsidian и т.п.) остаётся на месте: оно и так не синхронизируется.
// Возвращает, сколько файлов убрано.
func moveToZip(abs, zipPath string) (int, error) {
	files, err := localContent(abs)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}
	keys := make([]string, 0, len(files))
	for p := range files {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return 0, err
	}
	tmp := zipPath + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	fail := func(e error) (int, error) {
		out.Close()
		os.Remove(tmp)
		return 0, e
	}
	zw := zip.NewWriter(out)
	for _, p := range keys {
		src := full(abs, p)
		hdr := &zip.FileHeader{Name: p, Method: zip.Deflate, Modified: time.Unix(0, files[p].MTime)}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return fail(err)
		}
		in, err := openShared(src)
		if err != nil {
			return fail(err)
		}
		_, err = io.Copy(w, in)
		in.Close()
		if err != nil {
			return fail(err)
		}
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, zipPath); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	// Архив на месте — теперь убрать из папки.
	n := 0
	dirs := map[string]bool{}
	for _, p := range keys {
		if err := os.Remove(full(abs, p)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return n, fmt.Errorf("файлы в архиве %s, но %s не удалился: %w", zipPath, p, err)
		}
		n++
		for d := filepath.Dir(filepath.FromSlash(p)); d != "." && d != string(filepath.Separator); d = filepath.Dir(d) {
			dirs[d] = true
		}
	}
	// И пустые папки: при «Взять с сервера» отсюда на сервер не уходит ничего.
	if disk, err := scanDir(abs, NewIgnore(nil)); err == nil {
		for p := range disk {
			if isDir(p) {
				dirs[filepath.FromSlash(strings.TrimSuffix(p, "/"))] = true
			}
		}
	}
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	for _, d := range list {
		_ = os.Remove(filepath.Join(abs, d)) // только опустевшие
	}
	return n, nil
}
