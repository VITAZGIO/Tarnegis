package panel

// «Обновить на всех серверах» в режиме показа (ТЗ 31): выдуманный новый
// релиз и выдуманный ход — Лондон обновляется по шагам, Амстердам не на
// связи («обновится, когда вернётся»), этот сервер — последним. Ничего не
// качается и не ставится.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"sshtunnel/internal/updater"
)

// demoUpd — идущее «обновить на всех» в показе.
type demoUpd struct {
	target string
	start  time.Time
}

// demoNextVersion — «1.10.6» → «1.10.7»; не номер — «1.99.0».
func demoNextVersion(v string) string {
	v = cleanVersion(v)
	parts := strings.Split(strings.SplitN(v, "-", 2)[0], ".")
	if v == "" || len(parts) < 2 {
		return "1.99.0"
	}
	last, _ := strconv.Atoi(parts[len(parts)-1])
	parts[len(parts)-1] = strconv.Itoa(last + 1)
	return strings.Join(parts, ".")
}

// demoStage — шаг сервера через sec секунд после начала его обновления.
func demoStage(sec float64) string {
	switch {
	case sec < 0:
		return updWaiting
	case sec < 4:
		return "download"
	case sec < 5:
		return "verify"
	case sec < 6:
		return "install"
	case sec < 10:
		return "restart"
	case sec < 14:
		return updMeshd
	}
	return updDone
}

func (d *demoBackend) updateAllState() UpdAll {
	if d.srvnet.others == nil && !d.srvnet.alone {
		d.srvnet.others = demoOthers()
	}
	target := d.upd.target
	out := UpdAll{Target: target, InNet: !d.srvnet.alone, Servers: []UpdSrv{}}
	cur := strings.TrimPrefix(d.version, "v")
	sec := time.Since(d.upd.start).Seconds()
	self := UpdSrv{ID: "main", Name: d.servers[0].Name, Self: true, Online: true, Panel: cur, Meshd: meshdVersion,
		MeshdRel: d.version, Stage: "idle"}
	others := []UpdSrv{}
	if !d.srvnet.alone {
		for _, o := range d.srvnet.others {
			others = append(others, UpdSrv{ID: o.ID, Name: o.Name, Online: o.Online, Panel: cur, Meshd: meshdVersion,
				MeshdRel: d.version, Stage: "idle"})
		}
	}
	if target != "" {
		lastOther, k := 0.0, 0
		for i := range others {
			o := &others[i]
			if !o.Online {
				o.Stage = "offline"
				continue
			}
			at := float64(k) * 2
			k++
			o.Stage = demoStage(sec - at)
			if at+14 > lastOther {
				lastOther = at + 14
			}
			if o.Stage == updDone || o.Stage == updMeshd {
				o.Panel = target
			}
			if o.Stage == updDone {
				o.MeshdRel = "v" + target
			}
		}
		self.Stage = demoStage(sec - lastOther)
		if self.Stage == updDone {
			self.Panel, self.MeshdRel = target, "v"+target
		}
	} else {
		for i := range others {
			if !others[i].Online {
				others[i].Stage = "offline"
			}
		}
	}
	out.Servers = append(append(out.Servers, self), others...)
	return out
}

func (d *demoBackend) updateRoutes(h func(string, func(http.ResponseWriter, *http.Request))) {
	// «Проверить обновления»: в показе всегда «вышла новая версия» — чтобы
	// было видно обе кнопки.
	h("/api/update", func(w http.ResponseWriter, r *http.Request) {
		next := demoNextVersion(d.version)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": updater.Result{Installed: d.version,
			Latest: next, Newer: true, URL: "https://github.com/VITAZGIO/Tarnegis/releases"}})
	})
	h("/api/update/all", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, d.updateAllState())
			return
		}
		var req struct{}
		if !decodePost(w, r, &req) {
			return
		}
		if d.srvnet.alone {
			replyErr(w, textErr("этот сервер не в сети серверов — обнови его кнопкой «Обновить»"), nil)
			return
		}
		d.upd = demoUpd{target: demoNextVersion(d.version), start: time.Now()}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target": d.upd.target})
	})
}
