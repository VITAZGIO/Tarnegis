package mirror

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// Папка программы (ТЗ 39) — видимое человеку место для файлов зеркальных
// папок на этом устройстве:
//
//	<папка программы>/mirrored_folders/<имя папки>/   — новые папки по умолчанию
//	<папка программы>/backups/<имя папки>/<дата>.zip — снимки по умолчанию
//
// Технические имена — по-английски (решение Виталия). Служебные данные
// (индексы, версии, snapshots.json) остаются в данных программы.

const (
	mirroredSub = "mirrored_folders"
	backupsSub  = "backups"
	// casaAppData — общая папка данных приложений CasaOS / ZimaOS.
	casaAppData = "/DATA/AppData"
	// androidRoot — общая память телефона (как localPath).
	androidRoot = "/storage/emulated/0"
)

// programDirFn — папка программы этого устройства; тесты подменяют.
var programDirFn = func() string {
	home, _ := os.UserHomeDir()
	return programDirFor(runtime.GOOS, home, func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && st.IsDir()
	})
}

// programDirFor — папка программы для системы goos: Linux — /DATA/AppData/
// ssh_tunnel, если есть /DATA/AppData, иначе ~/ssh_tunnel; Windows —
// %USERPROFILE%\ssh_tunnel; Android — ssh_tunnel в корне общей памяти
// (особой папки для новых зеркальных папок там не было: они шли в корень).
// "" — домашней папки нет.
func programDirFor(goos, home string, isDir func(string) bool) string {
	const name = "ssh_tunnel"
	switch goos {
	case "android":
		return androidRoot + "/" + name
	case "windows":
		if home == "" {
			return ""
		}
		return strings.TrimRight(home, `\/`) + `\` + name
	}
	if isDir(casaAppData) {
		return casaAppData + "/" + name
	}
	if home == "" {
		return ""
	}
	return path.Join(home, name)
}

// newFolderPath — куда по умолчанию положить новую зеркальную папку name:
// <папка программы>/mirrored_folders/<name>. "" — папки программы нет.
func newFolderPath(progDir, name string) string {
	if progDir == "" {
		return ""
	}
	return filepath.Join(progDir, mirroredSub, safeFileName(name))
}

// backupsDir — папка снимков по умолчанию.
func backupsDir(progDir string) string {
	if progDir == "" {
		return ""
	}
	return filepath.Join(progDir, backupsSub)
}
