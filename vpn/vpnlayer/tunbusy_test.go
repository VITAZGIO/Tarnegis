//go:build windows || linux

package vpnlayer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"sshtunnel/android/core"
)

func TestOpenRetryBusyЖдётОсвобождения(t *testing.T) {
	calls, slept := 0, time.Duration(0)
	v, tries, err := openRetryBusy(func() (int, error) {
		calls++
		if calls < 4 {
			return 0, fmt.Errorf("TUNSETIFF: %w", syscall.EBUSY)
		}
		return 7, nil
	}, 3*time.Second, 150*time.Millisecond, func(d time.Duration) { slept += d })
	if err != nil || v != 7 || tries != 4 || slept != 450*time.Millisecond {
		t.Fatalf("v=%d tries=%d slept=%v err=%v", v, tries, slept, err)
	}
}

func TestOpenRetryBusyСдаётсяЧерез3с(t *testing.T) {
	var slept time.Duration
	_, tries, err := openRetryBusy(func() (int, error) {
		return 0, fmt.Errorf("TUNSETIFF: %w", syscall.EBUSY)
	}, 3*time.Second, 150*time.Millisecond, func(d time.Duration) { slept += d })
	if !errors.Is(err, syscall.EBUSY) || slept != 3*time.Second || tries != 21 {
		t.Fatalf("tries=%d slept=%v err=%v", tries, slept, err)
	}
}

func TestOpenRetryBusyДругаяОшибкаСразу(t *testing.T) {
	_, tries, err := openRetryBusy(func() (int, error) { return 0, errors.New("нет /dev/net/tun") },
		3*time.Second, time.Millisecond, func(time.Duration) { t.Fatal("ждать не надо") })
	if err == nil || tries != 1 {
		t.Fatalf("tries=%d err=%v", tries, err)
	}
}

func TestTunHolderНаходитПроцесс(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("симлинки в папке теста на Windows нужны с правами")
	}
	root := t.TempDir()
	mk := func(pid, fd, link, info, comm string) {
		d := filepath.Join(root, pid)
		os.MkdirAll(filepath.Join(d, "fd"), 0o755)
		os.MkdirAll(filepath.Join(d, "fdinfo"), 0o755)
		if err := os.Symlink(link, filepath.Join(d, "fd", fd)); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "fdinfo", fd), []byte(info), 0o644)
		os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644)
	}
	mk("100", "3", "/dev/null", "pos:\t0\n", "bash")
	mk("200", "5", "/dev/net/tun", "pos:\t0\nflags:\t02\niff:\tother0\n", "wg")
	mk("300", "9", "/dev/net/tun", "pos:\t0\nflags:\t02\niff:\tsshtun0\n", "ssh_tunnel_vpn_")
	pid, comm := tunHolder(root, "sshtun0")
	if pid != 300 || comm != "ssh_tunnel_vpn_" {
		t.Fatalf("pid=%d comm=%q", pid, comm)
	}
	if pid, _ := tunHolder(root, "nobody0"); pid != 0 {
		t.Fatalf("нашёл несуществующий: %d", pid)
	}
}

func TestBusyErrorПонятный(t *testing.T) {
	e := busyError("sshtun0", 42, "foo", syscall.EBUSY)
	if !strings.Contains(e.Error(), "интерфейс sshtun0 занят другим процессом (PID 42, foo)") || !errors.Is(e, syscall.EBUSY) {
		t.Fatal(e)
	}
	if e := busyError("sshtun0", 0, "", syscall.EBUSY); !strings.Contains(e.Error(), "занят другим процессом") {
		t.Fatal(e)
	}
}

// fakeDev — устройство, которое считает закрытия.
type fakeDev struct {
	core.Device
	closed int
}

func (d *fakeDev) Close() { d.closed++ }

func TestUpOwnedЗакрываетУстройствоПриОшибке(t *testing.T) {
	// Стек не поднялся — устройство закрыто ровно раз (даже если стек уже
	// закрыл его сам).
	d := &fakeDev{}
	err := upOwned(d, func(dev core.Device) error { dev.Close(); return errors.New("стек") },
		func() error { t.Fatal("настройка после ошибки стека"); return nil }, func() {})
	if err == nil || d.closed != 1 {
		t.Fatalf("closed=%d err=%v", d.closed, err)
	}
	// Стек поднялся, настройка адаптера не вышла — стек снят, устройство
	// закрыто раз.
	d = &fakeDev{}
	var held core.Device
	stopped := false
	err = upOwned(d, func(dev core.Device) error { held = dev; return nil },
		func() error { return errors.New("маршруты") },
		func() { stopped = true; held.Close() })
	if err == nil || !stopped || d.closed != 1 {
		t.Fatalf("stopped=%v closed=%d err=%v", stopped, d.closed, err)
	}
	// Всё вышло — устройство открыто.
	d = &fakeDev{}
	if err := upOwned(d, func(core.Device) error { return nil }, func() error { return nil }, func() {}); err != nil || d.closed != 0 {
		t.Fatalf("closed=%d err=%v", d.closed, err)
	}
}
