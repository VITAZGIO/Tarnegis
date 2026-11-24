package mirror

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Папка снимков устройства: <папка>/<имя раздаваемой папки>/<дата>.zip.
// По умолчанию — <папка программы>/backups (ТЗ 39, progdir.go); своя папка
// задаётся человеком. Без папки по умолчанию (хост плагинов на сервере,
// тесты) и без своей — снимки лежат рядом с данными папки (snapshots/).
// Служебный список снимков (snapshots.json) всегда остаётся в данных.

const snapRootFile = "snaproot.json"

type snapRootCfg struct {
	// Dir — своя папка снимков ("" — папка по умолчанию).
	Dir string `json:"dir"`
	// At — где архивы лежат сейчас ("" — в данных программы). Нет поля
	// (файл старой версии) — там же, где Dir.
	At *string `json:"at,omitempty"`
}

func (h *Hub) loadSnapRoot() {
	var c snapRootCfg
	_ = readJSON(filepath.Join(h.dir, snapRootFile), &c)
	h.snapRoot = c.Dir
	h.snapAt = c.Dir
	if c.At != nil {
		h.snapAt = *c.At
	}
}

func (h *Hub) saveSnapRoot(dir, at string) error {
	return writeJSONAtomic(filepath.Join(h.dir, snapRootFile), snapRootCfg{Dir: dir, At: &at})
}

// SnapRoot — где сейчас лежат снимки устройства ("" — рядом с данными
// каждой папки).
func (h *Hub) SnapRoot() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapAt
}

// SnapRootCustom — своя папка снимков ("" — папка по умолчанию).
func (h *Hub) SnapRootCustom() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapRoot
}

// SnapRootDefault — папка снимков по умолчанию ("" — не задана).
func (h *Hub) SnapRootDefault() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapDefault
}

// SetSnapRootDefault — папка снимков по умолчанию (<папка программы>/backups).
// Если своя не задана, снимки, лежащие в данных программы (или в прежней
// папке по умолчанию), переезжают туда — один раз: дальше они уже там.
func (h *Hub) SetSnapRootDefault(dir string) error {
	if dir != "" {
		dir = filepath.Clean(dir)
	}
	h.mu.Lock()
	h.snapDefault = dir
	custom := h.snapRoot
	h.mu.Unlock()
	if custom != "" {
		return nil
	}
	_, err := h.setSnapAt("", h.defaultTarget())
	return err
}

// defaultTarget — куда класть снимки без своей папки: папка по умолчанию,
// если она не внутри раздаваемой папки, иначе — в данные программы.
func (h *Hub) defaultTarget() string {
	h.mu.Lock()
	def := h.snapDefault
	h.mu.Unlock()
	if def == "" {
		return ""
	}
	for _, f := range h.Folders() {
		if within(def, f.Path) {
			return ""
		}
	}
	return def
}

// SetSnapRoot — сменить папку снимков ("" — папка по умолчанию). Уже
// сделанные снимки всех папок переезжают туда же. Папка снимков не может
// быть внутри раздаваемой папки: иначе архивы разошлись бы по всем
// устройствам. Возвращает, где снимки теперь.
func (h *Hub) SetSnapRoot(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir != "" {
		if !filepath.IsAbs(dir) {
			return "", errors.New("папка снимков — полный путь")
		}
		dir = filepath.Clean(dir)
		for _, f := range h.Folders() {
			if within(dir, f.Path) {
				return "", fmt.Errorf("папка снимков внутри раздаваемой папки «%s» — архивы разошлись бы по всем устройствам", f.Name)
			}
		}
	}
	target := dir
	if target == "" {
		target = h.defaultTarget()
	}
	return h.setSnapAt(dir, target)
}

// setSnapAt — запомнить свою папку custom и перенести архивы в target.
func (h *Hub) setSnapAt(custom, target string) (string, error) {
	h.mu.Lock()
	old, oldCustom := h.snapAt, h.snapRoot
	fs := h.list()
	h.mu.Unlock()
	if samePath(old, target) {
		if oldCustom != custom {
			h.mu.Lock()
			h.snapRoot = custom
			h.mu.Unlock()
			if err := h.saveSnapRoot(custom, old); err != nil {
				return "", err
			}
		}
		return target, nil
	}
	if target != "" {
		if err := os.MkdirAll(target, 0o700); err != nil {
			return "", fmt.Errorf("папка снимков не создаётся: %w", err)
		}
	}
	if err := h.saveSnapRoot(custom, target); err != nil {
		return "", err
	}
	// Сразу новая папка: снимки, что начнутся во время переезда, пишутся уже
	// туда (переезд каждой папки — под её замком снимков).
	h.mu.Lock()
	h.snapRoot, h.snapAt = custom, target
	h.mu.Unlock()
	var moveErr []string
	for _, f := range fs {
		mu := f.snapMu()
		mu.Lock()
		st := f.loadSnap()
		from := h.zipDirAt(old, f, &st)
		to := h.zipDirAt(target, f, &st)
		if err := moveSnaps(from, to, &st); err != nil {
			moveErr = append(moveErr, f.Name+": "+err.Error())
		}
		if len(st.List) > 0 || st.Config.Daily {
			_ = os.MkdirAll(f.snapDir(), 0o700)
			_ = f.saveSnap(st)
		}
		mu.Unlock()
	}
	if len(moveErr) > 0 {
		return target, errors.New("папка снимков сменена, но не все снимки переехали: " + strings.Join(moveErr, "; "))
	}
	return target, nil
}

// moveSnaps переносит архивы списка из from в to (имена — свободные в to).
func moveSnaps(from, to string, st *snapState) error {
	if samePath(from, to) {
		return nil
	}
	var errs []string
	for i, s := range st.List {
		if s.Error != "" || s.File == "" {
			continue
		}
		src := filepath.Join(from, s.File)
		if _, err := os.Stat(src); err != nil {
			continue // убрали руками
		}
		if err := os.MkdirAll(to, 0o700); err != nil {
			return err
		}
		name := s.File
		for n := 2; ; n++ {
			if _, err := os.Lstat(filepath.Join(to, name)); errors.Is(err, os.ErrNotExist) {
				break
			}
			name = fmt.Sprintf("%s (%d).zip", strings.TrimSuffix(s.File, ".zip"), n)
		}
		if err := moveFile(src, filepath.Join(to, name)); err != nil {
			errs = append(errs, s.File+": "+err.Error())
			continue
		}
		st.List[i].File = name
	}
	// Старая подпапка опустела — убрать (snapshots/ в данных не трогаем:
	// там snapshots.json).
	if !strings.EqualFold(filepath.Base(from), "snapshots") {
		_ = os.Remove(from)
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// moveFile — переименование, а между дисками — копия и удаление.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		in.Close()
		return err
	}
	_, err = io.Copy(out, in)
	in.Close()
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}

// within — путь p внутри папки dir (или совпадает с ней).
func within(p, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) && (filepath.Separator == '\\' || filepath.Clean(a) == filepath.Clean(b))
}
