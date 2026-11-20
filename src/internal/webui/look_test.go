package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sshtunnel/internal/app"
	"sshtunnel/internal/config"
)

// ТЗ 32: тема сохраняется сразу (/api/look), переживает перезапуск, и окно
// приходит уже с ней (без вспышки темы по умолчанию).
func TestLookSavedAtOnce(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("APPDATA", tmp)
	s, err := NewOn(app.New(withHost("203.0.113.10")), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rec := httptest.NewRecorder()
	s.handleLook(rec, httptest.NewRequest(http.MethodPost, "/api/look",
		strings.NewReader(`{"theme":"light","background":"code"}`)))
	if strings.Contains(rec.Body.String(), "error") {
		t.Fatal(rec.Body.String())
	}
	if got := config.Load(); got.Theme != "light" || got.Background != "code" || got.Language != "ru" {
		t.Fatalf("на диске: тема %q, фон %q, язык %q", got.Theme, got.Background, got.Language)
	}
	rec = httptest.NewRecorder()
	s.handleLook(rec, httptest.NewRequest(http.MethodPost, "/api/look", strings.NewReader(`{"theme":"pink"}`)))
	if !strings.Contains(rec.Body.String(), "error") {
		t.Fatal("неизвестная тема принята")
	}

	rec = httptest.NewRecorder()
	s.handleIndex(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), `<html lang="ru" data-theme="light" data-bg="code">`) {
		t.Fatalf("окно пришло без темы: %.200s", rec.Body.String())
	}
}
