//go:build !windows

package mirror

import (
	"errors"
	"os"
	"syscall"
)

// openShared — на других системах открытый файл и так не мешает удалению.
func openShared(name string) (*os.File, error) { return os.Open(name) }

// errNoSpace — «на диске нет места» (для тестов снимков).
var errNoSpace error = syscall.ENOSPC

// diskFull — ошибка «нет места на диске».
func diskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
