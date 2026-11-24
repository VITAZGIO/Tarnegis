package panel

// Обновление сети устройств в режиме показа (ТЗ 40): у устройств разные
// версии и автообновление, «Обновить» доводит устройство до версии сети
// через несколько секунд. Ничего не качается и не ставится.

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// demoDevUpd — устройство, которому в показе нажали «Обновить».
type demoDevUpd struct {
	target string
	at     time.Time
}

// seedNetUpdate — версии и автообновление устройств показа.
func (d *demoBackend) seedNetUpdate() {
	cur := strings.TrimPrefix(d.version, "v")
	if cleanVersion(cur) == "" {
		cur = "1.10.11"
	}
	i := 0
	for _, n := range d.nets {
		for k := range n.Devices {
			x := &n.Devices[k]
			x.Build, x.AutoUpd, x.AutoUpdT = x.Mode, "on", time.Now().Unix()-86400
			switch i % 4 {
			case 0:
				x.App = cur
			case 1:
				x.App = "1.10.4" // отстаёт
			case 2:
				x.App, x.AutoUpd = "1.10.4", "off"
			case 3:
				x.App = cur
			}
			i++
		}
	}
}

// demoNetTarget — версия сети в показе.
func (d *demoBackend) demoNetTarget() string {
	if d.upd.target != "" {
		return d.upd.target
	}
	if v := cleanVersion(strings.TrimPrefix(d.version, "v")); v != "" {
		return v
	}
	return "1.10.11"
}

func (d *demoBackend) demoDevice(netID, id string) *MeshDevice {
	n, _ := d.findNet(netID)
	if n == nil {
		return nil
	}
	for i := range n.Devices {
		if n.Devices[i].ID == id {
			return &n.Devices[i]
		}
	}
	return nil
}

// demoNetDevices — как NetDevices, по устройствам показа.
func (d *demoBackend) demoNetDevices() NetDevs {
	target := d.demoNetTarget()
	out := NetDevs{Target: target, Devices: []NetDev{}}
	for _, n := range d.nets {
		for i := range n.Devices {
			x := &n.Devices[i]
			if u, ok := d.devUpd[n.ID+"/"+x.ID]; ok && time.Since(u.at) > 6*time.Second {
				x.App = u.target
				delete(d.devUpd, n.ID+"/"+x.ID)
			}
			nd := NetDev{Net: n.ID, NetName: n.Name, ID: x.ID, Name: x.Display, Version: x.App,
				Platform: x.Platform, Build: x.Build, Online: x.Online, Here: x.Online,
				LastSeen: x.LastSeen, AutoUpd: x.AutoUpd, UpdSt: x.UpdSt}
			netDevState(&nd, target)
			if _, ok := d.devUpd[n.ID+"/"+x.ID]; ok {
				nd.UpdSt, nd.CanUpdate = "обновляется до "+target, false
			}
			out.Devices = append(out.Devices, nd)
		}
	}
	return out
}

func (d *demoBackend) netUpdateRoutes(h func(string, func(http.ResponseWriter, *http.Request))) {
	if d.devUpd == nil {
		d.devUpd = map[string]demoDevUpd{}
	}
	h("/api/update/devices", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, d.demoNetDevices())
			return
		}
		var req struct{}
		if !decodePost(w, r, &req) {
			return
		}
		sent := 0
		for _, nd := range d.demoNetDevices().Devices {
			if nd.CanUpdate {
				d.devUpd[nd.Net+"/"+nd.ID] = demoDevUpd{target: d.demoNetTarget(), at: time.Now()}
				sent++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": sent})
	})
	h("/api/update/device", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Net    string `json:"net"`
			Device string `json:"device"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		x := d.demoDevice(req.Net, req.Device)
		switch {
		case x == nil:
			replyErr(w, errors.New("нет такого устройства"), nil)
		case !x.Online:
			replyErr(w, errors.New("устройство не на связи"), nil)
		case !versionLess(x.App, d.demoNetTarget()):
			replyErr(w, errors.New("устройство не старше версии сети "+d.demoNetTarget()), nil)
		default:
			d.devUpd[req.Net+"/"+req.Device] = demoDevUpd{target: d.demoNetTarget(), at: time.Now()}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		}
	})
	h("/api/mesh/autoupd", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Net    string `json:"net"`
			Device string `json:"device"`
			On     bool   `json:"on"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		x := d.demoDevice(req.Net, req.Device)
		if x == nil {
			replyErr(w, errors.New("нет такого устройства"), nil)
			return
		}
		x.AutoUpd, x.AutoUpdT = "off", time.Now().Unix()
		if req.On {
			x.AutoUpd = "on"
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}
