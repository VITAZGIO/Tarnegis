package mirror

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Общая папка снимков устройства (как резервные копии Home Assistant в папке
// контейнера): <папка>/<имя раздаваемой папки>/<дата>.zip. Не задана —
// снимки лежат рядом с данными папки (snapshots/ в данных программы).
// Служебный список снимков (snapshots.json) всегда остаётся в данных.

const snapRootFile = "snaproot.json"

type snapRootCfg struct {
	Dir string `json:"dir"`
}

func (h *Hub) loadSnapRoot() string {
	var c snapRootCfg
	_ = readJSON(filepath.Join(h.dir, snapRootFile), &c)
	return c.Dir
}

// SnapRoot — общая папка снимков устройства ("" — рядом с данными папки).
func (h *Hub) SnapRoot() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapRoot
}

// SuggestSnapRoot — что предложить человеку: «backups» рядом с папкой, в
// которой лежат раздаваемые папки (…/ssh_tunnel/mirrors/Server →
// …/ssh_tunnel/backups). Раздаваемых папок нет — "".
func (h *Hub) SuggestSnapRoot() string {
	for _, f := range h.Folders() {
		parent := filepath.Dir(filepath.Clean(f.Path))
		base := filepath.Dir(parent)
		if filepath.Dir(base) == base { // parent — сразу под корнем диска
			return filepath.Join(parent, "backups")
		}
		return filepath.Join(base, "backups")
	}
	return ""
}

// SetSnapRoot — сменить общую папку снимков ("" — обратно в данные
// программы). Уже сделанные снимки всех папок переезжают туда же. Папка
// снимков не может быть внутри раздаваемой папки: иначе архивы разошлись бы
// по всем устройствам.
func (h *Hub) SetSnapRoot(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir != "" {
		if !filepath.IsAbs(dir) {
			return "", errors.New("папка снимков — полный путь, например /DATA/AppData/ssh_tunnel/backups")
		}
		dir = filepath.Clean(dir)
		for _, f := range h.Folders() {
			if within(dir, f.Path) {
				return "", fmt.Errorf("папка снимков внутри раздаваемой папки «%s» — архивы разошлись бы по всем устройствам", f.Name)
			}
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("папка снимков не создаётся: %w", err)
		}
	}
	h.mu.Lock()
	old := h.snapRoot
	fs := h.list()
	h.mu.Unlock()
	if samePath(old, dir) {
		return dir, nil
	}
	if err := writeJSONAtomic(filepath.Join(h.dir, snapRootFile), snapRootCfg{Dir: dir}); err != nil {
		return "", err
	}
	// Сразу новая папка: снимки, что начнутся во время переезда, пишутся уже
	// туда (переезд каждой папки — под её замком снимков).
	h.mu.Lock()
	h.snapRoot = dir
	h.mu.Unlock()
	var moveErr []string
	for _, f := range fs {
		mu := f.snapMu()
		mu.Lock()
		st := f.loadSnap()
		from := h.zipDirAt(old, f, &st)
		to := h.zipDirAt(dir, f, &st)
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
		return dir, errors.New("папка снимков сменена, но не все снимки переехали: " + strings.Join(moveErr, "; "))
	}
	return dir, nil
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
