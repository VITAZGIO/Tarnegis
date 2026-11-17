package panel

// Режим показа: одна картина сети на всех серверах (ТЗ 24). Три сервера —
// Франкфурт и Лондон на связи, Амстердам без связи с картиной 7-минутной
// давности. У каждого устройства свой набор серверов, «ноутбук» — на двух
// сразу. Картина собирается тем же сложением, что и настоящая
// (mergeNetworks, missingServers): «этот» сервер переключается ручкой
// POST /api/demo/self {"server":"lon"} — и видно, что на Лондоне всё то же.

import (
	"net/http"
	"sort"
	"time"

	"sshtunnel/internal/meshsvc"
)

const (
	demoFra = "f0e1d2c3b4a59687"
	demoLon = "b1d3f5a7c9e2b4d6"
	demoAms = "a2c4e6f8a1b3c5d7"
)

// demoSrv — сервер сети в показе.
type demoSrv struct {
	ID, Name, Host   string
	Port             int
	MeshIP, MeshHost string
	Reach            bool
	Age              int64   // сколько секунд картине (без связи)
	RTT              float64 // задержка связи с Франкфуртом
	NoAcct           string  // чьей учётной записи там нет
}

func demoServerTable() []demoSrv {
	return []demoSrv{
		{ID: demoFra, Name: "Франкфурт", Host: "demo.example", Port: 22, MeshIP: "198.18.0.1", MeshHost: "frankfurt", Reach: true},
		{ID: demoLon, Name: "Лондон", Host: "london.demo.example", Port: 2222, MeshIP: "198.18.0.2", MeshHost: "london",
			Reach: true, RTT: 31, NoAcct: demoUser(6)},
		{ID: demoAms, Name: "Амстердам", Host: "ams.demo.example", Port: 22, MeshIP: "198.18.0.3", MeshHost: "amsterdam",
			Age: 7 * 60},
	}
}

// demoSeedOn — какие устройства на каких серверах.
func (d *demoBackend) demoSeedOn() {
	d.demoSelf = demoFra
	d.srvName = map[string]string{}
	d.devOn = map[string][]string{
		"e1a04f": {demoFra}, "e1a050": {demoFra}, "7c22d9": {demoFra, demoLon}, "3b9e10": {demoLon},
		"b07d33": {demoFra, demoAms}, "55aa01": {demoFra}, "c4f0e2": {demoLon}, "0d0d77": {demoLon},
		"a9e001": {demoFra}, "a9e002": {demoFra}, "ams0n1": {demoAms}, "ams0t2": {demoAms},
	}
}

// demoServers — серверы сети сейчас (убранные кнопкой — нет), с именами из
// панели.
func (d *demoBackend) demoServers() []demoSrv {
	in := map[string]bool{demoFra: true}
	for _, s := range d.srvnet.others {
		in[s.ID] = true
	}
	var out []demoSrv
	for _, s := range demoServerTable() {
		if !in[s.ID] {
			continue
		}
		if n := d.srvName[s.ID]; n != "" {
			s.Name = n
		}
		out = append(out, s)
	}
	return out
}

func (d *demoBackend) demoSrvByID(id string) (demoSrv, bool) {
	for _, s := range d.demoServers() {
		if s.ID == id {
			return s, true
		}
	}
	return demoSrv{}, false
}

func (d *demoBackend) on(dev, srv string) bool {
	for _, s := range d.devOn[dev] {
		if s == srv {
			return true
		}
	}
	return false
}

// demoMerge — картина «этого» сервера (d.demoSelf) плюс картины остальных,
// сложенные как на настоящем сервере.
func (d *demoBackend) demoMerge(st *MeshState) {
	self := d.demoSelf
	servers := d.demoServers()
	if len(servers) < 2 {
		self = demoFra
	}
	me, _ := d.demoSrvByID(self)
	st.Name, st.SelfSrv = me.Name, self
	st.Self = &meshsvc.Self{Name: me.Name, Host: me.MeshHost, IP: me.MeshIP, Enabled: d.self.Enabled, Ports: d.self.Ports, ID: self}
	st.Servers = []MeshServer{{ID: "main", Name: me.Name, Host: me.Host, Main: true, Online: true, Known: true,
		MeshIP: me.MeshIP, MeshHost: me.MeshHost}}

	all := st.Networks
	ref := func(id string) string {
		if id == self {
			return "main"
		}
		return id
	}
	pick := func(srv string, local bool) []MeshNetwork {
		var out []MeshNetwork
		for _, nw := range all {
			c := nw
			c.Devices = nil
			for _, x := range nw.Devices {
				if d.on(x.ID, srv) || len(d.devOn[x.ID]) == 0 && srv == demoFra {
					x.Server = "main"
					c.Devices = append(c.Devices, x)
				}
			}
			if len(c.Devices) > 0 || srv == demoFra {
				out = append(out, c)
			}
		}
		_ = local
		return out
	}
	local := pick(self, true)
	// Задержка устройства до «чужого» сервера — чуть другая, как в жизни.
	var srcs []viewSrc
	users := map[string]string{"meshlink": "meshlink"}
	for _, c := range d.clients {
		users[userHash(c.Username)] = c.Username
	}
	links := map[string]*SrvLinkState{}
	addLink := func(a, b string, online bool, rtt float64, age int64) {
		a, b = ref(a), ref(b)
		if a > b && a != "main" || b == "main" {
			a, b = b, a
		}
		links[a+"|"+b] = &SrvLinkState{A: a, B: b, Online: online, RTTMs: rtt, Age: age}
	}
	var why []whySrv
	accts := func(s demoSrv) map[string]bool {
		m := map[string]bool{}
		for _, c := range d.clients {
			if c.Username != s.NoAcct {
				m[userHash(c.Username)] = true
			}
		}
		return m
	}
	why = append(why, whySrv{ID: "main", Host: me.Host, Port: me.Port, Online: true, Meshd: "7", Accts: accts(me)})
	for _, s := range servers {
		if s.ID == self {
			continue
		}
		live := s.Reach
		age := int64(4)
		if !live {
			age = s.Age + (time.Now().Unix()-d.started.Unix())%60
		}
		v := srvView{From: s.ID, Version: "7", Meshd: true, MeshIP: s.MeshIP, MeshHost: s.MeshHost}
		for _, nw := range pick(s.ID, false) {
			vn := viewNet{ID: nw.ID, Name: nw.Name, Created: nw.Created, EmptySince: nw.EmptySince, Approval: nw.Approval}
			for _, x := range nw.Devices {
				vd := viewDeviceOf(x.Device)
				if vd.RTTMs > 0 {
					vd.RTTMs, vd.RTTAvgMs = vd.RTTMs*1.3+9, vd.RTTAvgMs*1.3+9
				}
				vn.Devices = append(vn.Devices, vd)
			}
			v.Networks = append(v.Networks, vn)
		}
		if s.ID == demoLon {
			v.Relays = d.demoRelays()
		}
		srcs = append(srcs, viewSrc{ID: s.ID, Live: live, Age: age, View: v})
		ms := MeshServer{ID: s.ID, Name: s.Name, Host: s.Host, Known: true, Net: true, Online: live,
			MeshIP: s.MeshIP, MeshHost: s.MeshHost}
		rtt := s.RTT
		if self != demoFra && s.ID == demoFra {
			rtt = me.RTT
		}
		if live {
			ms.RTTMs = rtt
		} else {
			ms.Stale, ms.Age = true, age
		}
		st.Servers = append(st.Servers, ms)
		why = append(why, whySrv{ID: s.ID, Host: s.Host, Port: s.Port, Online: live, Meshd: "7", Accts: accts(s)})
		addLink(self, s.ID, live, rtt, 0)
	}
	// Связь Лондон — Амстердам по картине Лондона.
	if _, ok := d.demoSrvByID(demoAms); ok {
		if _, ok := d.demoSrvByID(demoLon); ok && self != demoAms {
			addLink(demoLon, demoAms, false, 0, 4)
		}
	}
	if self == demoLon {
		st.localRelays = d.demoRelays()
	}
	st.Networks = mergeNetworks(local, srcs, nil, users)
	st.ServerLinks = []SrvLinkState{}
	for _, l := range links {
		st.ServerLinks = append(st.ServerLinks, *l)
	}
	sort.Slice(st.ServerLinks, func(i, j int) bool {
		return st.ServerLinks[i].A+st.ServerLinks[i].B < st.ServerLinks[j].A+st.ServerLinks[j].B
	})
	st.Relays = mergeRelays(st, srcs)
	if len(servers) > 1 {
		for i := range st.Networks {
			for j := range st.Networks[i].Devices {
				x := &st.Networks[i].Devices[j]
				x.Missing = missingServers(*x, why)
			}
		}
	}
}

// demoRelays — телефон Лондона ходит на домашний сервер Франкфурта через
// связку серверов (meshd 7).
func (d *demoBackend) demoRelays() []meshsvc.RelayLine {
	home, _ := d.findNet("9f3c2a71d04e8b65")
	ph, srv := d.device("9f3c2a71d04e8b65", "3b9e10"), d.device("9f3c2a71d04e8b65", "b07d33")
	if home == nil || ph == nil || srv == nil || !ph.Online || !srv.Online {
		return nil
	}
	return []meshsvc.RelayLine{{Net: home.ID, Dir: "out", Dev: ph.ID, To: srv.IP, Port: 22, Srv: demoFra,
		Since: time.Now().Unix() - 300}}
}

// demoViewRoutes — переименование сервера и выбор «этого» сервера.
func (d *demoBackend) demoViewRoutes(h func(string, func(http.ResponseWriter, *http.Request))) {
	h("/api/mesh/servers/rename", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		name := cleanName(req.Name)
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "дай серверу имя"})
			return
		}
		id := req.ID
		if id == "main" || id == "" {
			id = d.demoSelf
		}
		if _, ok := d.demoSrvByID(id); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нет такого сервера в сети"})
			return
		}
		d.srvName[id] = name
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	h("/api/demo/self", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Server string `json:"server"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		id := map[string]string{"fra": demoFra, "lon": demoLon}[req.Server]
		if _, ok := d.demoSrvByID(id); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "fra или lon (и он в сети)"})
			return
		}
		d.demoSelf = id
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "self": id})
	})
}
