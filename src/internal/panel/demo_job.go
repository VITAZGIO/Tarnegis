package panel

import "time"

// demoJob — выдуманная установка в режиме показа (ТЗ 34): строки журнала
// появляются по одной, через несколько секунд установка «заканчивается».
// Нужна, чтобы в показе было видно журнал внутри карточки с кнопкой.
type demoJob struct {
	kind    string
	started time.Time
	lines   []string
}

const demoJobStep = 1200 * time.Millisecond

var demoJobLines = map[string][]string{
	"install":  {"скачиваю meshd", "проверяю контрольную сумму", "ставлю службу ssh_tunnel_meshd", "служба запущена"},
	"udprelay": {"Ставлю ретранслятор UDP", "скачиваю ретранслятор", "ретранслятор UDP запущен (127.0.0.1:47830)"},
	"node":     {"скачиваю хост плагинов", "проверяю контрольную сумму", "ставлю службу ssh_tunnel_node", "хост плагинов запущен"},
}

// start — начать новую установку; false — прошлая ещё идёт.
func (j *demoJob) start(kind string) bool {
	if s := j.snapshot(); s != nil && s.Running {
		return false
	}
	j.kind, j.started, j.lines = kind, time.Now(), demoJobLines[kind]
	return true
}

// snapshot — состояние для страницы, как у настоящего meshJob.
func (j *demoJob) snapshot() *meshJob {
	if j.started.IsZero() {
		return nil
	}
	n := int(time.Since(j.started)/demoJobStep) + 1
	if n > len(j.lines) {
		n = len(j.lines)
	}
	job := &meshJob{Kind: j.kind, Lines: append([]string{}, j.lines[:n]...), Started: j.started}
	end := j.started.Add(time.Duration(len(j.lines)) * demoJobStep)
	if time.Now().Before(end) {
		job.Running = true
	} else {
		job.Finished = end
	}
	return job
}
