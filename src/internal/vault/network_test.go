package vault

import (
	"os"
	"testing"

	"sshtunnel/internal/addons"
)

// ТЗ 33: одно хранилище паролей на сеть.

// device — «другое устройство»: своя папка данных с хранилищем pass и
// записями; возвращает его файл паролей. Состояние пакета общее, поэтому
// устройства проживаются по очереди.
func device(t *testing.T, pass string, es ...Entry) []byte {
	t.Helper()
	addons.New(t.TempDir())
	if err := Create(pass); err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if _, err := Put(e); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path())
	if err != nil {
		t.Fatal(err)
	}
	Lock()
	return raw
}

func init() { kick = func() {} }

func withPeers(t *testing.T, pf ...peerFile) {
	old := fetchPeers
	fetchPeers = func() []peerFile { return pf }
	t.Cleanup(func() { fetchPeers = old })
}

func passOf(t *testing.T, site string) string {
	t.Helper()
	m := Match(site)
	if len(m) != 1 {
		t.Fatalf("%s: %d записей", site, len(m))
	}
	return m[0].Pass
}

// Новое устройство находит хранилище на другом и открывает его тем же
// главным паролем; чужой пароль — отказ, своё хранилище не создаётся.
func TestJoinNetworkVault(t *testing.T) {
	raw := device(t, "общий-пароль-1", Entry{Site: "example.com", Login: "vit", Pass: "секрет"})
	withPeers(t, peerFile{Name: "WORK_COMP", Raw: raw}, peerFile{Name: "ubuntu201", Raw: raw})

	addons.New(t.TempDir()) // новое устройство
	vs := Network()
	if len(vs) != 1 || len(vs[0].Devices) != 2 || vs[0].Devices[0] != "WORK_COMP" {
		t.Fatalf("хранилища в сети: %+v", vs)
	}
	if err := Join(vs[0].ID, "не тот пароль"); err == nil {
		t.Fatal("чужой пароль открыл хранилище сети")
	}
	if exists() {
		t.Fatal("после отказа появилось своё хранилище")
	}
	if err := Join(vs[0].ID, "общий-пароль-1"); err != nil {
		t.Fatal(err)
	}
	defer Lock()
	if p := passOf(t, "example.com"); p != "секрет" {
		t.Fatalf("пароль: %q", p)
	}
	// Дальше — то же хранилище: открывается тем же паролем после закрытия.
	Lock()
	if err := Unlock("общий-пароль-1"); err != nil {
		t.Fatal(err)
	}
	if vs := Network(); len(vs) != 1 || !vs[0].Mine {
		t.Fatalf("своё хранилище в сети не отмечено: %+v", vs)
	}
	if err := Join(vs[0].ID, "общий-пароль-1"); err == nil {
		t.Fatal("join поверх своего хранилища")
	}
}

// Два разных хранилища: на первом сливают второе (его паролем) — записи
// обоих в первом, а устройство со вторым само переходит на первое и
// дальше открывается паролем первого.
func TestMergeTwoVaults(t *testing.T) {
	rawB := device(t, "пароль-второго", Entry{Site: "b.org", Login: "b", Pass: "pb"})
	dirB := t.TempDir()
	addons.New(dirB)
	if err := os.WriteFile(path(), rawB, 0o600); err != nil {
		t.Fatal(err)
	}

	dirA := t.TempDir()
	addons.New(dirA)
	if err := Create("пароль-первого"); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(Entry{Site: "a.org", Login: "a", Pass: "pa"}); err != nil {
		t.Fatal(err)
	}
	withPeers(t, peerFile{Name: "B", Raw: rawB})
	vs := Network()
	if len(vs) != 1 || vs[0].Mine {
		t.Fatalf("второе хранилище не видно чужим: %+v", vs)
	}
	if _, err := Merge(vs[0].ID, "пароль-первого"); err == nil {
		t.Fatal("слияние с неверным паролем второго")
	}
	if _, err := Merge(vs[0].ID, "пароль-второго"); err != nil {
		t.Fatal(err)
	}
	if passOf(t, "a.org") != "pa" || passOf(t, "b.org") != "pb" {
		t.Fatal("после слияния нет записей")
	}
	if vs := Network(); !vs[0].Mine {
		t.Fatalf("слитое хранилище всё ещё чужое: %+v", vs)
	}
	rawA, err := os.ReadFile(path())
	if err != nil {
		t.Fatal(err)
	}
	Lock()

	// Устройство B: открыто своим паролем, синхронизация с A — переход.
	addons.New(dirB)
	withPeers(t, peerFile{Name: "A", Raw: rawA})
	if err := Unlock("пароль-второго"); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(Entry{Site: "c.org", Login: "c", Pass: "pc"}); err != nil {
		t.Fatal(err)
	}
	Sync()
	mu.Lock()
	nt := notice
	mu.Unlock()
	if nt == "" {
		t.Fatal("B не перешёл на общее хранилище")
	}
	Lock()
	if err := Unlock("пароль-второго"); err == nil {
		t.Fatal("после перехода B открывается старым паролем")
	}
	if err := Unlock("пароль-первого"); err != nil {
		t.Fatalf("B не открывается паролем первого: %v", err)
	}
	defer Lock()
	for site, p := range map[string]string{"a.org": "pa", "b.org": "pb", "c.org": "pc"} {
		if got := passOf(t, site); got != p {
			t.Fatalf("%s: %q", site, got)
		}
	}
	rawB2, _ := os.ReadFile(path())
	Lock()

	// A забирает у B новую запись (теперь одно хранилище).
	addons.New(dirA)
	withPeers(t, peerFile{Name: "B", Raw: rawB2})
	if err := Unlock("пароль-первого"); err != nil {
		t.Fatal(err)
	}
	Sync()
	if passOf(t, "c.org") != "pc" {
		t.Fatal("запись B не дошла до A")
	}
	if vs := Network(); len(vs) != 1 || !vs[0].Mine {
		t.Fatalf("после перехода в сети не одно хранилище: %+v", vs)
	}
}

// Цепочка: третье слили во второе, второе — в первое; устройство с третьим
// переходит сразу на первое, даже если устройств со вторым нет в сети.
func TestMergeChain(t *testing.T) {
	raw3 := device(t, "пароль-третьего", Entry{Site: "z.org", Login: "z", Pass: "pz"})
	dir3 := t.TempDir()
	addons.New(dir3)
	_ = os.WriteFile(path(), raw3, 0o600)

	addons.New(t.TempDir())
	if err := Create("пароль-второго"); err != nil {
		t.Fatal(err)
	}
	withPeers(t, peerFile{Name: "C", Raw: raw3})
	if _, err := Merge(Network()[0].ID, "пароль-третьего"); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(path())
	Lock()

	addons.New(t.TempDir())
	if err := Create("пароль-первого"); err != nil {
		t.Fatal(err)
	}
	withPeers(t, peerFile{Name: "B", Raw: raw2})
	if _, err := Merge(Network()[0].ID, "пароль-второго"); err != nil {
		t.Fatal(err)
	}
	raw1, _ := os.ReadFile(path())
	Lock()

	addons.New(dir3)
	withPeers(t, peerFile{Name: "A", Raw: raw1})
	if err := Unlock("пароль-третьего"); err != nil {
		t.Fatal(err)
	}
	Sync()
	Lock()
	if err := Unlock("пароль-первого"); err != nil {
		t.Fatalf("третье не перешло на первое: %v", err)
	}
	defer Lock()
	if passOf(t, "z.org") != "pz" {
		t.Fatal("запись третьего пропала")
	}
}
