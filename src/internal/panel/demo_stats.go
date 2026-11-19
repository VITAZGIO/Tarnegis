package panel

import (
	"math"
	"time"
)

// Выдуманные показания «Мониторинга» для режима показа (ТЗ 25): история за
// час заполнена сразу, дальше значения плавно гуляют. Один диск почти
// полный — чтобы были видны жёлтый и красный цвета.

type demoStats struct {
	n       int // номер следующего замера (для плавной волны)
	last    time.Time
	cpu     []float64
	mem     []float64
	rx, tx  []float64
	rxTotal float64
	txTotal float64
}

// demoWave — плавное значение около base с размахом amp.
func demoWave(i int, base, amp, period, phase float64) float64 {
	return base + amp*math.Sin(float64(i)/period+phase) + amp*0.35*math.Sin(float64(i)/(period*0.27)+phase*2)
}

func (d *demoBackend) serverStats(now time.Time) ServerStats {
	if d.stats == nil {
		ds := &demoStats{last: now, rxTotal: 812e9, txTotal: 1.37e12}
		for i := 0; i < statsPoints; i++ {
			ds.push(d)
		}
		d.stats = ds
	}
	ds := d.stats
	for n := 0; now.Sub(ds.last) >= statsStep && n < statsPoints; n++ {
		ds.last = ds.last.Add(statsStep)
		ds.push(d)
	}
	last := len(ds.cpu) - 1
	cores := []float64{}
	for c := 0; c < 4; c++ {
		cores = append(cores, round1(math.Max(1, math.Min(99, ds.cpu[last]+float64(c*9-12)+d.rnd.Float64()*6))))
	}
	const gb = 1 << 30
	st := ServerStats{
		Time: now.Unix(), Step: int(statsStep / time.Second), Ready: true, Demo: true,
		Host: "frankfurt", OS: "Ubuntu 24.04.1 LTS", Kernel: "6.8.0-49-generic",
		Uptime: int64(time.Since(d.started).Seconds()) + 12*86400 + 3*3600,
		CPU:    StatsCPU{Count: 4, Percent: ds.cpu[last], Cores: cores, Load: [3]float64{round1(ds.cpu[last] / 25), 0.84, 0.66}},
		Mem:    StatsMem{Total: 4 * gb, Used: uint64(ds.mem[last] / 100 * 4 * gb), SwapTotal: 2 * gb, SwapUsed: 310 << 20},
		Disks: []StatsDisk{
			{Mount: "/", FS: "ext4", Total: 80 * gb, Used: 62 * gb},
			{Mount: "/boot", FS: "ext4", Total: 1 * gb, Used: 270 << 20},
			{Mount: "/srv/backups", FS: "xfs", Total: 200 * gb, Used: 187 * gb},
		},
		Net: StatsNet{RxRate: ds.rx[last], TxRate: ds.tx[last], RxTotal: uint64(ds.rxTotal), TxTotal: uint64(ds.txTotal)},
		TopCPU: []StatsProc{
			{PID: 812, Name: "meshd", CPU: round1(ds.cpu[last] * 0.35), RSS: 38 << 20},
			{PID: 640, Name: "sshd", CPU: round1(ds.cpu[last] * 0.22), RSS: 9 << 20},
			{PID: 1290, Name: "ssh_tunnel_pane", CPU: round1(ds.cpu[last] * 0.08), RSS: 31 << 20},
			{PID: 1502, Name: "udprelay", CPU: round1(ds.cpu[last] * 0.05), RSS: 7 << 20},
			{PID: 931, Name: "dockerd", CPU: round1(ds.cpu[last] * 0.03), RSS: 96 << 20},
		},
		TopMem: []StatsProc{
			{PID: 2210, Name: "java", RSS: 1210 << 20},
			{PID: 931, Name: "dockerd", RSS: 96 << 20},
			{PID: 3301, Name: "nginx", RSS: 64 << 20},
			{PID: 812, Name: "meshd", RSS: 38 << 20},
			{PID: 1290, Name: "ssh_tunnel_pane", RSS: 31 << 20},
		},
		Services: []StatsService{
			{ID: "panel", Unit: "ssh_tunnel_panel.service", Installed: true, Active: true, Version: d.version, Since: d.started.Unix()},
			{ID: "meshd", Unit: meshdUnit, Installed: true, Active: true, Version: meshdVersion, Release: d.version, Since: d.meshdStarted},
			{ID: "udprelay", Unit: "udprelay.service", Installed: true, Active: true, Since: d.started.Add(-5 * 24 * time.Hour).Unix()},
			{ID: "node", Unit: "ssh_tunnel_node.service", Installed: d.nodeInstalled, Active: d.nodeInstalled, Version: d.version},
		},
	}
	if d.oldMeshd {
		st.Services[1].Version, st.Services[1].Release = "1.6.0 (старая)", ""
	}
	st.History = StatsHistory{CPU: append([]float64{}, ds.cpu...), Mem: append([]float64{}, ds.mem...),
		Rx: append([]float64{}, ds.rx...), Tx: append([]float64{}, ds.tx...)}
	return st
}

// push добавляет замер i и держит час.
func (ds *demoStats) push(d *demoBackend) {
	i := ds.n
	ds.n++
	j := d.rnd.Float64()
	ds.cpu = append(ds.cpu, round1(math.Max(2, demoWave(i, 24, 14, 23, 0.4)+j*2)))
	ds.mem = append(ds.mem, round1(demoWave(i, 57, 4, 61, 1.3)+j))
	rx := math.Max(2e3, demoWave(i, 2.4e6, 1.9e6, 17, 2.1)+j*4e5)
	tx := math.Max(2e3, demoWave(i, 0.9e6, 0.7e6, 13, 0.2)+j*2e5)
	ds.rx, ds.tx = append(ds.rx, round1(rx)), append(ds.tx, round1(tx))
	ds.rxTotal += rx * statsStep.Seconds()
	ds.txTotal += tx * statsStep.Seconds()
	if len(ds.cpu) > statsPoints {
		ds.cpu, ds.mem = ds.cpu[1:], ds.mem[1:]
		ds.rx, ds.tx = ds.rx[1:], ds.tx[1:]
	}
}
