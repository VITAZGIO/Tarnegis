//go:build windows

package mirror

import (
	"os"
	"strings"
	"syscall"
)

// openShared открывает файл на чтение, не мешая другим программам удалить
// или переименовать его (FILE_SHARE_DELETE). os.Open в Go этого права не
// даёт: пока сверка читала файл (хеш, отправка), Obsidian и проводник не
// могли убрать папку с ним в корзину — «Не удалось переместить в системную
// корзину» (ТЗ 33).
func openShared(name string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(fixLongPath(name))
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(h), name), nil
}

// fixLongPath — путь длиннее MAX_PATH через «\\?\» (os.Open делает так же
// сам; CreateFile напрямую — нет).
func fixLongPath(p string) string {
	if len(p) < 248 || strings.HasPrefix(p, `\\?\`) {
		return p
	}
	p = strings.ReplaceAll(p, "/", `\`)
	if strings.HasPrefix(p, `\\`) {
		return `\\?\UNC\` + p[2:]
	}
	if len(p) >= 3 && p[1] == ':' && p[2] == '\\' {
		return `\\?\` + p
	}
	return p
}
