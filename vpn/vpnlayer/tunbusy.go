package vpnlayer

// «sshtun0 занят» (ТЗ 48, увидено 25.11 на сервере с ssh_tunnel_vpn_linux):
// после переподключения прежнее устройство TUN ядро ещё разбирает, и новое
// с тем же именем получает EBUSY. Ждём и пробуем снова до ~3 с; не вышло —
// говорим по-человечески, кем занят интерфейс, а не голым errno.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// busyWait — сколько ждать освобождения интерфейса и с каким шагом.
const (
	busyWait = 3 * time.Second
	busyStep = 150 * time.Millisecond
)

// openRetryBusy зовёт open, пока тот отвечает EBUSY и не вышло время wait.
// sleep подменяется в тестах. Ошибка не EBUSY возвращается сразу.
func openRetryBusy[T any](open func() (T, error), wait, step time.Duration, sleep func(time.Duration)) (T, int, error) {
	tries := 0
	var waited time.Duration
	for {
		tries++
		v, err := open()
		if err == nil || !errors.Is(err, syscall.EBUSY) || waited >= wait {
			return v, tries, err
		}
		sleep(step)
		waited += step
	}
}

// tunHolder ищет процесс, у которого открыто устройство TUN с именем name:
// у такого дескриптора /dev/net/tun в /proc/<pid>/fdinfo/<fd> есть строка
// «iff:\t<name>». root — корень /proc (в тестах — своя папка). Не нашёл —
// pid 0.
func tunHolder(root, name string) (pid int, comm string) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return 0, ""
	}
	self := os.Getpid()
	for _, e := range ents {
		p, err := strconv.Atoi(e.Name())
		if err != nil || p <= 0 || p == self {
			continue
		}
		fdDir := filepath.Join(root, e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || link != "/dev/net/tun" {
				continue
			}
			info, err := os.ReadFile(filepath.Join(root, e.Name(), "fdinfo", fd.Name()))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(info), "\n") {
				k, v, ok := strings.Cut(line, ":")
				if ok && strings.TrimSpace(k) == "iff" && strings.TrimSpace(v) == name {
					c, _ := os.ReadFile(filepath.Join(root, e.Name(), "comm"))
					return p, strings.TrimSpace(string(c))
				}
			}
		}
	}
	return 0, ""
}

// busyError — понятная ошибка «интерфейс занят»: с PID и именем процесса,
// если его удалось найти.
func busyError(name string, pid int, comm string, err error) error {
	switch {
	case pid > 0 && comm != "":
		return fmt.Errorf("интерфейс %s занят другим процессом (PID %d, %s): %w", name, pid, comm, err)
	case pid > 0:
		return fmt.Errorf("интерфейс %s занят другим процессом (PID %d): %w", name, pid, err)
	}
	return fmt.Errorf("интерфейс %s занят другим процессом или ещё не освободился после прошлого запуска: %w", name, err)
}
