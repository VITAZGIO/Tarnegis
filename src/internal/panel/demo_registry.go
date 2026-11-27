package panel

// Режим показа: устройства из панели (ТЗ 47) — серверы для галочек,
// приглашения, «Серверы» и «Сеть устройств» у устройства. Приглашение в
// показе настоящее по виду, но войти по нему некуда.

import (
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// demoDevServers — серверы сети со стороны «этого».
func (d *demoBackend) demoDevServers() []devServer {
	st := d.srvnetState()
	out := []devServer{}
	for _, s := range st.Servers {
		out = append(out, devServer{ID: s.ID, Name: s.Name, Self: s.Self})
	}
	return out
}

func (d *demoBackend) registryRoutes(h func(string, func(http.ResponseWriter, *http.Request))) {
	ok := map[string]bool{"ok": true}
	h("/api/devices/servers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"servers": d.demoDevServers()})
	})
	h("/api/devices/invites", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().Unix()
		out := []inviteView{}
		for i := len(d.invites) - 1; i >= 0; i-- {
			if d.invites[i].Expires > now {
				out = append(out, d.invites[i])
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"invites": out})
	})
	h("/api/devices/invite", func(w http.ResponseWriter, r *http.Request) {
		var req inviteReq
		if !decodePost(w, r, &req) {
			return
		}
		name, dt := strings.TrimSpace(req.Name), DeviceType(req.DeviceType)
		var servers []string
		if req.User != "" {
			c := d.client(req.User)
			if c == nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нет такого клиента"})
				return
			}
			name, dt, servers = c.Name, c.DeviceType, c.Servers
		} else if name == "" || len([]rune(name)) > maxNameLength || !validDeviceType(dt) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "имя или тип устройства не подходят"})
			return
		} else {
			servers = cleanServers(req.Servers)
			self := d.srvnetState().SelfID
			if !slices.Contains(servers, self) {
				servers = append(servers, self)
				slices.Sort(servers)
			}
		}
		if len(d.invites) >= maxDevInvites {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "уже 20 неиспользованных приглашений — отзови ненужные"})
			return
		}
		now := time.Now()
		v := inviteView{ID: randomHex(6), Name: name, DeviceType: dt, Servers: servers, Mesh: req.Mesh == nil || *req.Mesh,
			User: req.User, Created: now.Unix(), Expires: now.Add(devInviteTTL).Unix()}
		d.invites = append(d.invites, v)
		text := encodeDevInvite(inviteDoc{V: 1, Host: "203.0.113.5", Port: 22,
			HK: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDemoDemoDemoDemoDemoDemoDemoDemoDemoDemo", Tok: randomHex(32),
			Exp: v.Expires, Name: name})
		res := inviteResult{Invite: v, Text: text, Host: "203.0.113.5", Port: 22,
			Code: inviteCode("203.0.113.5", 22, devInvite{Secret: randomHex(7)})}
		if png, err := qrcode.Encode("tarnegis:"+res.Code, qrcode.Low, 512); err == nil {
			res.QR = base64.StdEncoding.EncodeToString(png)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
	})
	h("/api/devices/invite/revoke", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		n := len(d.invites)
		d.invites = slices.DeleteFunc(d.invites, func(v inviteView) bool { return v.ID == req.ID })
		if len(d.invites) == n {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нет такого приглашения"})
			return
		}
		writeJSON(w, http.StatusOK, ok)
	})
	h("/api/clients/scope", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID      string   `json:"id"`
			Servers []string `json:"servers"`
			Mesh    bool     `json:"mesh"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		c := d.client(req.ID)
		if c == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нет такого клиента"})
			return
		}
		list := cleanServers(req.Servers)
		if req.Servers != nil && len(list) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "отметь хотя бы один сервер"})
			return
		}
		if c.Servers == nil && len(list) == len(d.demoDevServers()) {
			list = nil
		}
		c.Servers, c.NoMesh = list, !req.Mesh
		writeJSON(w, http.StatusOK, ok)
	})
	h("/api/clients/key/remove", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		}
		if !decodePost(w, r, &req) {
			return
		}
		c := d.client(req.ID)
		if c == nil || len(c.keys()) < 2 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "это последний ключ устройства — удали устройство целиком"})
			return
		}
		c.ExtraKeys = slices.DeleteFunc(c.ExtraKeys, func(k string) bool { return k == req.Key })
		writeJSON(w, http.StatusOK, ok)
	})
}
