package webui

// Устройство по приглашению из панели (ТЗ 48): POST /api/device {op,…}.
// Решения — в app (JoinInvite, SyncDevice…) и config (ApplyDevice).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/invite"
)

// deviceView — что видит экран.
func (s *Server) deviceView() map[string]any {
	cfg := s.app.Config()
	out := map[string]any{"device": nil, "net": cfg.DeviceNetOf(), "meshAllowed": cfg.DeviceMeshAllowed(),
		"netMissing": cfg.DeviceNetMissing(), "gone": s.app.DeviceGone(),
		"sync": s.app.DeviceSyncState()}
	if d := cfg.Device; d != nil {
		out["device"] = map[string]any{"id": d.ID, "name": d.Name, "join": d.Join, "rev": d.Rev,
			"mesh": d.Mesh, "frozen": d.Frozen, "servers": d.Servers, "hidden": d.Hidden,
			"removed": nonNil(cfg.RemovedServers()),
			"profileFrom": d.ProfileFrom, "profileVer": d.ProfileVer}
	}
	return out
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, s.deviceView())
		return
	}
	var req struct {
		Op       string            `json:"op"`
		Invite   string            `json:"invite"`
		Name     string            `json:"name"`
		Type     string            `json:"type"`
		ID       string            `json:"id"`
		Key      string            `json:"key"`
		Hidden   bool              `json:"hidden"`
		On       *bool             `json:"on"`
		OnlyThis *bool             `json:"onlyThis"`
		ExitVia  *[]string         `json:"exitVia"`
		Net      *config.DeviceNet `json:"net"`
	}
	data, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err := json.Unmarshal(data, &req); err != nil {
		writeJSON(w, map[string]string{"error": "не разобрал запрос: " + err.Error()})
		return
	}
	fail := func(err error) { writeJSON(w, map[string]string{"error": err.Error()}) }
	ok := func(note string) {
		v := s.deviceView()
		v["ok"], v["note"] = true, note
		writeJSON(w, v)
	}
	switch req.Op {
	case "parse":
		// Короткий код (ТЗ 54) — ещё short, have, need, partial: поле
		// показывает, сколько символов осталось.
		info, err := invite.Inspect(req.Invite, time.Now())
		if err != nil {
			fail(err)
			return
		}
		info["ok"] = true
		writeJSON(w, info)
	case "join":
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		note, err := s.app.JoinInvite(ctx, req.Invite, req.Name, req.Type)
		if err != nil {
			fail(err)
			return
		}
		ok(note)
	case "sync":
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		res, err := s.app.SyncDevice(ctx)
		if err != nil {
			fail(err)
			return
		}
		note := "без изменений"
		if res.Changed {
			note = "профиль применён"
		}
		ok(note)
	case "hide":
		note, err := s.app.SetDeviceHidden(req.ID, req.Hidden)
		if err != nil {
			fail(err)
			return
		}
		ok(note)
	case "restore":
		note, err := s.app.RestoreDeviceServer(req.Key)
		if err != nil {
			fail(err)
			return
		}
		ok(note)
	case "net":
		n := s.app.Config().DeviceNetOf()
		if req.Net != nil {
			n = *req.Net
		}
		if req.On != nil {
			n.On = *req.On
		}
		if req.OnlyThis != nil {
			n.OnlyThis = *req.OnlyThis
		}
		if req.ExitVia != nil {
			n.ExitVia = *req.ExitVia
		}
		note, err := s.app.SetDeviceNet(n)
		if err != nil {
			fail(err)
			return
		}
		ok(note)
	default:
		fail(errUnknownOp(req.Op))
	}
}

// nonNil — пустой список отдаётся «[]», не null.
func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

type errUnknownOp string

func (e errUnknownOp) Error() string {
	return "неизвестное действие «" + string(e) + "»"
}
