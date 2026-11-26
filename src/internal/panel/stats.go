package panel

import (
	"bufio"
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sshtunnel/internal/meshsvc"
	"sshtunnel/internal/nodeapi"
	"sshtunnel/internal/updater"
)

// Показания сервера для раздела «Мониторинг» (ТЗ 25): процессор, память,
// диски, сеть, процессы и службы Tarnegis. Всё читается из /proc и statfs
// самой панелью — без нового процесса и без оболочки. Раз в 10 с снимается
// замер, последний час (360 замеров) лежит в памяти, на диск ничего не
// пишется.

const (
	statsStep   = 10 * time.Second
	statsPoints = 360 // час по 10 с
	statsTop    = 5
)

// ServerStats — ответ GET /api/server/stats.
type ServerStats struct {
	Time   int64  `json:"time"`
	Step   int    `json:"step"` // секунд между замерами
	Ready  bool   `json:"ready"`
	Demo   bool   `json:"demo,omitempty"`
	Host   string `json:"host,omitempty"`
	OS     string `json:"os,omitempty"`
	Kernel string `json:"kernel,omitempty"`
	Uptime int64  `json:"uptimeSeconds"`

	CPU      StatsCPU       `json:"cpu"`
	Mem      StatsMem       `json:"mem"`
	Disks    []StatsDisk    `json:"disks"`
	Net      StatsNet       `json:"net"`
	TopCPU   []StatsProc    `json:"topCpu"`
	TopMem   []StatsProc    `json:"topMem"`
	Services []StatsService `json:"services"`
	History  StatsHistory   `json:"history"`
}

// StatsCPU — загрузка процессора за последний замер, % всей машины и по ядрам.
type StatsCPU struct {
	Count   int        `json:"count"`
	Percent float64    `json:"percent"`
	Cores   []float64  `json:"cores"`
	Load    [3]float64 `json:"load"` // средняя за 1, 5, 15 минут
}

// StatsMem — память в байтах.
type StatsMem struct {
	Total     uint64 `json:"total"`
	Used      uint64 `json:"used"`
	SwapTotal uint64 `json:"swapTotal"`
	SwapUsed  uint64 `json:"swapUsed"`
}

// StatsDisk — одна смонтированная файловая система.
type StatsDisk struct {
	Mount string `json:"mount"`
	FS    string `json:"fs"`
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// StatsNet — сеть: скорость сейчас (байт/с) и сумма с загрузки.
type StatsNet struct {
	RxRate  float64 `json:"rxRate"`
	TxRate  float64 `json:"txRate"`
	RxTotal uint64  `json:"rxTotal"`
	TxTotal uint64  `json:"txTotal"`
}

// StatsProc — процесс в списке «больше всех».
type StatsProc struct {
	PID  int     `json:"pid"`
	Name string  `json:"name"`
	CPU  float64 `json:"cpu"` // % всей машины за последний замер
	RSS  uint64  `json:"rss"` // байт
}

// StatsService — служба Tarnegis на этом сервере.
type StatsService struct {
	ID        string `json:"id"` // panel, meshd, udprelay, node
	Unit      string `json:"unit"`
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Detail    string `json:"detail,omitempty"`
	Version   string `json:"version,omitempty"`
	Release   string `json:"release,omitempty"`
	Built     int64  `json:"built,omitempty"`
	Since     int64  `json:"since,omitempty"` // unix, с какого момента работает
}

// StatsHistory — последний час, от старых к новым: % ЦП, % памяти, сеть
// байт/с.
type StatsHistory struct {
	CPU []float64 `json:"cpu"`
	Mem []float64 `json:"mem"`
	Rx  []float64 `json:"rx"`
	Tx  []float64 `json:"tx"`
}

// ---------- разбор /proc (чистые функции — проверяются на образцах) ----------

// cpuTimes — строка cpu из /proc/stat: всего тиков и из них простоя.
type cpuTimes struct{ total, idle uint64 }

// parseProcStat — общая строка cpu и строки ядер cpuN.
func parseProcStat(text string) (all cpuTimes, cores []cpuTimes) {
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		var t cpuTimes
		for i, v := range f[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			// guest и guest_nice уже входят в user и nice — не считаем дважды.
			if i >= 8 {
				break
			}
			t.total += n
			if i == 3 || i == 4 { // idle, iowait
				t.idle += n
			}
		}
		if f[0] == "cpu" {
			all = t
		} else {
			cores = append(cores, t)
		}
	}
	return all, cores
}

// busyPercent — доля занятости между двумя замерами.
func busyPercent(prev, cur cpuTimes) float64 {
	dt := float64(cur.total) - float64(prev.total)
	if dt <= 0 {
		return 0
	}
	di := float64(cur.idle) - float64(prev.idle)
	p := (dt - di) / dt * 100
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return round1(p)
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// parseMeminfo — MemTotal/MemAvailable/Swap*. Занято = всего − доступно
// (как free -h «used» с кешем, который ядро отдаст по первому требованию).
func parseMeminfo(text string) StatsMem {
	kv := map[string]uint64{}
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseUint(f[0], 10, 64)
		kv[strings.TrimSpace(k)] = n * 1024
	}
	m := StatsMem{Total: kv["MemTotal"], SwapTotal: kv["SwapTotal"]}
	avail, ok := kv["MemAvailable"]
	if !ok { // старое ядро
		avail = kv["MemFree"] + kv["Buffers"] + kv["Cached"]
	}
	if avail < m.Total {
		m.Used = m.Total - avail
	}
	if kv["SwapFree"] < m.SwapTotal {
		m.SwapUsed = m.SwapTotal - kv["SwapFree"]
	}
	return m
}

// parseLoadavg — три средних из /proc/loadavg.
func parseLoadavg(text string) (out [3]float64) {
	f := strings.Fields(text)
	for i := 0; i < 3 && i < len(f); i++ {
		out[i], _ = strconv.ParseFloat(f[i], 64)
	}
	return out
}

// parseUptime — секунды с загрузки из /proc/uptime.
func parseUptime(text string) int64 {
	f := strings.Fields(text)
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return int64(v)
}

// netSkip — интерфейсы, которые не считаем: петля и виртуальные мосты
// контейнеров (их трафик уже посчитан на настоящем интерфейсе).
func netSkip(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range []string{"veth", "docker", "br-", "virbr"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// parseNetDev — принято и отдано всего по /proc/net/dev.
func parseNetDev(text string) (rx, tx uint64) {
	for _, line := range strings.Split(text, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if len(f) < 9 || netSkip(name) {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx
}

// procStat — нужное из /proc/<pid>/stat: имя, тики ЦП, резидентная память
// в страницах.
type procStat struct {
	pid   int
	name  string
	ticks uint64
	rss   uint64
}

// parsePidStat разбирает /proc/<pid>/stat. Имя — в скобках и само может
// содержать пробелы и скобки, поэтому режем по последней «)».
func parsePidStat(text string) (procStat, bool) {
	open := strings.IndexByte(text, '(')
	cls := strings.LastIndexByte(text, ')')
	if open < 0 || cls < open {
		return procStat{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(text[:open]))
	if err != nil {
		return procStat{}, false
	}
	f := strings.Fields(text[cls+1:])
	// После имени: state(0) ppid(1) … utime(11) stime(12) … rss(21).
	if len(f) < 22 {
		return procStat{}, false
	}
	ut, _ := strconv.ParseUint(f[11], 10, 64)
	st, _ := strconv.ParseUint(f[12], 10, 64)
	rss, _ := strconv.ParseUint(f[21], 10, 64)
	return procStat{pid: pid, name: text[open+1 : cls], ticks: ut + st, rss: rss}, true
}

// realFS — файловые системы, которые показываем (настоящие диски, без
// виртуальных и слоёв контейнеров).
var realFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"zfs": true, "f2fs": true, "vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "jfs": true,
	"reiserfs": true, "bcachefs": true}

// parseMounts — точки монтирования настоящих дисков из /proc/mounts, одно
// устройство — один раз (первая, обычно самая короткая точка).
func parseMounts(text string) []StatsDisk {
	var out []StatsDisk
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || !realFS[f[2]] || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		out = append(out, StatsDisk{Mount: unescapeMount(f[1]), FS: f[2]})
	}
	return out
}

// unescapeMount — в /proc/mounts пробел записан как \040 и т. п.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseOSRelease — PRETTY_NAME из /etc/os-release.
func parseOSRelease(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// parseSystemdTime — ActiveEnterTimestamp из systemctl show: «@1700000000»
// (--timestamp=unix) или «Thu 2026-11-19 10:00:00 UTC».
func parseSystemdTime(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" || v == "n/a" {
		return 0
	}
	if n, ok := strings.CutPrefix(v, "@"); ok {
		t, _ := strconv.ParseInt(n, 10, 64)
		return t
	}
	for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05 -0700"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// ---------- замеры ----------

// statsSource — откуда берутся показания: /proc на Linux, в тестах —
// подделка.
type statsSource interface {
	read(name string) (string, error) // файл относительно корня (/proc/stat …)
	pids() []int
	statfs(mount string) (total, free uint64, ok bool)
}

// statsSample — один замер.
type statsSample struct {
	at       time.Time
	cpu      cpuTimes
	cores    []cpuTimes
	rx, tx   uint64
	procs    map[int]procStat
	cpuPct   float64
	corePct  []float64
	memPct   float64
	rxRate   float64
	txRate   float64
	topCPU   []StatsProc
	topMem   []StatsProc
	hasPrior bool
}

// StatsSampler снимает замеры раз в statsStep и хранит последний час.
type StatsSampler struct {
	src      statsSource
	pageSize uint64

	mu      sync.Mutex
	last    *statsSample
	history []statsPoint
	started bool
}

type statsPoint struct{ cpu, mem, rx, tx float64 }

func newStatsSampler(src statsSource, pageSize uint64) *StatsSampler {
	return &StatsSampler{src: src, pageSize: pageSize}
}

// Start запускает фоновые замеры (один раз).
func (s *StatsSampler) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	s.Sample(time.Now())
	go func() {
		t := time.NewTicker(statsStep)
		defer t.Stop()
		for now := range t.C {
			s.Sample(now)
		}
	}()
}

// Sample снимает один замер и считает разницу с предыдущим.
func (s *StatsSampler) Sample(now time.Time) {
	cur := &statsSample{at: now, procs: map[int]procStat{}}
	if txt, err := s.src.read("/proc/stat"); err == nil {
		cur.cpu, cur.cores = parseProcStat(txt)
	}
	if txt, err := s.src.read("/proc/net/dev"); err == nil {
		cur.rx, cur.tx = parseNetDev(txt)
	}
	for _, pid := range s.src.pids() {
		txt, err := s.src.read("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			continue
		}
		if p, ok := parsePidStat(txt); ok {
			cur.procs[pid] = p
		}
	}
	var mem StatsMem
	if txt, err := s.src.read("/proc/meminfo"); err == nil {
		mem = parseMeminfo(txt)
	}
	if mem.Total > 0 {
		cur.memPct = round1(float64(mem.Used) / float64(mem.Total) * 100)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.last
	if prev != nil {
		cur.hasPrior = true
		cur.cpuPct = busyPercent(prev.cpu, cur.cpu)
		for i := range cur.cores {
			if i < len(prev.cores) {
				cur.corePct = append(cur.corePct, busyPercent(prev.cores[i], cur.cores[i]))
			}
		}
		if dt := now.Sub(prev.at).Seconds(); dt > 0 {
			if cur.rx >= prev.rx {
				cur.rxRate = float64(cur.rx-prev.rx) / dt
			}
			if cur.tx >= prev.tx {
				cur.txRate = float64(cur.tx-prev.tx) / dt
			}
		}
		cur.topCPU = topByCPU(prev, cur, s.pageSize)
	}
	cur.topMem = topByMem(cur, s.pageSize)
	s.last = cur
	if cur.hasPrior {
		s.history = append(s.history, statsPoint{cur.cpuPct, cur.memPct, cur.rxRate, cur.txRate})
		if len(s.history) > statsPoints {
			s.history = append([]statsPoint(nil), s.history[len(s.history)-statsPoints:]...)
		}
	}
}

// topByCPU — процессы, больше всех занимавшие ЦП между замерами.
func topByCPU(prev, cur *statsSample, pageSize uint64) []StatsProc {
	dt := float64(cur.cpu.total) - float64(prev.cpu.total)
	if dt <= 0 {
		return []StatsProc{}
	}
	var list []StatsProc
	for pid, p := range cur.procs {
		old, ok := prev.procs[pid]
		if !ok || old.name != p.name || p.ticks < old.ticks {
			continue
		}
		d := float64(p.ticks - old.ticks)
		if d <= 0 {
			continue
		}
		list = append(list, StatsProc{PID: pid, Name: p.name, CPU: round1(d / dt * 100), RSS: p.rss * pageSize})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].CPU != list[j].CPU {
			return list[i].CPU > list[j].CPU
		}
		return list[i].PID < list[j].PID
	})
	if len(list) > statsTop {
		list = list[:statsTop]
	}
	if list == nil {
		list = []StatsProc{}
	}
	return list
}

// topByMem — процессы с наибольшей резидентной памятью.
func topByMem(cur *statsSample, pageSize uint64) []StatsProc {
	list := make([]StatsProc, 0, len(cur.procs))
	for pid, p := range cur.procs {
		if p.rss == 0 {
			continue
		}
		list = append(list, StatsProc{PID: pid, Name: p.name, RSS: p.rss * pageSize})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].RSS != list[j].RSS {
			return list[i].RSS > list[j].RSS
		}
		return list[i].PID < list[j].PID
	})
	if len(list) > statsTop {
		list = list[:statsTop]
	}
	return list
}

// Snapshot — показания для страницы: последний замер плюс то, что дёшево
// прочитать на месте (память, диски, загрузка, время работы, система).
func (s *StatsSampler) Snapshot(now time.Time) ServerStats {
	st := ServerStats{Time: now.Unix(), Step: int(statsStep / time.Second),
		Disks: []StatsDisk{}, TopCPU: []StatsProc{}, TopMem: []StatsProc{}, Services: []StatsService{}}
	src := s.src
	if txt, err := src.read("/proc/meminfo"); err == nil {
		st.Mem = parseMeminfo(txt)
	}
	if txt, err := src.read("/proc/loadavg"); err == nil {
		st.CPU.Load = parseLoadavg(txt)
	}
	if txt, err := src.read("/proc/uptime"); err == nil {
		st.Uptime = parseUptime(txt)
	}
	if txt, err := src.read("/proc/sys/kernel/osrelease"); err == nil {
		st.Kernel = strings.TrimSpace(txt)
	}
	if txt, err := src.read("/proc/sys/kernel/hostname"); err == nil {
		st.Host = strings.TrimSpace(txt)
	}
	if txt, err := src.read("/etc/os-release"); err == nil {
		st.OS = parseOSRelease(txt)
	}
	if txt, err := src.read("/proc/mounts"); err == nil {
		for _, d := range parseMounts(txt) {
			total, free, ok := src.statfs(d.Mount)
			if !ok || total == 0 {
				continue
			}
			d.Total, d.Used = total, total-free
			st.Disks = append(st.Disks, d)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.last; l != nil {
		st.CPU.Count = len(l.cores)
		st.Net.RxTotal, st.Net.TxTotal = l.rx, l.tx
		st.TopMem = l.topMem
		if l.hasPrior {
			st.Ready = true
			st.CPU.Percent = l.cpuPct
			st.CPU.Cores = l.corePct
			st.Net.RxRate, st.Net.TxRate = round1(l.rxRate), round1(l.txRate)
			st.TopCPU = l.topCPU
		}
	}
	if st.CPU.Cores == nil {
		st.CPU.Cores = []float64{}
	}
	h := StatsHistory{CPU: make([]float64, len(s.history)), Mem: make([]float64, len(s.history)),
		Rx: make([]float64, len(s.history)), Tx: make([]float64, len(s.history))}
	for i, p := range s.history {
		h.CPU[i], h.Mem[i], h.Rx[i], h.Tx[i] = p.cpu, p.mem, round1(p.rx), round1(p.tx)
	}
	st.History = h
	return st
}

// ---------- ручка ----------

// WithStats подключает замеры (запуск — StartStats или первый запрос).
func (s *Server) WithStats(st *StatsSampler) *Server {
	s.stats = st
	return s
}

// StartStats начинает замеры сразу при запуске панели: к первому открытию
// «Мониторинга» уже есть история.
func (s *Server) StartStats() {
	if s.stats == nil {
		s.stats = newSystemStats()
	}
	if s.stats != nil {
		s.stats.Start()
	}
}

// handleServerStats — GET /api/server/stats.
func (s *Server) handleServerStats(w http.ResponseWriter, r *http.Request) {
	if s.stats == nil {
		s.stats = newSystemStats()
	}
	if s.stats == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "показания сервера есть только на Linux"})
		return
	}
	s.stats.Start()
	st := s.stats.Snapshot(time.Now())
	st.Services = s.serviceStates(r.Context())
	writeJSON(w, http.StatusOK, st)
}

// serviceStates — службы Tarnegis: работает ли, с какого момента, версия.
func (s *Server) serviceStates(ctx context.Context) []StatsService {
	units := []struct{ id, unit string }{
		{"panel", "ssh_tunnel_panel.service"},
		{"meshd", meshdUnit},
		{"udprelay", meshsvc.UDPRelayUnitName},
		{"node", nodeapi.Unit},
	}
	out := make([]StatsService, 0, len(units))
	for _, u := range units {
		us, since := unitWithSince(u.unit)
		sv := StatsService{ID: u.id, Unit: u.unit, Installed: us.Installed, Active: us.Active, Detail: us.Detail, Since: since}
		if u.id == "panel" {
			// Панель отвечает — значит работает, даже если запущена не
			// службой (руками, в контейнере).
			sv.Active, sv.Version = true, updater.Version
			if sv.Since == 0 {
				sv.Since = s.startedAt.Unix()
			}
		}
		out = append(out, sv)
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if s.mesh != nil && s.mesh.admin != nil && out[1].Active {
		if ms, err := s.mesh.admin.State(c); err == nil {
			out[1].Version, out[1].Release, out[1].Built = ms.Version, ms.Release, ms.Built
		}
	}
	if s.node != nil && out[3].Installed {
		if ns := s.node.Status(c); ns.State != nil {
			out[3].Version = ns.State.Version
		}
	}
	return out
}

// unitWithSince — состояние службы и с какого момента она работает.
func unitWithSince(unit string) (UnitState, int64) {
	out, _ := runSystemctl("show", unit, "--timestamp=unix",
		"--property=LoadState,ActiveState,SubState,UnitFileState,ActiveEnterTimestamp")
	if !strings.Contains(out, "ActiveEnterTimestamp=") {
		// Старый systemd без --timestamp=unix: спрашиваем обычным видом.
		out, _ = runSystemctl("show", unit, "--property=LoadState,ActiveState,SubState,UnitFileState,ActiveEnterTimestamp")
	}
	st := parseUnitState(out)
	var since int64
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "ActiveEnterTimestamp="); ok && st.Active {
			since = parseSystemdTime(v)
		}
	}
	return st, since
}
