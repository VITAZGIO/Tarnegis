package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/app"
	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
)

// ТЗ 50: «Добавить сервер» (Linux -web, экран «Нет серверов»): при нуле
// серверов новый появляется, становится выбранным и доходит до экрана
// событием config; удалить его можно, и снова будет ноль.
func TestДобавитьСерверПриНуле(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles, cfg.ActiveProfile = nil, ""
	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ch, stop := s.app.Bus.Subscribe()
	defer stop()

	rec := httptest.NewRecorder()
	s.handleProfileAdd(rec, httptest.NewRequest(http.MethodPost, "/api/profile/add", strings.NewReader(`{"name":"","flag":""}`)))
	var r struct {
		OK      bool           `json:"ok"`
		Profile config.Profile `json:"profile"`
		Config  config.Config  `json:"config"`
		Error   string         `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil || !r.OK || r.Error != "" {
		t.Fatalf("%v %s", err, rec.Body.String())
	}
	if r.Profile.ID == "" || len(r.Config.Profiles) != 1 || r.Config.ActiveProfile != r.Profile.ID {
		t.Fatalf("после добавления: %+v", r.Config)
	}
	got := false
	for deadline := time.After(2 * time.Second); !got; {
		select {
		case e := <-ch:
			got = e.Kind == events.KindConfig
		case <-deadline:
			t.Fatal("экран не узнал о новом сервере (нет события config)")
		}
	}

	rec = httptest.NewRecorder()
	s.handleProfileRemove(rec, httptest.NewRequest(http.MethodPost, "/api/profile/remove", strings.NewReader(`{"id":"`+r.Profile.ID+`"}`)))
	if strings.Contains(rec.Body.String(), `"error"`) || len(s.app.Config().Profiles) != 0 {
		t.Fatalf("удаление: %s", rec.Body.String())
	}
}
