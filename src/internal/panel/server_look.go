package panel

import (
	"net/http"
	"strings"
)

// Тема и фон панели (ТЗ 25): тот же выбор, что в программе. Страница
// хранит выбор у себя (localStorage) и здесь — по логину, чтобы другой
// браузер открылся так же.

var (
	lookThemes = map[string]bool{"": true, "dark": true, "light": true, "console": true}
	lookBgs    = map[string]bool{"": true, "code": true, "author": true}
)

// handleLook — GET /api/settings/look → {"theme","bg"}; POST с теми же
// полями сохраняет выбор текущего пользователя.
func (s *Server) handleLook(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	user := strings.ToLower(sess.username)
	if r.Method == http.MethodGet {
		var look PanelLook
		if s.settings != nil {
			look = s.settings.Get().Looks[user]
		}
		writeJSON(w, http.StatusOK, look)
		return
	}
	var req PanelLook
	if !decodePost(w, r, &req) {
		return
	}
	if !lookThemes[req.Theme] || !lookBgs[req.Bg] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нет такой темы или фона"})
		return
	}
	if s.settings == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "настройки панели недоступны"})
		return
	}
	err := s.settings.Update(func(st *Settings) error {
		if st.Looks == nil {
			st.Looks = map[string]PanelLook{}
		}
		if req == (PanelLook{}) {
			delete(st.Looks, user)
		} else {
			st.Looks[user] = req
		}
		return nil
	})
	replyErr(w, err, req)
}
