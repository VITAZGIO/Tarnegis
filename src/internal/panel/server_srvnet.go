package panel

// Ручки сети серверов (ТЗ 22) — всё, что делает блок «Серверы сети»:
//
//	GET  /api/mesh/servers                 → SrvNetState
//	POST /api/mesh/servers/invite  {}      → {"code","expires"}
//	POST /api/mesh/servers/preview {code}  → JoinPreview
//	POST /api/mesh/servers/join    {code}  → {"ok":true} (идёт заданием)
//	POST /api/mesh/servers/remove  {id}    → {"ok":true}
//	POST /api/mesh/servers/migrate {}      → {"ok":true} (старый способ → сеть)
//	POST /api/mesh/servers/rename {id,name} → {"ok":true} (ТЗ 24; id "main" —
//	     этот сервер, "link:…" — сервер старого способа)

import (
	"net/http"
	"strings"
)

func (s *Server) serverNetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/mesh/servers", s.requireAuth(s.withMesh(s.handleServers)))
	mux.HandleFunc("/api/update/all", s.requireAuth(s.withMesh(s.handleUpdateAll)))
	mux.HandleFunc("/api/mesh/servers/invite", s.requireAuth(s.withMesh(s.handleServersInvite)))
	mux.HandleFunc("/api/mesh/servers/preview", s.requireAuth(s.withMesh(s.handleServersPreview)))
	mux.HandleFunc("/api/mesh/servers/join", s.requireAuth(s.withMesh(s.handleServersJoin)))
	mux.HandleFunc("/api/mesh/servers/remove", s.requireAuth(s.withMesh(s.handleServersRemove)))
	mux.HandleFunc("/api/mesh/servers/migrate", s.requireAuth(s.withMesh(s.handleServersMigrate)))
	mux.HandleFunc("/api/mesh/servers/rename", s.requireAuth(s.withMesh(s.handleServersRename)))
}

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mesh.State(r.Context()).ServerNet)
}

func (s *Server) handleServersInvite(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodePost(w, r, &req) {
		return
	}
	code, exp, err := s.mesh.net.Invite(r.Context())
	replyErr(w, err, map[string]any{"ok": true, "code": code, "expires": exp.Unix()})
}

func (s *Server) handleServersPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	p, err := s.mesh.net.Preview(r.Context(), req.Code)
	replyErr(w, err, p)
}

func (s *Server) handleServersJoin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.mesh.net.Join(req.Code), map[string]bool{"ok": true})
}

func (s *Server) handleServersRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	var err error
	if id, ok := strings.CutPrefix(req.ID, legacyPrefix); ok {
		err = s.mesh.DeleteLink(id)
	} else {
		err = s.mesh.net.Remove(r.Context(), req.ID)
	}
	replyErr(w, err, map[string]bool{"ok": true})
}

func (s *Server) handleServersMigrate(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.mesh.net.Migrate(), map[string]bool{"ok": true})
}

// handleServersRename — новое имя серверу сети: оно общее, побеждает
// последняя правка, на всех панелях, схеме и устройствах (ТЗ 24).
func (s *Server) handleServersRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	var err error
	if id, ok := strings.CutPrefix(req.ID, legacyPrefix); ok {
		err = s.mesh.RenameLink(id, req.Name)
	} else {
		err = s.mesh.net.RenameServer(r.Context(), req.ID, req.Name)
	}
	replyErr(w, err, map[string]bool{"ok": true})
}
