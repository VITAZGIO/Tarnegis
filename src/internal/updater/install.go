package updater

// Обновление одной кнопкой (замечание владельца 17, 03.11.2026): скачать
// файл своей системы, сверить с SHA256SUMS.txt релиза, поставить на место
// работающей программы и перезапуститься. Как у панели VPS
// (panel/selfupdate.go), только для программы на компьютере. Где подменить
// себя нельзя (VPN-версия Windows: ядро в службе), CanInstall — false, и
// остаётся «Скачать в Загрузки».

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Prepare скачивает новую версию рядом с работающей программой
// («<программа>.new»), сверяет контрольную сумму и возвращает пути: новый
// файл и сама программа. Ставит файл на место Install (по системам).
func Prepare(ctx context.Context) (newFile, exe string, err error) {
	rel, err := latest(ctx)
	if err != nil {
		return "", "", err
	}
	if Compare(Version, rel.Tag) >= 0 {
		return "", "", fmt.Errorf("уже стоит последняя версия (%s)", strings.TrimPrefix(rel.Tag, "v"))
	}
	return prepareRelease(ctx, *rel)
}

// prepareRelease — файл релиза rel для этой системы рядом с программой
// («<программа>.new»), сверенный с SHA256SUMS.txt того же релиза.
func prepareRelease(ctx context.Context, rel Release) (newFile, exe string, err error) {
	bin, sums, err := releaseFiles(rel)
	if err != nil {
		return "", "", err
	}
	exe, err = os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return "", "", err
	}
	newFile = exe + ".new"
	if _, err := fetchChecked(ctx, bin, sums, newFile); err != nil {
		return "", "", err
	}
	return newFile, exe, nil
}

func fetch(ctx context.Context, url, dst string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Tarnegis/"+Version+" ("+runtime.GOOS+")")
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", fmt.Errorf("не удалось скачать файл: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub ответил %s", resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", fmt.Errorf("нельзя записать рядом с программой (%v) — скачай в «Загрузки»", err)
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 300<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sumOf достаёт из SHA256SUMS.txt сумму файла name.
func sumOf(ctx context.Context, url, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return "", fmt.Errorf("не скачались контрольные суммы: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("контрольные суммы: GitHub ответил %s", resp.Status)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("в контрольных суммах нет файла %s", name)
}

// CleanOld убирает файл прошлой версии, оставшийся после обновления на
// Windows (работающую программу там можно только переименовать).
func CleanOld() {
	if exe, err := os.Executable(); err == nil {
		os.Remove(exe + ".old")
	}
}
