package meshsvc

// Обновление сети устройств (meshd 8, ТЗ 40), см. cmd/meshd «обновление
// сети». У старого meshd этих путей нет — ErrUnsupported.

import (
	"context"
	"net/http"
)

// NetUpdateReq — просьба устройства «обновить сеть до Version». Device —
// id устройства (как в State), User — его SSH-пользователь.
type NetUpdateReq struct {
	Call    string `json:"call"`
	Net     string `json:"net"`
	Device  string `json:"device"`
	Name    string `json:"name,omitempty"`
	User    string `json:"user,omitempty"`
	Version string `json:"version"`
	At      int64  `json:"at"`
}

// NetUpdateAnswer — ответ панели: Error пуст — принято.
type NetUpdateAnswer struct {
	Call  string `json:"call"`
	Error string `json:"error,omitempty"`
}

// SetNetVersion — версия сети устройств ("" — только релиз meshd).
func (a *Admin) SetNetVersion(ctx context.Context, ver string) error {
	return a.do(ctx, http.MethodPost, "/v1/netver", map[string]string{"version": ver}, nil)
}

// DevUpdate — «Обновить» у одного устройства (на связи и старше сети).
func (a *Admin) DevUpdate(ctx context.Context, netID, device string) error {
	return a.do(ctx, http.MethodPost, "/v1/devupdate", deviceReq{Net: netID, Device: device}, nil)
}

// SetAutoUpdate — «Получать автообновление» у устройства; ответ — время правки.
func (a *Admin) SetAutoUpdate(ctx context.Context, netID, device string, on bool) (int64, error) {
	var out struct {
		T int64 `json:"t"`
	}
	err := a.do(ctx, http.MethodPost, "/v1/autoupd", map[string]any{"net": netID, "device": device, "on": on}, &out)
	return out.T, err
}

// NetUpdates — отдать ответы на прошлые просьбы и забрать новые.
func (a *Admin) NetUpdates(ctx context.Context, answers []NetUpdateAnswer) ([]NetUpdateReq, error) {
	if answers == nil {
		answers = []NetUpdateAnswer{}
	}
	var out struct {
		Requests []NetUpdateReq `json:"requests"`
	}
	err := a.do(ctx, http.MethodPost, "/v1/netupdate", map[string]any{"answers": answers}, &out)
	return out.Requests, err
}
