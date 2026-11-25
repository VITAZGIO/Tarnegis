package webui

// /api/hostkey — «ключ сервера изменился» из интерфейса (ТЗ 46): карточка с
// отпечатками, «принять новый ключ», «Отмена», «забыть ключ». Логика и JSON
// общие с телефоном — hostkey.HandleAPI.

import (
	"io"
	"net/http"

	"sshtunnel/internal/config"
	"sshtunnel/internal/hostkey"
)

func (s *Server) handleHostKey(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Method == http.MethodPost {
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		body = b
	}
	q := map[string]string{"host": r.URL.Query().Get("host"), "port": r.URL.Query().Get("port")}
	res, err := hostkey.HandleAPI(config.KnownHostsPath(), r.Method, q, body)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, res)
}
