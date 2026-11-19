package panel

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Образцы /proc с настоящего Ubuntu (сокращены).
const (
	sampleStat1 = `cpu  1000 0 500 8000 500 0 0 0 0 0
cpu0 500 0 250 4000 250 0 0 0 0 0
cpu1 500 0 250 4000 250 0 0 0 0 0
intr 12345
ctxt 6789
`
	sampleStat2 = `cpu  1300 0 600 8500 600 0 0 0 0 0
cpu0 450 0 300 4400 250 0 0 0 0 0
cpu1 850 0 300 4100 350 0 0 0 0 0
`
	sampleMeminfo = `MemTotal:        4028424 kB
MemFree:          312000 kB
MemAvailable:    1612000 kB
Buffers:          102000 kB
Cached:          1200000 kB
SwapTotal:       2097148 kB
SwapFree:        1797148 kB
`
	sampleNetDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 9999999    100    0    0    0     0          0         0  9999999    100    0    0    0     0       0          0
  eth0: 1000000    800    0    0    0     0          0         0   500000    600    0    0    0     0       0          0
docker0: 777777    10    0    0    0     0          0         0   777777    10    0    0    0     0       0          0
vethab12: 5555     10    0    0    0     0          0         0     5555    10    0    0    0     0       0          0
  wg0:   2000     10    0    0    0     0          0         0     3000    10    0    0    0     0       0          0
`
	sampleMounts = `sysfs /sys sysfs rw,nosuid 0 0
proc /proc proc rw 0 0
/dev/vda1 / ext4 rw,relatime 0 0
tmpfs /run tmpfs rw 0 0
/dev/vda15 /boot/efi vfat rw 0 0
/dev/vda1 /var/lib/docker/overlay ext4 rw 0 0
overlay /var/lib/docker/overlay2/x/merged overlay rw 0 0
/dev/vdb1 /srv/my\040disk xfs rw 0 0
`
)

func TestStatsРазборProcStat(t *testing.T) {
	a1, c1 := parseProcStat(sampleStat1)
	a2, c2 := parseProcStat(sampleStat2)
	if len(c1) != 2 || len(c2) != 2 {
		t.Fatalf("ядер %d/%d", len(c1), len(c2))
	}
	// Всего +1000 тиков, простой (idle+iowait) +600 → занято 400/1000 = 40 %.
	if p := busyPercent(a1, a2); p != 40 {
		t.Fatalf("ЦП %v, ждали 40", p)
	}
	if p := busyPercent(c1[1], c2[1]); p != 66.7 { // +600, простой +200 → 400/600
		t.Fatalf("ядро 1: %v", p)
	}
	if p := busyPercent(a2, a1); p != 0 {
		t.Fatalf("счётчик назад (перезагрузка) — 0, а не %v", p)
	}
}

func TestStatsРазборПамятиСетиДисков(t *testing.T) {
	m := parseMeminfo(sampleMeminfo)
	if m.Total != 4028424*1024 || m.Used != (4028424-1612000)*1024 || m.SwapUsed != 300000*1024 {
		t.Fatalf("память %+v", m)
	}
	old := parseMeminfo("MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 100 kB\nCached: 300 kB\n")
	if old.Used != 500*1024 {
		t.Fatalf("старое ядро без MemAvailable: %+v", old)
	}
	rx, tx := parseNetDev(sampleNetDev)
	if rx != 1002000 || tx != 503000 {
		t.Fatalf("сеть без lo/docker/veth: %d %d", rx, tx)
	}
	d := parseMounts(sampleMounts)
	if len(d) != 3 || d[0].Mount != "/" || d[1].Mount != "/boot/efi" || d[2].Mount != "/srv/my disk" {
		t.Fatalf("диски %+v", d)
	}
	if l := parseLoadavg("0.52 0.41 0.30 1/234 5678\n"); l != [3]float64{0.52, 0.41, 0.30} {
		t.Fatalf("загрузка %v", l)
	}
	if u := parseUptime("123456.78 400000.00\n"); u != 123456 {
		t.Fatalf("время работы %d", u)
	}
	if os := parseOSRelease("NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n"); os != "Ubuntu 24.04.1 LTS" {
		t.Fatalf("система %q", os)
	}
	if v := parseSystemdTime("@1763546400"); v != 1763546400 {
		t.Fatalf("время службы %d", v)
	}
	if v := parseSystemdTime("Wed 2025-11-19 10:00:00 UTC"); v != 1763546400 {
		t.Fatalf("время службы текстом %d", v)
	}
	if parseSystemdTime("n/a") != 0 || parseSystemdTime("") != 0 {
		t.Fatal("пустое время службы")
	}
}

func TestStatsРазборПроцесса(t *testing.T) {
	// Имя с пробелом и скобкой — режем по последней «)».
	p, ok := parsePidStat("1234 (my (odd) proc) S 1 1234 1234 0 -1 4194560 100 0 0 0 250 50 0 0 20 0 3 0 100 123456789 2048 18446744073709551615")
	if !ok || p.pid != 1234 || p.name != "my (odd) proc" || p.ticks != 300 || p.rss != 2048 {
		t.Fatalf("процесс %+v %v", p, ok)
	}
	if _, ok := parsePidStat("garbage"); ok {
		t.Fatal("мусор разобран")
	}
}

// fakeStats — подделка /proc для замеров.
type fakeStats struct {
	files map[string]string
	procs []int
}

func (f *fakeStats) read(name string) (string, error) {
	if v, ok := f.files[name]; ok {
		return v, nil
	}
	return "", errors.New("нет файла")
}
func (f *fakeStats) pids() []int { return f.procs }
func (f *fakeStats) statfs(mount string) (uint64, uint64, bool) {
	return 100 << 30, 25 << 30, true
}

func TestStatsЗамерыИИстория(t *testing.T) {
	src := &fakeStats{procs: []int{1, 2}, files: map[string]string{
		"/proc/stat": sampleStat1, "/proc/meminfo": sampleMeminfo, "/proc/net/dev": sampleNetDev,
		"/proc/mounts": sampleMounts, "/proc/loadavg": "0.5 0.4 0.3 1/2 3",
		"/proc/1/stat": "1 (systemd) S 0 1 1 0 -1 0 0 0 0 0 100 0 0 0 20 0 1 0 1 1000 500 0",
		"/proc/2/stat": "2 (meshd) S 1 2 2 0 -1 0 0 0 0 0 200 0 0 0 20 0 1 0 1 1000 9000 0",
	}}
	s := newStatsSampler(src, 4096)
	t0 := time.Unix(1_700_000_000, 0)
	s.Sample(t0)
	if st := s.Snapshot(t0); st.Ready || len(st.History.CPU) != 0 {
		t.Fatalf("после одного замера ещё не готово: %+v", st)
	}
	src.files["/proc/stat"] = sampleStat2
	src.files["/proc/2/stat"] = "2 (meshd) S 1 2 2 0 -1 0 0 0 0 0 290 0 0 0 20 0 1 0 1 1000 9000 0"
	src.files["/proc/net/dev"] = strings.Replace(sampleNetDev, "eth0: 1000000", "eth0: 1100000", 1)
	s.Sample(t0.Add(10 * time.Second))
	st := s.Snapshot(t0.Add(10 * time.Second))
	if !st.Ready || st.CPU.Percent != 40 || len(st.CPU.Cores) != 2 || st.CPU.Count != 2 {
		t.Fatalf("ЦП %+v", st.CPU)
	}
	if st.Net.RxRate != 10000 {
		t.Fatalf("скорость приёма %v, ждали 10000 байт/с", st.Net.RxRate)
	}
	if len(st.TopCPU) != 1 || st.TopCPU[0].Name != "meshd" || st.TopCPU[0].CPU != 9 { // 90 из 1000 тиков
		t.Fatalf("процессы по ЦП %+v", st.TopCPU)
	}
	if len(st.TopMem) != 2 || st.TopMem[0].Name != "meshd" || st.TopMem[0].RSS != 9000*4096 {
		t.Fatalf("процессы по памяти %+v", st.TopMem)
	}
	if len(st.Disks) != 3 || st.Disks[0].Used != 75<<30 {
		t.Fatalf("диски %+v", st.Disks)
	}
	if len(st.History.CPU) != 1 || st.History.Mem[0] == 0 {
		t.Fatalf("история %+v", st.History)
	}
	// Час — не больше 360 замеров.
	for i := 0; i < statsPoints+20; i++ {
		s.Sample(t0.Add(time.Duration(20+i*10) * time.Second))
	}
	if n := len(s.Snapshot(t0).History.CPU); n != statsPoints {
		t.Fatalf("в истории %d замеров", n)
	}
}

func TestStatsНуженВход(t *testing.T) {
	srv, pass := newTestServer(t)
	srv.WithStats(newStatsSampler(&fakeStats{files: map[string]string{"/proc/stat": sampleStat1}}, 4096))
	h := srv.Handler()
	if rec, _ := doJSON(t, h, http.MethodGet, "/api/server/stats", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("без входа: %d", rec.Code)
	}
	rec, out := doJSON(t, h, http.MethodGet, "/api/server/stats", nil, loggedInCookies(t, h, pass))
	if rec.Code != http.StatusOK || out["step"] != float64(10) || out["services"] == nil {
		t.Fatalf("со входом: %d %v", rec.Code, out)
	}
}

func TestВидПанелиСохраняетсяНаСервере(t *testing.T) {
	srv, pass := newTestServer(t)
	st, err := OpenSettings(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv.WithSettings(st)
	h := srv.Handler()
	if rec, _ := doJSON(t, h, http.MethodGet, "/api/settings/look", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("без входа: %d", rec.Code)
	}
	ck := loggedInCookies(t, h, pass)
	if rec, _ := doJSON(t, h, http.MethodPost, "/api/settings/look", map[string]string{"theme": "neon", "bg": ""}, ck); rec.Code != http.StatusBadRequest {
		t.Fatalf("чужая тема: %d", rec.Code)
	}
	if rec, _ := doJSON(t, h, http.MethodPost, "/api/settings/look", map[string]string{"theme": "console", "bg": "code"}, ck); rec.Code != http.StatusOK {
		t.Fatalf("сохранение: %d", rec.Code)
	}
	// Другой браузер того же пользователя — новый вход (пароль уже сменён).
	rec, _ := doJSON(t, h, http.MethodPost, "/api/login", map[string]string{"username": "admin", "password": "brandnewpass1"}, nil)
	_, out := doJSON(t, h, http.MethodGet, "/api/settings/look", nil, rec.Result().Cookies())
	if out["theme"] != "console" || out["bg"] != "code" {
		t.Fatalf("в другом браузере: %v", out)
	}
	st2, _ := OpenSettings(st.path)
	if len(st2.Get().Looks) != 1 {
		t.Fatalf("на диске: %+v", st2.Get().Looks)
	}
}

func TestDemoМониторинг(t *testing.T) {
	h := NewDemoHandler("v9.9.9")
	_, out := demoCall(t, h, http.MethodGet, "/api/server/stats", nil)
	hist := out["history"].(map[string]any)
	if out["ready"] != true || len(hist["cpu"].([]any)) != statsPoints || len(out["disks"].([]any)) < 2 ||
		len(out["services"].([]any)) != 4 || len(out["topCpu"].([]any)) != 5 {
		t.Fatalf("показ «Мониторинга»: %v", out)
	}
	if code, _ := demoCall(t, h, http.MethodPost, "/api/settings/look", map[string]string{"theme": "light", "bg": "author"}); code != 200 {
		t.Fatalf("вид в показе: %d", code)
	}
	if _, out := demoCall(t, h, http.MethodGet, "/api/settings/look", nil); out["theme"] != "light" {
		t.Fatalf("вид в показе не запомнился: %v", out)
	}
	// «Обновить meshd» при свежем meshd — «уже последняя» (п. 8).
	if _, out := demoCall(t, h, http.MethodPost, "/api/mesh/reinstall", map[string]any{}); out["latest"] != true {
		t.Fatalf("обновление свежего meshd: %v", out)
	}
	_, st := demoCall(t, h, http.MethodGet, "/api/mesh/state", nil)
	if st["version"] != meshdVersion || st["release"] != "v9.9.9" {
		t.Fatalf("версия meshd в показе: %v %v", st["version"], st["release"])
	}
}

func TestMeshdТотЖеРелиз(t *testing.T) {
	for _, c := range []struct {
		have, rel, panel string
		want             bool
	}{
		{"7", "v1.10.5", "v1.10.5", true},
		{"7", "v1.10.4", "v1.10.5", false},
		{"7", "", "v1.10.5", false}, // собран руками — не знаем, ставим
		{"6", "v1.10.5", "v1.10.5", false},
	} {
		if got := meshdSame(c.have, c.rel, c.panel); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}
