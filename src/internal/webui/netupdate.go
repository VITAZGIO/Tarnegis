package webui

// Обновление сети устройств (ТЗ 40), ручки окна:
//
//	GET  /api/update/net → updater.AutoState: автообновление, версия сети,
//	     роль «Моё устройство», предложение и ход обновления;
//	POST /api/update/net {"version":"1.10.8"} — «Обновить сеть до vX»:
//	     попросить серверы (ответ — когда панель приняла) и обновиться самому;
//	POST /api/update/now {"version":"1.10.8"} — обновить это устройство до
//	     версии сети (строка «Есть новая версия сети vX — обновить?»);
//	POST /api/update/auto {"on":bool} — галочка «Автообновление».

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

func (s *Server) handleUpdateNet(w http.ResponseWriter, r *http.Request) {
	a := s.app.AutoUpdate()
	if r.Method == http.MethodGet {
		writeJSON(w, a.State())
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Version == "" {
		writeJSON(w, map[string]string{"error": "нужна версия: {\"version\":\"1.10.8\"}"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := s.app.RequestNetUpdate(ctx, req.Version); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "state": a.State()})
}

func (s *Server) handleUpdateNow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Version == "" {
		writeJSON(w, map[string]string{"error": "нужна версия: {\"version\":\"1.10.8\"}"})
		return
	}
	a := s.app.AutoUpdate()
	if err := a.UpdateNow(req.Version); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "state": a.State()})
}

func (s *Server) handleUpdateAuto(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On *bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.On == nil {
		writeJSON(w, map[string]string{"error": "нужно {\"on\":true|false}"})
		return
	}
	if _, err := s.app.SetAutoUpdate(*req.On, 0); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "state": s.app.AutoUpdate().State()})
}
