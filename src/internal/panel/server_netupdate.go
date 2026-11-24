package panel

// Ручки обновления сети устройств (ТЗ 40, netupdate.go):
//
//	GET  /api/update/devices → NetDevs: версия сети и устройства сети с
//	     версией, платформой, автообновлением и шагом (ok / behind /
//	     offline / off / unknown);
//	POST /api/update/device {net, device} → {"ok":true}: «Обновить» у одного
//	     устройства (на связи с этим сервером, старше версии сети);
//	POST /api/update/devices {} → {"ok":true,"sent":N}: «Обновить все
//	     устройства» — каждому отстающему на связи;
//	POST /api/mesh/autoupd {net, device, on} → {"ok":true}: «Получать
//	     автообновление» в карточке устройства.

import "net/http"

func (s *Server) netUpdateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/update/devices", s.requireAuth(s.withMesh(s.handleUpdateDevices)))
	mux.HandleFunc("/api/update/device", s.requireAuth(s.withMesh(s.handleUpdateDevice)))
	mux.HandleFunc("/api/mesh/autoupd", s.requireAuth(s.withMesh(s.handleMeshAutoUpd)))
}

func (s *Server) handleUpdateDevices(w http.ResponseWriter, r *http.Request) {
	n := s.mesh.net
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, n.NetDevices(r.Context()))
		return
	}
	var req struct{}
	if !decodePost(w, r, &req) {
		return
	}
	sent, err := n.UpdateDevices(r.Context())
	replyErr(w, err, map[string]any{"ok": true, "sent": sent})
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Net    string `json:"net"`
		Device string `json:"device"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.mesh.net.UpdateDevice(r.Context(), req.Net, req.Device), map[string]bool{"ok": true})
}

func (s *Server) handleMeshAutoUpd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Net    string `json:"net"`
		Device string `json:"device"`
		On     bool   `json:"on"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.mesh.net.SetDeviceAutoUpdate(r.Context(), req.Net, req.Device, req.On), map[string]bool{"ok": true})
}
