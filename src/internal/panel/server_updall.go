package panel

// Ручки «Обновить на всех серверах» (ТЗ 31, часть C; srvnet_update.go):
//
//	GET  /api/update/all → UpdAll: версии панели и meshd каждого сервера
//	     сети и ход обновления (target, servers[].stage/error);
//	POST /api/update/all {} → {"ok":true,"target":"1.10.7"}: взять
//	     последний релиз с GitHub (как «Проверить обновления») и попросить
//	     все серверы сети обновиться до него; этот — последним.

import (
	"net/http"
	"strings"
)

func (s *Server) handleUpdateAll(w http.ResponseWriter, r *http.Request) {
	n := s.mesh.net
	if r.Method == http.MethodGet {
		// Страница следит за ходом — свежие картины других серверов.
		n.RefreshViews()
		writeJSON(w, http.StatusOK, n.UpdateAllState(r.Context()))
		return
	}
	var req struct{}
	if !decodePost(w, r, &req) {
		return
	}
	rel, err := selfUpdateLatest(r.Context())
	if err != nil {
		replyErr(w, err, nil)
		return
	}
	target := cleanVersion(rel.Tag)
	if target == "" {
		replyErr(w, errBadRelease(rel.Tag), nil)
		return
	}
	// Обновлять есть кого: хоть у одного сервера панель старше.
	st := n.UpdateAllState(r.Context())
	older := false
	for _, sv := range st.Servers {
		if sv.Panel == "" || versionLess(sv.Panel, target) {
			older = true
		}
	}
	if !older {
		replyErr(w, errAllLatest(target), nil)
		return
	}
	replyErr(w, n.UpdateAll(r.Context(), target), map[string]any{"ok": true, "target": target})
}

type textErr string

func (e textErr) Error() string { return string(e) }

func errBadRelease(tag string) error {
	return textErr("у последнего релиза непонятный номер «" + strings.TrimSpace(tag) + "»")
}

func errAllLatest(v string) error {
	return textErr("на всех серверах сети уже стоит " + v)
}
