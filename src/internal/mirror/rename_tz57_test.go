package mirror

// ТЗ 57: сервер, который раздаёт папку, переименовали (и сменился адрес) —
// папка продолжает синхронизироваться в обе стороны, в списке новое имя,
// второй записи не появляется.

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"sshtunnel/internal/addons"
	"sshtunnel/internal/mesh"
)

func TestПереименованиеСервераПапкаИдётДальше(t *testing.T) {
	svc := addons.New(t.TempDir())
	defer svc.Close()
	e := newEnv(t)
	a := e.client("A", ModeBoth)

	var mu sync.Mutex
	peers := []mesh.Peer{{ID: "srv1", Host: "old", Platform: "server", IP: netip.MustParseAddr("198.18.0.9"), Online: true}}
	wasPeers, wasRemote := peersNow, remoteFor
	peersNow = func() []mesh.Peer { mu.Lock(); defer mu.Unlock(); return append([]mesh.Peer(nil), peers...) }
	remoteFor = func(addr string) *Remote {
		mu.Lock()
		defer mu.Unlock()
		if addr == peers[0].IP.String() {
			return &Remote{Base: e.srv.URL, HC: http.DefaultClient}
		}
		return &Remote{Base: "http://127.0.0.1:1", HC: http.DefaultClient} // старый адрес — нет связи
	}
	t.Cleanup(func() { peersNow, remoteFor = wasPeers, wasRemote })

	hub, err := OpenHub(t.TempDir(), "me")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	m := &module{hub: hub, subs: map[string]*runner{}}
	// Папка подключена старой версией: без id сервера.
	r := &runner{Sub: Sub{ID: "s1", Peer: "198.18.0.9", Host: "old", Folder: e.fid, Name: "Тест", Path: a.Path, Mode: ModeBoth}, m: m, rep: a}
	m.subs["s1"] = r
	round := func() {
		t.Helper()
		a.R = remoteFor(r.follow(peersNow()))
		e.settle()
		e.converged()
	}

	e.write(e.root, "до.md", "с сервера")
	e.write(a.Path, "моё.md", "с устройства")
	round()
	if r.PeerID != "srv1" {
		t.Fatalf("id сервера не запомнен: %+v", r.Sub)
	}

	// Переименование и новый адрес.
	mu.Lock()
	peers = []mesh.Peer{{ID: "srv1", Host: "new", Platform: "server", IP: netip.MustParseAddr("198.18.0.20"), Online: true}}
	mu.Unlock()
	e.write(e.root, "после.md", "снова с сервера")
	e.write(a.Path, "моё2.md", "снова с устройства")
	round()
	if !exists(a.Path, "после.md") || !exists(e.root, "моё2.md") {
		t.Fatal("после переименования синхронизация встала")
	}
	if st := a.Status(); st.State != "ok" {
		t.Fatalf("статус %+v", st)
	}
	if r.Host != "new" || r.Peer != "198.18.0.20" {
		t.Fatalf("имя и адрес не обновились: %+v", r.Sub)
	}
	var saved []Sub
	b, _ := os.ReadFile(filepath.Join(dataDir(), "subs.json"))
	if err := json.Unmarshal(b, &saved); err != nil || len(saved) != 1 || saved[0].Host != "new" || saved[0].PeerID != "srv1" {
		t.Fatalf("сохранено: %s", b)
	}

	// Сервер под новым именем — папка уже подключена: не предлагается и не
	// подключается второй раз.
	f := found{Peer: "198.18.0.20", Host: "new", PeerID: "srv1"}
	if !isJoined([]Sub{r.Sub}, f, e.fid) {
		t.Fatal("папка под новым именем показана как не подключённая")
	}
	if _, _, err := m.join(Sub{Peer: f.Peer, Host: f.Host, PeerID: f.PeerID, Folder: e.fid, Path: t.TempDir()}, JoinMerge); err == nil {
		t.Fatal("вторая запись той же папки принята")
	}
	// Другое устройство со старым именем — не та папка.
	if isJoined([]Sub{r.Sub}, found{Peer: "198.18.0.30", Host: "old", PeerID: "other"}, e.fid) {
		t.Fatal("чужой сервер со старым именем принят за свой")
	}
}

func TestLocateПоIDИмениАдресу(t *testing.T) {
	ip := netip.MustParseAddr("198.18.0.5")
	peers := []mesh.Peer{{ID: "x", Host: "now", IP: ip}, {Self: true, ID: "me", Host: "me"}}
	for _, s := range []Sub{{PeerID: "x"}, {Host: "now"}, {Peer: "198.18.0.5"}} {
		if p, ok := locate(s, peers); !ok || p.ID != "x" {
			t.Fatalf("%+v не найден", s)
		}
	}
	// С id — только по id: пропавший сервер не подменяется тёзкой.
	if _, ok := locate(Sub{PeerID: "gone", Host: "now"}, peers); ok {
		t.Fatal("нашёлся по имени при чужом id")
	}
	if got := peerAddr(Sub{PeerID: "gone", Peer: "198.18.0.1"}, peers); got != "198.18.0.1" {
		t.Fatalf("адрес %s", got)
	}
}
