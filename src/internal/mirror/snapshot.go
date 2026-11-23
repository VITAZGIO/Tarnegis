package mirror

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sshtunnel/internal/updater"
)

// Снимки папки (ТЗ 36), как резервные копии Home Assistant: на устройстве,
// которое раздаёт папку, — zip всей папки раз в ночь (и по кнопке), хранятся
// последние N. Лежат в данных программы рядом с versions/ (никогда не
// зеркалируются), открываются любым архиватором. Восстановление — всей
// папки: разница со снимком применяется обычными изменениями Hub, так что
// устройства получают её обычной сверкой, а их неотправленные правки
// становятся конфликт-копиями.

// Причины снимка.
const (
	SnapAuto   = "auto"   // по расписанию
	SnapManual = "manual" // вручную
	SnapPre    = "pre"    // перед восстановлением
)

const (
	snapDefaultTime = "03:00"
	snapDefaultKeep = 3
	snapMaxKeep     = 30
	snapMaxPre      = 3 // «перед восстановлением» — сверх N, но не больше
	snapMaxErrors   = 3 // неудачные попытки в списке
	snapMetaName    = "snapshot.json"
	snapMetaAlt     = "snapshot.ssh_tunnel.json" // если в корне папки свой snapshot.json
	snapComment     = "ssh_tunnel snapshot; meta="
)

// SnapConfig — расписание снимков папки.
type SnapConfig struct {
	Daily bool   `json:"daily"` // по расписанию (иначе — только вручную)
	Every int    `json:"every"` // раз в сколько дней: 1, 3 или 7
	Time  string `json:"time"`  // «ЧЧ:ММ», местное время устройства
	Keep  int    `json:"keep"`
}

// snapEvery — допустимые «раз в N дней».
func snapEvery(n int) bool { return n == 1 || n == 3 || n == 7 }

// snapDue — пора ли снимок по расписанию: последнее «ЧЧ:ММ» позже, чем
// прошлый снимок по расписанию + (N−1) дней. Пропущенное — один раз.
func snapDue(now time.Time, c SnapConfig, lastRun int64) bool {
	if !c.Daily {
		return false
	}
	slot := lastSlot(now, c.Time)
	return slot.Unix() > time.Unix(lastRun, 0).Add(time.Duration(c.Every-1)*24*time.Hour).Unix()
}

// Snapshot — один снимок (или неудачная попытка — Error).
type Snapshot struct {
	File   string `json:"file,omitempty"` // имя zip в папке снимков
	Time   int64  `json:"time"`           // unix
	Size   int64  `json:"size"`
	Files  int    `json:"files"`
	Reason string `json:"reason"`
	Seq    int64  `json:"seq,omitempty"` // номер изменения Hub
	Error  string `json:"error,omitempty"`
	// Folder — имя папки (только для скачивания: имя файла «<папка> <дата>»).
	Folder string `json:"-"`
}

// SnapList — всё о снимках папки для страницы.
type SnapList struct {
	Config    SnapConfig `json:"config"`
	Dir       string     `json:"dir"`     // где лежат (показать человеку: «Лежат в: …»)
	Total     int64      `json:"total"`   // сколько занимают все снимки
	LastRun   int64      `json:"lastRun"` // последний запуск по расписанию (unix)
	Snapshots []Snapshot `json:"snapshots"`
}

// RestoreResult — итог восстановления.
type RestoreResult struct {
	Changed int    `json:"changed"`
	Deleted int    `json:"deleted"`
	Added   int    `json:"added"`
	Pre     string `json:"pre,omitempty"` // снимок «перед восстановлением»
	Text    string `json:"text"`
}

// snapMeta — snapshot.json внутри архива.
type snapMeta struct {
	Folder  string     `json:"folder"`
	ID      string     `json:"id"`
	Time    string     `json:"time"`
	Unix    int64      `json:"unix"`
	Seq     int64      `json:"seq"`
	Epoch   string     `json:"epoch"`
	Reason  string     `json:"reason"`
	Version string     `json:"version"`
	Files   []snapFile `json:"files"`
	Dirs    []string   `json:"dirs"`
}

type snapFile struct {
	Path  string `json:"path"`
	Hash  string `json:"hash"`
	Size  int64  `json:"size"`
	Ver   int64  `json:"ver"`
	MTime int64  `json:"mtime,omitempty"`
}

// snapState — snapshots.json в папке снимков.
type snapState struct {
	Config  SnapConfig `json:"config"`
	// Dir — подпапка этой папки в общей папке снимков устройства (SnapRoot);
	// запоминается при первом снимке, чтобы переименование папки не теряло
	// старые архивы.
	Dir     string     `json:"dir,omitempty"`
	LastRun int64      `json:"lastRun"`
	List    []Snapshot `json:"list"`
}

// snapMu — снимок, восстановление и настройка папки не идут одновременно
// (индекс папки при этом под своим f.mu).
func (f *hfolder) snapMu() *sync.Mutex { return &f.snap }

// snapDir — служебные данные снимков (snapshots.json); архивы — в zipDir.
func (f *hfolder) snapDir() string { return filepath.Join(f.dir, "snapshots") }

// zipDir — где лежат архивы снимков папки. Папка снимков устройства не
// задана — рядом с данными (snapshots/), как в ТЗ 36. Задана (как резервные
// копии Home Assistant в папке контейнера) — <папка снимков>/<имя папки>/.
// Пустой st.Dir заполняется (сохранит вызывающий вместе с остальным).
func (h *Hub) zipDir(f *hfolder, st *snapState) string {
	return h.zipDirAt(h.SnapRoot(), f, st)
}

func (h *Hub) zipDirAt(root string, f *hfolder, st *snapState) string {
	if root == "" {
		return f.snapDir()
	}
	if st.Dir == "" {
		st.Dir = h.snapSubName(f)
	}
	return filepath.Join(root, st.Dir)
}

// snapSubName — имя подпапки снимков: имя папки безопасными символами; если
// так же называется другая раздаваемая папка — с началом id.
func (h *Hub) snapSubName(f *hfolder) string {
	name := safeFileName(f.Name)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, o := range h.folders {
		if o != f && strings.EqualFold(safeFileName(o.Name), name) {
			id := f.ID
			if len(id) > 6 {
				id = id[:6]
			}
			return name + " " + id
		}
	}
	return name
}

// safeFileName — имя без символов, запрещённых в именах файлов.
func safeFileName(name string) string {
	safe := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	safe = strings.Trim(safe, ". ")
	if safe == "" {
		safe = "папка"
	}
	return safe
}

func (f *hfolder) loadSnap() snapState {
	var st snapState
	_ = readJSON(filepath.Join(f.snapDir(), "snapshots.json"), &st)
	st.Config = normSnapConfig(st.Config)
	return st
}

func (f *hfolder) saveSnap(st snapState) error {
	return writeJSONAtomic(filepath.Join(f.snapDir(), "snapshots.json"), st)
}

func normSnapConfig(c SnapConfig) SnapConfig {
	if _, _, ok := parseHM(c.Time); !ok {
		c.Time = snapDefaultTime
	}
	if c.Keep <= 0 {
		c.Keep = snapDefaultKeep
	}
	if c.Keep > snapMaxKeep {
		c.Keep = snapMaxKeep
	}
	if !snapEvery(c.Every) {
		c.Every = 1
	}
	return c
}

func parseHM(s string) (int, int, bool) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, 0, false
	}
	hh, err1 := strconv.Atoi(a)
	mm, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, 0, false
	}
	return hh, mm, true
}

// lastSlot — последнее наступившее время расписания (сегодня в hh:mm или
// вчера, если сегодняшнее ещё впереди), по местным часам now.
func lastSlot(now time.Time, hm string) time.Time {
	hh, mm, _ := parseHM(hm)
	t := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if t.After(now) {
		t = t.AddDate(0, 0, -1)
	}
	return t
}

// SetSnapConfig — включить или выключить «Каждую ночь», время, сколько
// хранить. Время — «ЧЧ:ММ», хранить — 1…30.
func (h *Hub) SetSnapConfig(id string, c SnapConfig) (SnapConfig, error) {
	f, err := h.folder(id)
	if err != nil {
		return SnapConfig{}, err
	}
	if c.Time == "" {
		c.Time = snapDefaultTime
	}
	if _, _, ok := parseHM(c.Time); !ok {
		return SnapConfig{}, errors.New("время — ЧЧ:ММ, например 03:00")
	}
	if c.Keep == 0 {
		c.Keep = snapDefaultKeep
	}
	if c.Keep < 1 || c.Keep > snapMaxKeep {
		return SnapConfig{}, fmt.Errorf("хранить — от 1 до %d снимков", snapMaxKeep)
	}
	if c.Every == 0 {
		c.Every = 1
	}
	if !snapEvery(c.Every) {
		return SnapConfig{}, errors.New("как часто — раз в 1, 3 или 7 дней")
	}
	mu := f.snapMu()
	mu.Lock()
	defer mu.Unlock()
	st := f.loadSnap()
	if c.Daily && (!st.Config.Daily || st.Config.Time != c.Time || st.Config.Every != c.Every) {
		// Только что включили (или сдвинули время) — первый снимок в
		// ближайшую ночь, а не «догонять» сразу.
		st.LastRun = h.now().Unix()
	}
	st.Config = c
	if err := os.MkdirAll(f.snapDir(), 0o700); err != nil {
		return SnapConfig{}, err
	}
	st.List = rotateSnaps(h.zipDir(f, &st), st.List, c.Keep)
	return c, f.saveSnap(st)
}

// Snapshots — расписание и список снимков папки, новые сверху.
func (h *Hub) Snapshots(id string) (SnapList, error) {
	f, err := h.folder(id)
	if err != nil {
		return SnapList{}, err
	}
	mu := f.snapMu()
	mu.Lock()
	defer mu.Unlock()
	st := f.loadSnap()
	zd := h.zipDir(f, &st)
	out := SnapList{Config: st.Config, Dir: zd, LastRun: st.LastRun, Snapshots: []Snapshot{}}
	if abs, err := filepath.Abs(out.Dir); err == nil {
		out.Dir = abs
	}
	for _, s := range st.List {
		if s.Error == "" {
			fi, err := os.Stat(filepath.Join(zd, s.File))
			if err != nil {
				continue // убрали руками
			}
			s.Size = fi.Size()
			out.Total += s.Size
		}
		out.Snapshots = append(out.Snapshots, s)
	}
	sortSnaps(out.Snapshots)
	return out, nil
}

func sortSnaps(l []Snapshot) {
	sort.SliceStable(l, func(i, j int) bool { return l[i].Time > l[j].Time })
}

// OpenSnapshot — архив снимка для скачивания.
func (h *Hub) OpenSnapshot(id, file string) (*os.File, Snapshot, error) {
	f, err := h.folder(id)
	if err != nil {
		return nil, Snapshot{}, err
	}
	mu := f.snapMu()
	mu.Lock()
	st := f.loadSnap()
	s, ok := findSnap(st.List, file)
	zd := h.zipDir(f, &st)
	mu.Unlock()
	if !ok {
		return nil, Snapshot{}, errors.New("такого снимка нет")
	}
	s.Folder = f.Name
	fh, err := os.Open(filepath.Join(zd, s.File))
	if err != nil {
		return nil, Snapshot{}, errors.New("такого снимка нет")
	}
	return fh, s, nil
}

func findSnap(list []Snapshot, file string) (Snapshot, bool) {
	if file == "" || strings.ContainsAny(file, `/\`) || file == "." || file == ".." {
		return Snapshot{}, false
	}
	for _, s := range list {
		if s.Error == "" && s.File == file {
			return s, true
		}
	}
	return Snapshot{}, false
}

// SnapNow — снимок сейчас (reason: manual или auto).
func (h *Hub) SnapNow(id, reason string) (Snapshot, error) {
	f, err := h.folder(id)
	if err != nil {
		return Snapshot{}, err
	}
	mu := f.snapMu()
	mu.Lock()
	defer mu.Unlock()
	return h.snapshotLocked(f, reason)
}

// SnapTick — снимки по расписанию, которые пора сделать. Пропущенная ночь
// (устройство было выключено) делается при следующем запуске — один раз,
// сколько бы ночей ни пропало. Зовётся из круга сверки (раз в 30 с).
func (h *Hub) SnapTick() {
	h.mu.Lock()
	fs := h.list()
	h.mu.Unlock()
	for _, f := range fs {
		mu := f.snapMu()
		mu.Lock()
		st := f.loadSnap()
		now := h.now()
		if snapDue(now, st.Config, st.LastRun) {
			st.LastRun = now.Unix()
			if err := os.MkdirAll(f.snapDir(), 0o700); err == nil && f.saveSnap(st) == nil {
				_, _ = h.snapshotLocked(f, SnapAuto)
			}
		}
		mu.Unlock()
	}
}

// snapshotLocked — сделать снимок и записать его в список (или ошибку —
// тоже в список и в журнал). Старые снимки при нехватке места не
// удаляются: ротация — только после удачного снимка. Замок снимков взят.
func (h *Hub) snapshotLocked(f *hfolder, reason string) (Snapshot, error) {
	switch reason {
	case SnapAuto, SnapManual, SnapPre:
	default:
		reason = SnapManual
	}
	if err := os.MkdirAll(f.snapDir(), 0o700); err != nil {
		return Snapshot{}, err
	}
	st := f.loadSnap()
	f.zdir = h.zipDir(f, &st)
	if err := os.MkdirAll(f.zdir, 0o700); err != nil {
		return Snapshot{}, fmt.Errorf("папка снимков %s: %w", f.zdir, err)
	}
	s, err := h.buildSnap(f, reason)
	if err != nil {
		why := err.Error()
		if diskFull(err) {
			why = "не хватило места на диске — снимок не сделан, старые снимки не тронуты"
		}
		s = Snapshot{Time: h.now().Unix(), Reason: reason, Error: why}
		st.List = append(st.List, s)
		st.List = rotateSnaps(f.zdir, st.List, st.Config.Keep)
		_ = f.saveSnap(st)
		if h.OnEvent != nil {
			go h.OnEvent("snapshot", fmt.Sprintf("Снимок папки «%s» не сделан: %s", f.Name, why))
		}
		return s, errors.New(why)
	}
	st.List = append(st.List, s)
	st.List = rotateSnaps(f.zdir, st.List, st.Config.Keep)
	if err := f.saveSnap(st); err != nil {
		return s, err
	}
	return s, nil
}

// rotateSnaps — оставить N новых «по расписанию» и «вручную», не больше 3
// «перед восстановлением» и 3 последних ошибок; лишние архивы удалить.
func rotateSnaps(dir string, list []Snapshot, keep int) []Snapshot {
	if keep <= 0 {
		keep = snapDefaultKeep
	}
	l := append([]Snapshot(nil), list...)
	sortSnaps(l)
	var out []Snapshot
	reg, pre, bad := 0, 0, 0
	for _, s := range l {
		var ok bool
		switch {
		case s.Error != "":
			bad++
			ok = bad <= snapMaxErrors
		case s.Reason == SnapPre:
			pre++
			ok = pre <= snapMaxPre
		default:
			reg++
			ok = reg <= keep
		}
		if ok {
			out = append(out, s)
		} else if s.Error == "" && s.File != "" {
			os.Remove(filepath.Join(dir, s.File))
		}
	}
	// Старые записи снизу, как дописывались.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}

// snapFileName — «<имя папки> ГГГГ-ММ-ДД ЧЧ-ММ.zip», только безопасные
// символы; занято — « (2)», « (3)»…
func snapFileName(dir, name string, t time.Time) string {
	base := t.Format("2006-01-02 15-04")
	if name != "" {
		base = safeFileName(name) + " " + base
	}
	for i := 1; ; i++ {
		n := base + ".zip"
		if i > 1 {
			n = fmt.Sprintf("%s (%d).zip", base, i)
		}
		if _, err := os.Lstat(filepath.Join(dir, n)); errors.Is(err, os.ErrNotExist) {
			return n
		}
	}
}

// buildSnap — снимок из состояния Hub. Список файлов и хеши — под замком
// папки (один номер изменения), содержимое копируется уже без него: из
// файлов папки, а если файл успел поменяться — из versions/ (туда уходит
// всё заменённое). Не нашлось нигде — снимок заново; последняя попытка
// держит замок до конца (загрузки устройств при этом просто ждут).
func (h *Hub) buildSnap(f *hfolder, reason string) (Snapshot, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		hold := attempt == 2
		f.mu.Lock()
		seq := f.ix.Seq
		err := h.trackLocked(f, func() error {
			disk, e := scanDir(f.Path, f.ig)
			if e != nil {
				return e
			}
			return h.scanLocked(f, disk)
		})
		if err != nil {
			f.mu.Unlock()
			return Snapshot{}, err
		}
		grew := f.ix.Seq != seq
		meta, blobs := h.snapMetaLocked(f, reason)
		if !hold {
			f.mu.Unlock()
		}
		if grew {
			h.changed()
		}
		s, err := h.writeSnap(f, meta, blobs)
		if hold {
			f.mu.Unlock()
		}
		if errors.Is(err, errBusy) {
			last = err
			continue
		}
		return s, err
	}
	return Snapshot{}, fmt.Errorf("файлы папки всё время меняются: %w", last)
}

// snapMetaLocked — что войдёт в снимок: живые не исключённые файлы и папки
// индекса. blobs — где лежит старое содержимое по хешу. f.mu взят.
func (h *Hub) snapMetaLocked(f *hfolder, reason string) (snapMeta, map[string]string) {
	now := h.now()
	m := snapMeta{Folder: f.Name, ID: f.ID, Time: now.Format(time.RFC3339), Unix: now.Unix(),
		Seq: f.ix.Seq, Epoch: f.ix.Epoch, Reason: reason, Version: updater.Version,
		Files: []snapFile{}, Dirs: []string{}}
	for p, fi := range f.ix.Files {
		if fi.Deleted || f.ig.Match(strings.TrimSuffix(p, "/"), isDir(p)) {
			continue
		}
		if isDir(p) {
			m.Dirs = append(m.Dirs, p)
			continue
		}
		m.Files = append(m.Files, snapFile{Path: p, Hash: fi.Hash, Size: fi.Size, Ver: fi.Ver, MTime: fi.MTime})
	}
	sort.Strings(m.Dirs)
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	blobs := map[string]string{}
	for _, v := range f.ix.Versions {
		blobs[v.Hash] = v.Blob
	}
	return m, blobs
}

// snapCap — для тестов: «диск» снимков вмещает столько байт (0 — без
// предела).
var snapCap int64

type capWriter struct {
	w    io.Writer
	left int64
}

func (c *capWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		n, _ := c.w.Write(p[:c.left])
		c.left = 0
		return n, errNoSpace
	}
	n, err := c.w.Write(p)
	c.left -= int64(n)
	return n, err
}

// writeSnap пишет архив во временный файл и переименовывает его в готовый.
func (h *Hub) writeSnap(f *hfolder, meta snapMeta, blobs map[string]string) (Snapshot, error) {
	dir := f.zdir
	tmp := filepath.Join(dir, tmpPrefix+randHex(6)+".zip.part")
	work := filepath.Join(dir, tmpPrefix+randHex(6)+".work")
	defer os.Remove(work)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Snapshot{}, err
	}
	ok := false
	defer func() {
		if !ok {
			out.Close()
			os.Remove(tmp)
		}
	}()
	var w io.Writer = out
	if snapCap > 0 {
		w = &capWriter{w: out, left: snapCap}
	}
	zw := zip.NewWriter(w)
	metaName := snapMetaName
	for _, x := range meta.Files {
		if strings.EqualFold(x.Path, snapMetaName) {
			metaName = snapMetaAlt
		}
	}
	if err := zw.SetComment(snapComment + metaName); err != nil {
		return Snapshot{}, err
	}
	mb, _ := json.MarshalIndent(meta, "", " ")
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: metaName, Method: zip.Deflate, Modified: time.Unix(meta.Unix, 0)})
	if err != nil {
		return Snapshot{}, err
	}
	if _, err := mw.Write(mb); err != nil {
		return Snapshot{}, err
	}
	for _, d := range meta.Dirs {
		if _, err := zw.CreateHeader(&zip.FileHeader{Name: d, Method: zip.Store, Modified: time.Unix(meta.Unix, 0)}); err != nil {
			return Snapshot{}, err
		}
	}
	for _, x := range meta.Files {
		if err := snapCopy(f, x, blobs, work); err != nil {
			return Snapshot{}, err
		}
		hdr := &zip.FileHeader{Name: x.Path, Method: zip.Deflate}
		if x.MTime > 0 {
			hdr.Modified = time.Unix(0, x.MTime)
		} else {
			hdr.Modified = time.Unix(meta.Unix, 0)
		}
		ew, err := zw.CreateHeader(hdr)
		if err != nil {
			return Snapshot{}, err
		}
		in, err := os.Open(work)
		if err != nil {
			return Snapshot{}, err
		}
		_, err = io.Copy(ew, in)
		in.Close()
		if err != nil {
			return Snapshot{}, err
		}
	}
	if err := zw.Close(); err != nil {
		return Snapshot{}, err
	}
	if err := out.Sync(); err != nil {
		return Snapshot{}, err
	}
	if err := out.Close(); err != nil {
		return Snapshot{}, err
	}
	// В своей подпапке общей папки снимков — просто дата: имя папки уже в
	// имени подпапки. Рядом с данными (по умолчанию) — «<папка> <дата>».
	prefix := meta.Folder
	if !samePath(dir, f.snapDir()) {
		prefix = ""
	}
	name := snapFileName(dir, prefix, time.Unix(meta.Unix, 0).In(h.now().Location()))
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return Snapshot{}, err
	}
	ok = true
	fi, err := os.Stat(filepath.Join(dir, name))
	size := int64(0)
	if err == nil {
		size = fi.Size()
	}
	return Snapshot{File: name, Time: meta.Unix, Size: size, Files: len(meta.Files), Reason: meta.Reason, Seq: meta.Seq}, nil
}

// snapCopy — содержимое файла x (ровно с его хешем) во временный work: из
// папки, иначе из versions/. errBusy — нигде нет такого содержимого.
func snapCopy(f *hfolder, x snapFile, blobs map[string]string, work string) error {
	srcs := []string{full(f.Path, x.Path)}
	if b, ok := blobs[x.Hash]; ok {
		srcs = append(srcs, filepath.Join(f.dir, "versions", b))
	}
	for _, src := range srcs {
		got, err := copyShared(src, work)
		if err != nil {
			if diskFull(err) {
				return err
			}
			continue
		}
		if got == x.Hash {
			return nil
		}
	}
	return errBusy
}

// copyShared — как copyFile, но источник открывается не мешая удалению
// (openShared) и без fsync: это рабочая копия на минуту.
func copyShared(src, dst string) (string, error) {
	in, err := openShared(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hs := sha256.New()
	var w io.Writer = out
	if snapCap > 0 {
		w = &capWriter{w: out, left: snapCap}
	}
	_, err = io.Copy(io.MultiWriter(w, hs), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hs.Sum(nil)), nil
}

// readSnapMeta — snapshot.json архива (имя — в комментарии архива).
func readSnapMeta(zr *zip.Reader) (snapMeta, map[string]*zip.File, error) {
	name := snapMetaName
	if n, ok := strings.CutPrefix(zr.Comment, snapComment); ok && n != "" {
		name = n
	}
	byName := map[string]*zip.File{}
	for _, zf := range zr.File {
		byName[zf.Name] = zf
	}
	var m snapMeta
	zf := byName[name]
	if zf == nil {
		return m, nil, errors.New("в архиве нет snapshot.json — это не снимок папки")
	}
	rc, err := zf.Open()
	if err != nil {
		return m, nil, err
	}
	defer rc.Close()
	if err := json.NewDecoder(io.LimitReader(rc, 64<<20)).Decode(&m); err != nil {
		return m, nil, fmt.Errorf("испорчен snapshot.json: %w", err)
	}
	return m, byName, nil
}

// RestoreSnapshot — вернуть всю папку к снимку file. Сначала — снимок
// «перед восстановлением» (без него восстановление не идёт), потом разница
// обычными изменениями: лишнее — в корзину, изменённое — заменить (старое
// в версии), недостающее — добавить; пустые папки — как в снимке.
func (h *Hub) RestoreSnapshot(id, dev, file string) (RestoreResult, error) {
	f, err := h.folder(id)
	if err != nil {
		return RestoreResult{}, err
	}
	mu := f.snapMu()
	mu.Lock()
	defer mu.Unlock()
	rst := f.loadSnap()
	s, ok := findSnap(rst.List, file)
	if !ok {
		return RestoreResult{}, errors.New("такого снимка нет")
	}
	zr, err := zip.OpenReader(filepath.Join(h.zipDir(f, &rst), s.File))
	if err != nil {
		return RestoreResult{}, fmt.Errorf("снимок не открылся: %w", err)
	}
	defer zr.Close()
	meta, byName, err := readSnapMeta(&zr.Reader)
	if err != nil {
		return RestoreResult{}, err
	}
	for _, x := range meta.Files {
		if p, ok := cleanRel(x.Path); !ok || p != x.Path || isDir(p) || byName[x.Path] == nil || len(x.Hash) != 64 {
			return RestoreResult{}, fmt.Errorf("в снимке плохая запись %q", x.Path)
		}
	}
	for _, d := range meta.Dirs {
		if p, ok := cleanRel(d); !ok || p != d || !isDir(p) {
			return RestoreResult{}, fmt.Errorf("в снимке плохая запись %q", d)
		}
	}
	pre, err := h.snapshotLocked(f, SnapPre)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("не удалось сделать снимок перед восстановлением, папка не тронута: %w", err)
	}
	f.mu.Lock()
	var res RestoreResult
	err = h.trackLocked(f, func() error {
		var e error
		res, e = h.restoreSnapLocked(f, dev, meta, byName)
		return e
	})
	f.mu.Unlock()
	go h.changed()
	res.Pre = pre.File
	when := time.Unix(s.Time, 0).In(h.now().Location()).Format("02.01.2006 15:04")
	res.Text = fmt.Sprintf("Папка «%s» возвращена к снимку %s: изменено %d, удалено %d, добавлено %d",
		f.Name, when, res.Changed, res.Deleted, res.Added)
	if err != nil {
		res.Text += " — не до конца: " + err.Error()
		return res, fmt.Errorf("%s", res.Text)
	}
	if h.OnEvent != nil {
		go h.OnEvent("restore", res.Text)
	}
	return res, nil
}

func (h *Hub) restoreSnapLocked(f *hfolder, dev string, meta snapMeta, byName map[string]*zip.File) (RestoreResult, error) {
	var res RestoreResult
	disk, err := scanDir(f.Path, f.ig)
	if err != nil {
		return res, err
	}
	if err := h.scanLocked(f, disk); err != nil {
		return res, err
	}
	want := map[string]snapFile{}
	for _, x := range meta.Files {
		want[x.Path] = x
	}
	wantDir := map[string]bool{}
	for _, d := range meta.Dirs {
		wantDir[d] = true
	}
	// Место: всё, что придётся записать заново.
	var need int64
	for _, x := range meta.Files {
		if cur := f.ix.Files[x.Path]; cur == nil || cur.Deleted || cur.Hash != x.Hash {
			need += x.Size
		}
	}
	if err := h.needLocked(f, need); err != nil {
		return res, err
	}
	// 1. Лишние файлы — в корзину.
	var extraFiles, extraDirs []string
	for p, fi := range f.ix.Files {
		if fi.Deleted || f.ig.Match(strings.TrimSuffix(p, "/"), isDir(p)) {
			continue
		}
		if isDir(p) {
			if !wantDir[p] {
				extraDirs = append(extraDirs, p)
			}
		} else if _, ok := want[p]; !ok {
			extraFiles = append(extraFiles, p)
		}
	}
	sort.Strings(extraFiles)
	var firstErr error
	keepErr := func(e error) {
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}
	for _, p := range extraFiles {
		cur := f.ix.Files[p]
		if err := h.remove(f, p, cur, dev); err != nil {
			keepErr(fmt.Errorf("%s: %w", p, err))
			continue
		}
		res.Deleted++
	}
	// 2. Лишние папки — глубокие сначала.
	sort.Slice(extraDirs, func(i, j int) bool { return depth(extraDirs[i]) > depth(extraDirs[j]) })
	for _, p := range extraDirs {
		if err := h.remove(f, p, f.ix.Files[p], dev); err != nil && !errors.Is(err, errDirBusy) {
			keepErr(fmt.Errorf("%s: %w", p, err))
		}
	}
	// 3. Папки из снимка.
	dirs := append([]string(nil), meta.Dirs...)
	sort.Slice(dirs, func(i, j int) bool { return depth(dirs[i]) < depth(dirs[j]) })
	for _, d := range dirs {
		if cur := f.ix.Files[d]; cur != nil && !cur.Deleted {
			continue
		}
		if err := os.MkdirAll(full(f.Path, d), 0o755); err != nil {
			keepErr(err)
			continue
		}
		keepErr(f.setFile(h, d, FileInfo{Hash: DirHash, By: dev}))
	}
	// 4. Файлы из снимка: другие — заменить, нет — добавить.
	for _, x := range meta.Files {
		cur := f.ix.Files[x.Path]
		if cur != nil && !cur.Deleted && cur.Hash == x.Hash {
			continue
		}
		if err := h.putFromZip(f, dev, x, byName[x.Path], cur); err != nil {
			keepErr(fmt.Errorf("%s: %w", x.Path, err))
			continue
		}
		if cur != nil && !cur.Deleted {
			res.Changed++
		} else {
			res.Added++
		}
	}
	return res, firstErr
}

func depth(p string) int { return strings.Count(strings.TrimSuffix(p, "/"), "/") }

// putFromZip кладёт файл снимка на место (через временный файл рядом,
// старое содержимое — в версии).
func (h *Hub) putFromZip(f *hfolder, dev string, x snapFile, zf *zip.File, cur *FileInfo) error {
	dst := full(f.Path, x.Path)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(dst), tmpPrefix+randHex(6)+".tmp")
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		rc.Close()
		return err
	}
	hs := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, hs), rc)
	rc.Close()
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(hs.Sum(nil)) != x.Hash {
		err = errors.New("содержимое в архиве не совпало с хешем — архив испорчен")
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if x.MTime > 0 {
		t := time.Unix(0, x.MTime)
		_ = os.Chtimes(tmp, t, t)
	}
	if cur != nil && !cur.Deleted {
		if err := h.archive(f, x.Path, cur, dev, false); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	fi, err := os.Lstat(dst)
	if err != nil {
		return err
	}
	st := statOf(fi)
	return f.setFile(h, x.Path, FileInfo{Hash: x.Hash, Size: st.Size, MTime: st.MTime, By: dev})
}

// SaveSnapshot — копия архива снимка в папку dir (свободное имя); путь копии.
func (h *Hub) SaveSnapshot(id, file, dir string) (string, error) {
	f, s, err := h.OpenSnapshot(id, file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := snapDownloadName(s)
	dst := filepath.Join(dir, base)
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}
		dst = filepath.Join(dir, fmt.Sprintf("%s (%d).zip", strings.TrimSuffix(base, ".zip"), i))
	}
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, f)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

// snapDownloadName — имя архива вне папки снимков: в своей подпапке снимок
// назван датой, а скачанному нужно и имя папки.
func snapDownloadName(s Snapshot) string {
	if s.Folder != "" && !strings.HasPrefix(s.File, safeFileName(s.Folder)) {
		return safeFileName(s.Folder) + " " + s.File
	}
	return s.File
}
