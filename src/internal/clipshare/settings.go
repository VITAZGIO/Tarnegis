package clipshare

// Настройки «Общего буфера» на этом устройстве (ТЗ 52): пока одна —
// «Скриншоты сразу в общий буфер» (телефон). Хранятся рядом с историей,
// меняются через API — экран, PowerShell и MCP делают это одинаково.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Settings — настройки буфера этого устройства.
type Settings struct {
	// Screenshots — новый скриншот на телефоне сразу уходит картинкой
	// (KindImage) на остальные устройства. По умолчанию выключено.
	Screenshots bool `json:"screenshots"`
	// ScreenshotsSince — когда галочку включили (unix, с): скриншоты старше
	// не отправляются (иначе включение разослало бы старые).
	ScreenshotsSince int64 `json:"screenshotsSince,omitempty"`
}

var setMu sync.Mutex

func (h *history) settingsPath() string { return filepath.Join(h.dir, "settings.json") }

func (h *history) settings() Settings {
	setMu.Lock()
	defer setMu.Unlock()
	var s Settings
	if b, err := os.ReadFile(h.settingsPath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func (h *history) setScreenshots(on bool, now time.Time) Settings {
	setMu.Lock()
	defer setMu.Unlock()
	var s Settings
	if b, err := os.ReadFile(h.settingsPath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if on && !s.Screenshots {
		s.ScreenshotsSince = now.Unix()
	}
	s.Screenshots = on
	if b, err := json.Marshal(s); err == nil {
		_ = os.WriteFile(h.settingsPath(), b, 0o600)
	}
	return s
}

// CurrentSettings — настройки буфера; буфер не добавлен или выключен — всё
// выключено.
func CurrentSettings() Settings {
	n := current()
	if n == nil {
		return Settings{}
	}
	return n.h.settings()
}

// handleSettings — GET: настройки; POST {"screenshots":bool}: поменять.
func handleSettings(w http.ResponseWriter, r *http.Request) {
	n := current()
	if n == nil {
		writeJSON(w, map[string]string{"error": errOff.Error()})
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			Screenshots *bool `json:"screenshots"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Screenshots == nil {
			writeJSON(w, map[string]string{"error": "нужен JSON {\"screenshots\":true|false}"})
			return
		}
		writeJSON(w, n.h.setScreenshots(*req.Screenshots, time.Now()))
		return
	}
	writeJSON(w, n.h.settings())
}
