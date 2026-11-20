package panel

import (
	"net/http"
	"strings"
	"testing"

	"sshtunnel/internal/nodeapi"
)

func nodePluginServer(t *testing.T) (*fakeNodeAPI, http.Handler, []*http.Cookie) {
	t.Helper()
	s, _ := newTestServer(t)
	sys := newFakeNodeSystem()
	sys.units[nodeUnit] = UnitState{Installed: true, Active: true}
	api := newFakeNodeAPI()
	s.WithNode(NewNodeManager(sys, api, "v1.10.5"))
	id, _ := s.sessions.create("admin", false)
	return api, s.Handler(), []*http.Cookie{{Name: cookieName, Value: id}}
}

// ТЗ 29: «Убрать плагин» без данных — плагин выключен, папки на месте.
func TestNodeУбратьПлагинБезДанных(t *testing.T) {
	api, h, cookies := nodePluginServer(t)
	netID := strings.Repeat("b", 64)
	api.cfg.Mirror = true
	api.folders[netID] = []nodeapi.Folder{{ID: "f1", Name: "Фото"}}

	rec, out := doJSON(t, h, http.MethodPost, "/api/node/plugin/remove", map[string]any{"plugin": "mirror"}, cookies)
	if rec.Code != 200 {
		t.Fatalf("%d %v", rec.Code, out)
	}
	if out["config"].(map[string]any)["mirror"] != false {
		t.Fatalf("плагин не выключен: %v", out)
	}
	if len(api.folders[netID]) != 1 {
		t.Fatalf("без deleteData папки не трогаются: %v", api.folders[netID])
	}
}

// С данными — папки всех сетей и общая папка стёрты, «Можно менять» снят.
func TestNodeУбратьПлагинСДанными(t *testing.T) {
	api, h, cookies := nodePluginServer(t)
	n1, n2 := strings.Repeat("c", 64), strings.Repeat("d", 64)
	api.cfg.Mirror = true
	api.folders[n1] = []nodeapi.Folder{{ID: "f1", Name: "Фото"}, {ID: "f2", Name: "Доки"}}
	api.folders[n2] = []nodeapi.Folder{{ID: "f3", Name: "Музыка"}}

	rec, out := doJSON(t, h, http.MethodPost, "/api/node/plugin/remove", map[string]any{"plugin": "mirror", "deleteData": true}, cookies)
	if rec.Code != 200 {
		t.Fatalf("%d %v", rec.Code, out)
	}
	if len(api.folders[n1])+len(api.folders[n2]) != 0 {
		t.Fatalf("папки остались: %v", api.folders)
	}

	api.cfg.Files, api.cfg.FilesWrite = true, true
	api.files = map[string]int64{n1: 5 << 20}
	rec, out = doJSON(t, h, http.MethodPost, "/api/node/plugin/remove", map[string]any{"plugin": "files", "deleteData": true}, cookies)
	if rec.Code != 200 {
		t.Fatalf("files: %d %v", rec.Code, out)
	}
	if len(api.filesCleared) != 1 || api.filesCleared[0] != n1 {
		t.Fatalf("общая папка не стёрта: %v", api.filesCleared)
	}
	if api.cfg.Files || api.cfg.FilesWrite {
		t.Fatalf("files/filesWrite не выключены: %+v", api.cfg)
	}
}

// Старый хост без /files/clear — понятная просьба обновить, плагин всё
// равно выключен.
func TestNodeУбратьПлагинСтарыйХост(t *testing.T) {
	api, h, cookies := nodePluginServer(t)
	n1 := strings.Repeat("e", 64)
	api.cfg.Files = true
	api.files = map[string]int64{n1: 1 << 20}
	api.filesOld = true
	rec, out := doJSON(t, h, http.MethodPost, "/api/node/plugin/remove", map[string]any{"plugin": "files", "deleteData": true}, cookies)
	if rec.Code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "Обновить") {
		t.Fatalf("ждали просьбу обновить хост: %d %v", rec.Code, out)
	}
	if api.cfg.Files {
		t.Fatal("плагин должен выключиться и со старым хостом")
	}
}

func TestNodeУбратьНеизвестныйПлагин(t *testing.T) {
	_, h, cookies := nodePluginServer(t)
	rec, out := doJSON(t, h, http.MethodPost, "/api/node/plugin/remove", map[string]any{"plugin": "nope"}, cookies)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %v", rec.Code, out)
	}
}
