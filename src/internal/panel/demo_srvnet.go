package panel

// Режим показа: сеть из трёх серверов (ТЗ 22) с выдуманными данными. Можно
// пройти «Добавить сервер» (код показывается, но никуда не ведёт), убрать
// серверы и, оставшись одним, «Войти в сеть» — серверы вернутся.

import (
	"net/http"
	"strings"
	"time"
)

// demoSrvNet — серверы сети в показе (кроме этого).
type demoSrvNet struct {
	others []SrvState
	alone  bool
}

func demoOthers() []SrvState {
	now := time.Now().Unix()
	return []SrvState{
		{ID: "a2c4e6f8a1b3c5d7", Name: "Амстердам", Host: "ams.demo.example", Port: 22, LastSync: now - 7*60},
		{ID: "b1d3f5a7c9e2b4d6", Name: "Лондон", Host: "london.demo.example", Port: 2222, Online: true, LastSync: now - 4},
	}
}

func (d *demoBackend) srvnetState() SrvNetState {
	if d.srvnet.others == nil && !d.srvnet.alone {
		d.srvnet.others = demoOthers()
	}
	// «Этот» сервер переключается (ТЗ 24, /api/demo/self): список — тот же,
	// со стороны выбранного.
	self := d.demoSelf
	if self == "" || len(d.srvnet.others) == 0 {
		self = demoFra
	}
	st := SrvNetState{SelfID: self, Servers: []SrvState{}}
	now := time.Now().Unix()
	var others []SrvState
	for _, s := range d.demoServers() {
		ss := SrvState{ID: s.ID, Name: s.Name, Host: s.Host, Port: s.Port}
		if s.ID == self {
			ss.Self, ss.Online = true, true
			st.Servers = append(st.Servers, ss)
			continue
		}
		ss.Online = s.Reach
		if s.Reach {
			ss.LastSync, ss.RTTMs = now-4, s.RTT
			if s.ID == demoFra {
				me, _ := d.demoSrvByID(self)
				ss.RTTMs = me.RTT
			}
		} else {
			ss.Error = "нет связи с " + s.Host + ": dial tcp: i/o timeout"
			ss.LastSync = now - s.Age
		}
		others = append(others, ss)
	}
	if len(d.srvnet.others) > 0 {
		st.Net = "9d8c7b6a5f4e3d2c1b0a99887766aabb"
		st.Servers = append(st.Servers, others...)
	}
	return st
}

func (d *demoBackend) srvnetRoutes(h func(string, func(http.ResponseWriter, *http.Request))) {
	ok := map[string]bool{"ok": true}
	h("/api/mesh/servers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, d.srvnetState())
	})
	h("/api/mesh/servers/invite", func(w http.ResponseWriter, r *http.Request) {
		exp := time.Now().Add(inviteTTL)
		code := encodeNetCode(netCode{V: 1, Net: "демо", ID: "f0e1d2c3b4a59687", Name: d.servers[0].Name,
			Host: "demo.example", Port: 22, K: "демо-ключ-не-настоящий", S: "демо", E: exp.Unix()})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "code": code, "expires": exp.Unix()})
	})
	h("/api/mesh/servers/preview", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code string `json:"code"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		if !strings.HasPrefix(strings.TrimSpace(req.Code), codePrefix) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "это не код добавления сервера — он начинается с " + codePrefix})
			return
		}
		writeJSON(w, http.StatusOK, JoinPreview{Servers: []string{"Амстердам", "Лондон"}, Rules: 2, NetRules: 4,
			Holds: 1, AddAccounts: 6, GiveAccounts: len(d.clients)})
	})
	h("/api/mesh/servers/join", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code string `json:"code"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		if !strings.HasPrefix(strings.TrimSpace(req.Code), codePrefix) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "это не код добавления сервера — он начинается с " + codePrefix})
			return
		}
		d.srvnet = demoSrvNet{others: demoOthers()}
		writeJSON(w, http.StatusOK, ok)
	})
	h("/api/mesh/servers/remove", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		d.srvnetState()
		if req.ID == "f0e1d2c3b4a59687" {
			d.srvnet = demoSrvNet{alone: true}
			writeJSON(w, http.StatusOK, ok)
			return
		}
		out := d.srvnet.others[:0]
		for _, s := range d.srvnet.others {
			if s.ID != req.ID {
				out = append(out, s)
			}
		}
		d.srvnet.others = out
		d.srvnet.alone = len(out) == 0
		writeJSON(w, http.StatusOK, ok)
	})
	h("/api/mesh/servers/migrate", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "в режиме показа этот сервер уже в сети серверов"})
	})
}
