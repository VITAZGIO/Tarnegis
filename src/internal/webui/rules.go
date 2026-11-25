package webui

import (
	"encoding/json"
	"net/http"
	"strings"

	"sshtunnel/internal/config"
	"sshtunnel/internal/routing"
)

// Правила (общие для программы, ТЗ 32; у сервера со «своими правилами» —
// его) отдельным адресом: что идёт напрямую, что всегда
// через сервер и какие программы ведутся через туннель. Это те же поля, что
// в /api/config, — но без ключей и остальных настроек: адрес нужен командам,
// MCP и своим приложениям с разрешением «менять правила» (им незачем видеть
// всё). Решает «куда» по-прежнему только ядро (core.Decide).
//
// Сюда же — DNS режима VPN (ТЗ 9): галочка «Подставные адреса» (fakeIP) и
// список «всегда настоящий адрес» (realIPHosts). Они общие для всех серверов
// (config.Config), а не у профиля.

type rulesResp struct {
	Profile     string   `json:"profile"`
	FilterMode  string   `json:"filterMode"`
	FilterApps  []string `json:"filterApps"`
	DirectHosts []string `json:"directHosts"`
	TunnelHosts []string `json:"tunnelHosts"`
	FakeIP      bool     `json:"fakeIP"`
	RealIPHosts []string `json:"realIPHosts"`
}

func rulesOf(cfg config.Config) rulesResp {
	p := cfg.Active()
	r := rulesResp{Profile: p.ID, FilterMode: p.FilterMode, FilterApps: p.FilterApps,
		DirectHosts: p.DirectHosts, TunnelHosts: p.TunnelHosts,
		FakeIP: cfg.FakeIP, RealIPHosts: cfg.RealIPHosts}
	for _, l := range []*[]string{&r.FilterApps, &r.DirectHosts, &r.TunnelHosts, &r.RealIPHosts} {
		if *l == nil {
			*l = []string{}
		}
	}
	return r
}

// handleRules — GET: правила активного сервера. POST:
//
//	{"op":"add"|"remove","list":"direct"|"tunnel"|"apps"|"realip","value":"youtube.com"}
//	{"filterMode":"all"|"only"|"except"}
//	{"fakeIP":true|false}
//
// Правила применяются сразу, как при сохранении настроек.
// handleDirectGroups — готовые группы «всегда напрямую» (ТЗ 51).
func (s *Server) handleDirectGroups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"groups": routing.Groups()})
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Config()
	p := cfg.Active()
	if r.Method != http.MethodPost {
		writeJSON(w, rulesOf(cfg))
		return
	}
	var req struct {
		Op         string `json:"op"`
		List       string `json:"list"`
		Value      string `json:"value"`
		FilterMode string `json:"filterMode"`
		FakeIP     *bool  `json:"fakeIP"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "нужен JSON {\"op\":\"add|remove\",\"list\":\"direct|tunnel|apps|realip\",\"value\"}, {\"filterMode\"} или {\"fakeIP\"}"})
		return
	}
	if req.FakeIP != nil {
		cfg.FakeIP = *req.FakeIP
	}
	if req.FilterMode != "" {
		switch req.FilterMode {
		case "all", "only", "except":
		default:
			writeJSON(w, map[string]string{"error": "filterMode: all, only или except"})
			return
		}
	}
	var vals []string
	if req.Op != "" {
		switch req.List {
		case "direct", "tunnel", "apps", "realip":
		default:
			writeJSON(w, map[string]string{"error": "list: direct, tunnel, apps или realip"})
			return
		}
		switch req.Op {
		case "add", "remove":
		default:
			writeJSON(w, map[string]string{"error": "op: add или remove"})
			return
		}
		vals = routing.SplitEntries(req.Value)
		if req.List == "apps" {
			// Путь к программе может быть с пробелами — одна запись целиком.
			vals = nil
			if v := strings.TrimSpace(req.Value); v != "" {
				vals = []string{v}
			}
		}
		if len(vals) == 0 {
			writeJSON(w, map[string]string{"error": "пустое value"})
			return
		}
	}
	// Правила — общие для программы (ТЗ 32); у сервера со «своими
	// правилами» — его собственные. Где именно — решает config.EditRules.
	edit := func(l []string) []string {
		switch req.Op {
		case "add":
			l = append([]string(nil), l...)
			for _, v := range vals {
				if !containsFold(l, v) {
					l = append(l, v)
				}
			}
		case "remove":
			out := []string{}
			for _, e := range l {
				if !containsFold(vals, e) {
					out = append(out, e)
				}
			}
			l = out
		}
		return l
	}
	if req.Op != "" && req.List == "realip" {
		cfg.RealIPHosts = edit(cfg.RealIPHosts)
	}
	cfg.Profiles = append([]config.Profile(nil), cfg.Profiles...)
	cfg.EditRules(p.ID, func(rs *config.Prefs) {
		if req.FilterMode != "" {
			rs.FilterMode = req.FilterMode
		}
		if req.Op == "" {
			return
		}
		switch req.List {
		case "direct":
			rs.DirectHosts = edit(rs.DirectHosts)
		case "tunnel":
			rs.TunnelHosts = edit(rs.TunnelHosts)
		case "apps":
			rs.FilterApps = edit(rs.FilterApps)
		}
	})
	note, err := s.app.SetConfig(cfg)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "rules": rulesOf(s.app.Config()), "note": note})
}

func containsFold(l []string, v string) bool {
	for _, e := range l {
		if strings.EqualFold(e, v) {
			return true
		}
	}
	return false
}
