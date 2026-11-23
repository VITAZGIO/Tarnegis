package panel

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"sshtunnel/internal/nodeapi"
)

// WithNode подключает хост плагинов сервера.
func (s *Server) WithNode(m *NodeManager) *Server {
	s.node = m
	return s
}

func (s *Server) nodeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/node/state", s.requireAuth(s.withNode(s.handleNodeState)))
	mux.HandleFunc("/api/node/install", s.requireAuth(s.withNode(s.handleNodeInstall)))
	mux.HandleFunc("/api/node/remove", s.requireAuth(s.withNode(s.handleNodeRemove)))
	mux.HandleFunc("/api/node/config", s.requireAuth(s.withNode(s.handleNodeConfig)))
	mux.HandleFunc("/api/node/folder/add", s.requireAuth(s.withNode(s.handleNodeFolderAdd)))
	mux.HandleFunc("/api/node/folder/remove", s.requireAuth(s.withNode(s.handleNodeFolderRemove)))
	mux.HandleFunc("/api/node/folder/quota", s.requireAuth(s.withNode(s.handleNodeFolderQuota)))
	mux.HandleFunc("/api/node/folder/access", s.requireAuth(s.withNode(s.handleNodeFolderAccess)))
	// ТЗ 36: снимки зеркальных папок.
	mux.HandleFunc("/api/node/folder/snapshots", s.requireAuth(s.withNode(s.handleNodeSnapshots)))
	mux.HandleFunc("/api/node/folder/snapshot", s.requireAuth(s.withNode(s.handleNodeSnapshotGet)))
	mux.HandleFunc("/api/node/folder/snap/cfg", s.requireAuth(s.withNode(s.handleNodeSnapCfg)))
	mux.HandleFunc("/api/node/folder/snap/now", s.requireAuth(s.withNode(s.handleNodeSnapNow)))
	mux.HandleFunc("/api/node/folder/snap/restore", s.requireAuth(s.withNode(s.handleNodeSnapRestore)))
	mux.HandleFunc("/api/node/clipboard/clear", s.requireAuth(s.withNode(s.handleNodeClipboardClear)))
	mux.HandleFunc("/api/node/backup/delete", s.requireAuth(s.withNode(s.handleNodeBackupDelete)))
	mux.HandleFunc("/api/node/net/delete", s.requireAuth(s.withNode(s.handleNodeNetDelete)))
	mux.HandleFunc("/api/node/fix", s.requireAuth(s.withNode(s.handleNodeFix)))
	mux.HandleFunc("/api/node/plugin/remove", s.requireAuth(s.withNode(s.handleNodePluginRemove)))
	// ТЗ 15: общий список блокировки, проверка сайта с сервера, история машин.
	mux.HandleFunc("/api/node/adblock/set", s.requireAuth(s.withNode(s.handleNodeAdblockSet)))
	mux.HandleFunc("/api/node/adblock/update", s.requireAuth(s.withNode(s.handleNodeAdblockUpdate)))
	mux.HandleFunc("/api/node/sitecheck", s.requireAuth(s.withNode(s.handleNodeSitecheck)))
	mux.HandleFunc("/api/node/machines/clear", s.requireAuth(s.withNode(s.handleNodeMachinesClear)))
}

func (s *Server) handleNodeAdblockSet(w http.ResponseWriter, r *http.Request) {
	var req nodeapi.AdblockConf
	if !decodePost(w, r, &req) {
		return
	}
	st, err := s.node.api.SetAdblock(r.Context(), req)
	replyErr(w, err, st)
}

func (s *Server) handleNodeAdblockUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodePost(w, r, &req) {
		return
	}
	st, err := s.node.api.UpdateAdblock(r.Context())
	replyErr(w, err, st)
}

// handleNodeSitecheck — «Проверить сайт» с сервера кнопкой панели; ответ —
// как у устройств ({server:{steps…},verdict,text}).
func (s *Server) handleNodeSitecheck(w http.ResponseWriter, r *http.Request) {
	var req nodeapi.SiteReq
	if !decodePost(w, r, &req) {
		return
	}
	res, err := s.node.api.CheckSite(r.Context(), req)
	if err != nil {
		replyErr(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(append(json.RawMessage(nil), res...))
}

func (s *Server) handleNodeMachinesClear(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Net string `json:"net"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.node.api.ClearMachines(r.Context(), req.Net), map[string]bool{"ok": true})
}

func (s *Server) withNode(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.node == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "node_unavailable"})
			return
		}
		next(w, r)
	}
}

func (s *Server) handleNodeState(w http.ResponseWriter, r *http.Request) {
	st := s.nodeStatusWithAccess(r.Context())
	st.Warnings = s.nodeWarnings(r.Context(), st.State)
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleNodeBackupDelete(w http.ResponseWriter, r *http.Request) {
	var req nodeapi.BackupReq
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.node.DeleteBackup(r.Context(), req), map[string]bool{"ok": true})
}

// handleNodeNetDelete — стереть данные плагинов сети, которой больше нет в
// meshd. Сеть ещё есть (или проверить нельзя) — отказ: данные живой сети
// так не стираются.
func (s *Server) handleNodeNetDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Net string `json:"net"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	has, ok := s.meshHasNet(r.Context(), req.Net)
	switch {
	case !ok:
		replyErr(w, errors.New("не удалось спросить meshd, есть ли эта сеть, — данные не тронуты"), nil)
		return
	case has:
		replyErr(w, errors.New("эта сеть ещё есть в сети устройств — сначала удали её в «Сетях»"), nil)
		return
	}
	replyErr(w, s.node.DeleteNet(r.Context(), req.Net), map[string]bool{"ok": true})
}

// handleNodeFix — безопасное исправление того, что мешает плагинам.
func (s *Server) handleNodeFix(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.fixNode(r.Context(), req.Code), map[string]bool{"ok": true})
}

func (s *Server) handleNodeInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	replyErr(w, s.node.Install(), map[string]bool{"ok": true})
}

func (s *Server) handleNodeRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeleteData bool `json:"deleteData"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.node.Remove(req.DeleteData), map[string]bool{"ok": true})
}

func (s *Server) handleNodeConfig(w http.ResponseWriter, r *http.Request) {
	var p nodeapi.ConfigPatch
	if !decodePost(w, r, &p) {
		return
	}
	st, err := s.node.SetConfig(r.Context(), p)
	replyErr(w, err, st)
}

func (s *Server) handleNodeFolderAdd(w http.ResponseWriter, r *http.Request) {
	var req nodeapi.FolderReq
	if !decodePost(w, r, &req) {
		return
	}
	f, err := s.node.AddFolder(r.Context(), req)
	replyErr(w, err, f)
}

func (s *Server) handleNodeFolderRemove(w http.ResponseWriter, r *http.Request) {
	var req nodeapi.FolderReq
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.node.RemoveFolder(r.Context(), req), map[string]bool{"ok": true})
}

func (s *Server) handleNodeFolderQuota(w http.ResponseWriter, r *http.Request) {
	var req nodeapi.FolderReq
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.node.SetFolderQuota(r.Context(), req), map[string]bool{"ok": true})
}

func (s *Server) handleNodeClipboardClear(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Net string `json:"net"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.node.ClearClipboard(r.Context(), req.Net), map[string]bool{"ok": true})
}

// ---- снимки зеркальных папок (ТЗ 36) ----

// oldNodeSnap — старый хост не знает снимков (404): понятный текст.
func oldNodeSnap(err error) error {
	if err != nil && strings.Contains(err.Error(), "404") {
		return errors.New("хост плагинов на сервере старый и не умеет снимков — обнови его («Обновить на всех серверах»)")
	}
	return err
}

func (s *Server) handleNodeSnapshots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sl, err := s.node.Snapshots(r.Context(), q.Get("net"), q.Get("id"))
	replyErr(w, oldNodeSnap(err), sl)
}

// handleNodeSnapshotGet — скачать архив снимка через вход в панель.
func (s *Server) handleNodeSnapshotGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rc, size, err := s.node.api.OpenSnapshot(r.Context(), q.Get("net"), q.Get("id"), q.Get("snap"))
	if err != nil {
		replyErr(w, oldNodeSnap(err), nil)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(q.Get("snap")))
	w.Header().Set("Cache-Control", "no-store")
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	_, _ = io.Copy(w, rc)
}

func (s *Server) handleNodeSnapCfg(w http.ResponseWriter, r *http.Request) {
	var req nodeapi.SnapReq
	if !decodePost(w, r, &req) {
		return
	}
	sl, err := s.node.SnapConfig(r.Context(), req)
	replyErr(w, oldNodeSnap(err), sl)
}

func (s *Server) handleNodeSnapNow(w http.ResponseWriter, r *http.Request) {
	var req nodeapi.SnapReq
	if !decodePost(w, r, &req) {
		return
	}
	sl, err := s.node.SnapNow(r.Context(), req)
	replyErr(w, oldNodeSnap(err), sl)
}

func (s *Server) handleNodeSnapRestore(w http.ResponseWriter, r *http.Request) {
	var req nodeapi.SnapReq
	if !decodePost(w, r, &req) {
		return
	}
	res, err := s.node.SnapRestore(r.Context(), req)
	replyErr(w, oldNodeSnap(err), res)
}
