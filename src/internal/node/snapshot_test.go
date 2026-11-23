package node

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/mirror"
	"sshtunnel/internal/nodeapi"
)

// ТЗ 36: снимки зеркальных папок через управляющий вход (как ходит панель):
// настройка, «Сделать сейчас», список, скачивание, восстановление, и
// снимки не занимают квоту папки.
func TestСнимкиПапкиЧерезУправление(t *testing.T) {
	h := newTestHost(t)
	on := true
	if _, err := h.SetConfig(nodeapi.ConfigPatch{Mirror: &on}); err != nil {
		t.Fatal(err)
	}
	netID := strings.Repeat("e", 64)
	f, err := h.AddFolder(nodeapi.FolderReq{Net: netID, Name: "server"})
	if err != nil {
		t.Fatal(err)
	}
	grant(t, h, netID, f.ID, "pc")
	dev := serverFor(h, nodeapi.PluginMirror, nodeapi.Caller{Net: netID, Device: "pc", Host: "pc", IP: "198.19.0.1"})
	defer dev.Close()
	dir := t.TempDir()
	rep := &mirror.Replica{Folder: f.ID, Path: dir, Mode: mirror.ModeBoth, Device: "pc",
		State: filepath.Join(t.TempDir(), "a.json"), R: remoteOf(dev)}
	ctx := context.Background()
	os.WriteFile(filepath.Join(dir, "важное.md"), []byte("было"), 0o644)
	if _, err := rep.Sync(ctx, 0, false); err != nil {
		t.Fatal(err)
	}

	ctl := httptest.NewServer(h.ControlHandler())
	defer ctl.Close()
	post := func(path string, in, out any) {
		t.Helper()
		b, _ := json.Marshal(in)
		resp, err := http.Post(ctl.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, data)
		}
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var sl nodeapi.SnapList
	post("/folder/snap/cfg", nodeapi.SnapReq{Net: netID, ID: f.ID, Daily: true, Time: "04:30", Keep: 5}, &sl)
	if !sl.Config.Daily || sl.Config.Time != "04:30" || sl.Config.Keep != 5 {
		t.Fatalf("настройка: %+v", sl.Config)
	}
	if !strings.Contains(filepath.ToSlash(sl.Dir), "/mirror/hub/"+f.ID+"/snapshots") {
		t.Fatalf("снимки лежат не в данных папки: %s", sl.Dir)
	}
	usedBefore, _ := h.snapHub(netID, f.ID)
	quotaBefore, _ := usedBefore.Usage(f.ID)
	post("/folder/snap/now", nodeapi.SnapReq{Net: netID, ID: f.ID}, &sl)
	if len(sl.Snapshots) != 1 || sl.Snapshots[0].Reason != "manual" || sl.Snapshots[0].Files != 1 || sl.Total == 0 {
		t.Fatalf("снимок: %+v", sl)
	}
	snap := sl.Snapshots[0].File
	if !strings.HasPrefix(snap, "server ") || !strings.HasSuffix(snap, ".zip") {
		t.Fatalf("имя снимка %q", snap)
	}
	if q, _ := usedBefore.Usage(f.ID); q != quotaBefore {
		t.Fatalf("снимок занял квоту папки: %d → %d", quotaBefore, q)
	}
	h.recount()
	if st := h.State(); st.Networks[0].Folders[0].Snap == nil || len(st.Networks[0].Folders[0].Snap.Snapshots) != 1 {
		t.Fatalf("в состоянии нет снимков: %+v", st.Networks[0].Folders[0])
	}

	// Список и скачивание.
	resp, err := http.Get(ctl.URL + "/folder/snap/list?" + url.Values{"net": {netID}, "id": {f.ID}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	var list nodeapi.SnapList
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Snapshots) != 1 || list.Snapshots[0].File != snap {
		t.Fatalf("список: %+v", list)
	}
	resp, err = http.Get(ctl.URL + "/folder/snap/get?" + url.Values{"net": {netID}, "id": {f.ID}, "snap": {snap}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("скачивание: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, zf := range zr.File {
		names[zf.Name] = true
	}
	if !names["важное.md"] || !names["snapshot.json"] {
		t.Fatalf("в архиве %v", names)
	}
	resp, _ = http.Get(ctl.URL + "/folder/snap/get?" + url.Values{"net": {netID}, "id": {f.ID}, "snap": {"../config.json"}}.Encode())
	if resp.StatusCode == 200 {
		t.Fatal("отдал файл не из списка снимков")
	}
	resp.Body.Close()

	// Папку засорили — восстановление, и устройство получает старое.
	os.WriteFile(filepath.Join(dir, "важное.md"), []byte("испорчено"), 0o644)
	os.WriteFile(filepath.Join(dir, "мусор.md"), []byte("лишнее"), 0o644)
	if _, err := rep.Sync(ctx, 0, false); err != nil {
		t.Fatal(err)
	}
	var res nodeapi.RestoreResult
	post("/folder/snap/restore", nodeapi.SnapReq{Net: netID, ID: f.ID, Snap: snap}, &res)
	if res.Changed != 1 || res.Deleted != 1 || res.Added != 0 || res.Pre == "" {
		t.Fatalf("восстановление: %+v", res)
	}
	for i := 0; i < 3; i++ {
		if _, err := rep.Sync(ctx, 0, false); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "важное.md")); string(b) != "было" {
		t.Fatalf("на устройстве %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "мусор.md")); err == nil {
		t.Fatal("лишний файл остался на устройстве")
	}
	found := false
	for i := 0; i < 100 && !found; i++ { // журнал пишется из горутины
		for _, e := range h.State().Events {
			if strings.Contains(e.Text, "возвращена к снимку") && strings.Contains(e.Text, "изменено 1, удалено 1, добавлено 0") {
				found = true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Fatalf("нет строки журнала о восстановлении: %+v", h.State().Events)
	}
}
