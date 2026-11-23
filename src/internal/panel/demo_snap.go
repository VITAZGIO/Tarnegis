package panel

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"sshtunnel/internal/nodeapi"
)

// Снимки зеркальных папок в режиме показа (ТЗ 36): только память; архив
// для «Скачать» собирается на лету (snapshot.json и один файл).

func (d *demoBackend) demoSnaps(netID, id, name string) *nodeapi.SnapList {
	if d.nodeSnaps == nil {
		d.nodeSnaps = map[string]*nodeapi.SnapList{}
	}
	k := netID + "/" + id
	if sl := d.nodeSnaps[k]; sl != nil {
		return sl
	}
	now := time.Now()
	night := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location())
	if night.After(now) {
		night = night.AddDate(0, 0, -1)
	}
	sl := &nodeapi.SnapList{Config: nodeapi.SnapConfig{Daily: true, Every: 1, Time: "03:00", Keep: 3},
		Dir: "/var/lib/ssh_tunnel_node/nets/" + netID + "/mirror/hub/" + id + "/snapshots", LastRun: night.Unix()}
	for i := 2; i >= 0; i-- {
		t := night.AddDate(0, 0, -i)
		sl.Snapshots = append(sl.Snapshots, nodeapi.Snapshot{File: demoSnapName(name, t), Time: t.Unix(),
			Size: int64(19+i) << 20, Files: 412 - i*7, Reason: "auto", Seq: int64(900 - i*40)})
	}
	t := now.Add(-26 * time.Hour)
	sl.Snapshots = append(sl.Snapshots, nodeapi.Snapshot{Time: t.Unix(), Reason: "auto",
		Error: "не хватило места на диске — снимок не сделан, старые снимки не тронуты"})
	d.sortDemoSnaps(sl)
	d.nodeSnaps[k] = sl
	return sl
}

func demoSnapName(name string, t time.Time) string {
	return name + " " + t.Format("2006-01-02 15-04") + ".zip"
}

func (d *demoBackend) sortDemoSnaps(sl *nodeapi.SnapList) {
	sort.SliceStable(sl.Snapshots, func(i, j int) bool { return sl.Snapshots[i].Time > sl.Snapshots[j].Time })
	sl.Total = 0
	for _, s := range sl.Snapshots {
		sl.Total += s.Size
	}
}

// demoRotate — как настоящая ротация: N обычных + не больше 3 «перед
// восстановлением» и 3 ошибок.
func demoRotate(sl *nodeapi.SnapList) {
	var out []nodeapi.Snapshot
	reg, pre, bad := 0, 0, 0
	for _, s := range sl.Snapshots {
		switch {
		case s.Error != "":
			bad++
			if bad <= 3 {
				out = append(out, s)
			}
		case s.Reason == "pre":
			pre++
			if pre <= 3 {
				out = append(out, s)
			}
		default:
			reg++
			if reg <= sl.Config.Keep {
				out = append(out, s)
			}
		}
	}
	sl.Snapshots = out
}

func (d *demoBackend) demoFolder(netID, id string) (nodeapi.Folder, bool) {
	for _, f := range d.nodeFolders[netID] {
		if f.ID == id {
			return f, true
		}
	}
	return nodeapi.Folder{}, false
}

func (d *demoBackend) snapRoutes(h func(string, func(http.ResponseWriter, *http.Request))) {
	h("/api/node/folder/snapshots", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f, ok := d.demoFolder(q.Get("net"), q.Get("id"))
		if !ok {
			replyErr(w, errors.New("нет такой папки"), nil)
			return
		}
		writeJSON(w, http.StatusOK, d.demoSnaps(q.Get("net"), f.ID, f.Name))
	})
	h("/api/node/folder/snapshot", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f, ok := d.demoFolder(q.Get("net"), q.Get("id"))
		if !ok {
			replyErr(w, errors.New("нет такой папки"), nil)
			return
		}
		var snap *nodeapi.Snapshot
		for _, s := range d.demoSnaps(q.Get("net"), f.ID, f.Name).Snapshots {
			if s.File == q.Get("snap") && s.Error == "" {
				s := s
				snap = &s
			}
		}
		if snap == nil {
			replyErr(w, errors.New("такого снимка нет"), nil)
			return
		}
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		mw, _ := zw.Create("snapshot.json")
		meta, _ := json.MarshalIndent(map[string]any{"folder": f.Name, "id": f.ID, "seq": snap.Seq,
			"time": time.Unix(snap.Time, 0).Format(time.RFC3339), "version": d.version, "demo": true}, "", " ")
		mw.Write(meta)
		fw, _ := zw.Create("Показ.md")
		fmt.Fprintf(fw, "Это архив режима показа: настоящий снимок содержит все файлы папки «%s».\n", f.Name)
		zw.Close()
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(snap.File))
		w.Write(buf.Bytes())
	})
	h("/api/node/folder/snap/cfg", func(w http.ResponseWriter, r *http.Request) {
		var req nodeapi.SnapReq
		if !decodePost(w, r, &req) {
			return
		}
		f, ok := d.demoFolder(req.Net, req.ID)
		if !ok {
			replyErr(w, errors.New("нет такой папки"), nil)
			return
		}
		if req.Time == "" {
			req.Time = "03:00"
		}
		if hh, mm, ok := strings.Cut(req.Time, ":"); !ok || len(hh) != 2 || len(mm) != 2 || hh > "23" || mm > "59" {
			replyErr(w, errors.New("время — ЧЧ:ММ, например 03:00"), nil)
			return
		}
		if req.Keep == 0 {
			req.Keep = 3
		}
		if req.Keep < 1 || req.Keep > 30 {
			replyErr(w, errors.New("хранить — от 1 до 30 снимков"), nil)
			return
		}
		sl := d.demoSnaps(req.Net, f.ID, f.Name)
		if req.Every == 0 {
			req.Every = 1
		}
		sl.Config = nodeapi.SnapConfig{Daily: req.Daily, Every: req.Every, Time: req.Time, Keep: req.Keep}
		demoRotate(sl)
		d.sortDemoSnaps(sl)
		writeJSON(w, http.StatusOK, sl)
	})
	h("/api/node/folder/snap/now", func(w http.ResponseWriter, r *http.Request) {
		var req nodeapi.SnapReq
		if !decodePost(w, r, &req) {
			return
		}
		f, ok := d.demoFolder(req.Net, req.ID)
		if !ok {
			replyErr(w, errors.New("нет такой папки"), nil)
			return
		}
		sl := d.demoSnaps(req.Net, f.ID, f.Name)
		now := time.Now()
		sl.Snapshots = append([]nodeapi.Snapshot{{File: demoSnapName(f.Name, now) + "", Time: now.Unix(),
			Size: 21 << 20, Files: 415, Reason: "manual", Seq: 950}}, sl.Snapshots...)
		demoRotate(sl)
		d.sortDemoSnaps(sl)
		writeJSON(w, http.StatusOK, sl)
	})
	h("/api/node/folder/snap/restore", func(w http.ResponseWriter, r *http.Request) {
		var req nodeapi.SnapReq
		if !decodePost(w, r, &req) {
			return
		}
		f, ok := d.demoFolder(req.Net, req.ID)
		if !ok {
			replyErr(w, errors.New("нет такой папки"), nil)
			return
		}
		sl := d.demoSnaps(req.Net, f.ID, f.Name)
		var snap *nodeapi.Snapshot
		for _, s := range sl.Snapshots {
			if s.File == req.Snap && s.Error == "" {
				s := s
				snap = &s
			}
		}
		if snap == nil {
			replyErr(w, errors.New("такого снимка нет"), nil)
			return
		}
		now := time.Now()
		pre := nodeapi.Snapshot{File: demoSnapName(f.Name, now) + "", Time: now.Unix(), Size: 22 << 20, Files: 431, Reason: "pre", Seq: 960}
		sl.Snapshots = append([]nodeapi.Snapshot{pre}, sl.Snapshots...)
		demoRotate(sl)
		d.sortDemoSnaps(sl)
		res := nodeapi.RestoreResult{Changed: 3, Deleted: 19, Added: 1, Pre: pre.File}
		res.Text = fmt.Sprintf("Папка «%s» возвращена к снимку %s: изменено %d, удалено %d, добавлено %d",
			f.Name, time.Unix(snap.Time, 0).Format("02.01.2006 15:04"), res.Changed, res.Deleted, res.Added)
		writeJSON(w, http.StatusOK, res)
	})
}
