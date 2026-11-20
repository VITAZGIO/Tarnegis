package app

import (
	"net"
	"testing"
	"time"

	"sshtunnel/internal/config"
)

// quitProfile — профиль на свободных локальных портах. Нулевые порты
// программа заменяет на 1080/8080, а предыдущий тест пакета может ещё
// держать их — тест падал с «порт занят программой app.test».
func quitProfile(t *testing.T, addr, keyPath string) config.Profile {
	t.Helper()
	p := profileAt(t, "main", addr, keyPath)
	p.SocksPort, p.HTTPPort = freeLocalPort(t), freeLocalPort(t)
	return p
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitRunning(t *testing.T, a *App, want bool, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if a.Running() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Running() = %v, ждали %v", a.Running(), want)
}

// «Выход» в окне службы (ТЗ 32): туннель выключается, выбор не
// запоминается; окно открылось снова — при «подключаться при запуске»
// туннель поднимается сам.
func TestВыходИзОкнаИСноваОкно(t *testing.T) {
	keyPath, pub := testKeyPath(t)
	addr := newFakeSSHServer(t, pub, true)
	a := New(config.Config{StartMode: config.StartOn,
		Profiles: []config.Profile{quitProfile(t, addr, keyPath)}})
	t.Cleanup(a.Stop)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	a.QuitWindow()
	if a.Running() {
		t.Fatal("после «Выхода» туннель работает")
	}
	if !a.WindowOpened() {
		t.Fatal("окно открылось после «Выхода» — запуск не начат")
	}
	waitRunning(t, a, true, 5*time.Second)
	if a.WindowOpened() {
		t.Fatal("второе открытие окна снова запускает")
	}
}

// Без «Выхода» открытие окна ничего не включает; после «Выхода» и ручного
// включения — тоже (решение уже за человеком).
func TestОкноБезВыходаНичегоНеВключает(t *testing.T) {
	keyPath, pub := testKeyPath(t)
	addr := newFakeSSHServer(t, pub, true)
	a := New(config.Config{StartMode: config.StartOn,
		Profiles: []config.Profile{quitProfile(t, addr, keyPath)}})
	t.Cleanup(a.Stop)
	if a.WindowOpened() {
		t.Fatal("без «Выхода» окно запустило туннель")
	}
	a.QuitWindow()
	if err := a.WantWait(on(), nil); err != nil { // человек включил сам (API)
		t.Fatal(err)
	}
	if a.WindowOpened() {
		t.Fatal("после ручного включения окно запускает ещё раз")
	}
}

// «Не подключаться при запуске» — после «Выхода» окно не включает.
func TestВыходИЗапускВыключен(t *testing.T) {
	keyPath, pub := testKeyPath(t)
	addr := newFakeSSHServer(t, pub, true)
	a := New(config.Config{StartMode: config.StartOff,
		Profiles: []config.Profile{quitProfile(t, addr, keyPath)}})
	t.Cleanup(a.Stop)
	a.QuitWindow()
	if a.WindowOpened() {
		t.Fatal("при «не подключаться» окно запустило туннель")
	}
}
