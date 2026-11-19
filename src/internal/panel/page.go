package panel

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"sshtunnel/internal/uiassets"
)

// Страница панели собрана из файлов (ТЗ 25): assets/index.html — каркас
// (шапка, боковое меню, вход), у каждого раздела свои
// assets/sections/<имя>.html, .js и .css, общее — assets/core.js и core.css,
// переводы — assets/i18n/<раздел>.<язык>.js. Сборки при сборке программы нет:
// сервер один раз склеивает каркас и файлы в одну страницу и отдаёт её как
// раньше — одним ответом, без отдельных запросов за скриптами.
//
// В каркасе:
//   - <link rel="stylesheet" href="X"> — вставить X внутрь <style>;
//   - <script src="X"></script> — вставить X внутрь <script> (порядок тегов —
//     единственный «загрузчик»: что выше, то выполняется раньше);
//   - <!--#include X--> — вставить X как есть (разметка раздела);
//   - путь «ui/…» берётся из общих файлов программы (internal/uiassets):
//     так у панели те же темы и тот же знак VG, что у программы.

var (
	pageLinkRe    = regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)">`)
	pageScriptRe  = regexp.MustCompile(`<script src="([^"]+)"></script>`)
	pageIncludeRe = regexp.MustCompile(`<!--#include ([^ ]+?)-->`)

	pageOnce sync.Once
	pageData []byte
	pageErr  error
)

// pageFile читает файл страницы: «ui/…» — из файлов программы, остальное —
// из assets панели.
func pageFile(name string) ([]byte, error) {
	if strings.Contains(name, "..") {
		return nil, fmt.Errorf("страница: недопустимый путь %q", name)
	}
	if rest, ok := strings.CutPrefix(name, "ui/"); ok {
		return uiassets.ReadFile(rest)
	}
	return assets.ReadFile(path.Join("assets", name))
}

// buildPage склеивает страницу. Ошибка — в каркасе ссылка на файл, которого
// нет, или в скрипте встретился закрывающий тег.
func buildPage() ([]byte, error) {
	frame, err := assets.ReadFile("assets/index.html")
	if err != nil {
		return nil, err
	}
	var bad error
	put := func(re *regexp.Regexp, src []byte, wrap func(name string, body []byte) ([]byte, error)) []byte {
		return re.ReplaceAllFunc(src, func(m []byte) []byte {
			name := string(re.FindSubmatch(m)[1])
			body, err := pageFile(name)
			if err == nil {
				body, err = wrap(name, body)
			}
			if err != nil && bad == nil {
				bad = fmt.Errorf("страница: %s: %w", name, err)
			}
			return body
		})
	}
	out := put(pageIncludeRe, frame, func(_ string, b []byte) ([]byte, error) { return b, nil })
	out = put(pageLinkRe, out, func(name string, b []byte) ([]byte, error) {
		if bytes.Contains(bytes.ToLower(b), []byte("</style")) {
			return nil, fmt.Errorf("в стилях встретился </style>")
		}
		return []byte("<style>/* " + name + " */\n" + string(b) + "</style>"), nil
	})
	out = put(pageScriptRe, out, func(name string, b []byte) ([]byte, error) {
		if bytes.Contains(bytes.ToLower(b), []byte("</script")) {
			return nil, fmt.Errorf("в скрипте встретился </script>")
		}
		// sourceURL — чтобы в инструментах браузера ошибка показывала свой
		// файл и строку, а не строку склеенной страницы.
		return []byte("<script>\n" + string(b) + "//# sourceURL=" + name + "\n</script>"), nil
	})
	if bad != nil {
		return nil, bad
	}
	return out, nil
}

// pageHTML — собранная страница (склеивается один раз).
func pageHTML() ([]byte, error) {
	pageOnce.Do(func() { pageData, pageErr = buildPage() })
	return pageData, pageErr
}

// servePage отдаёт страницу панели; общая для настоящей панели и показа.
func servePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := pageHTML()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

// pageScripts — имена скриптов страницы в порядке загрузки (для тестов).
func pageScripts() ([]string, error) {
	frame, err := assets.ReadFile("assets/index.html")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range pageScriptRe.FindAllSubmatch(frame, -1) {
		out = append(out, string(m[1]))
	}
	return out, nil
}

// pageAssetFiles — все файлы assets с данным окончанием (для тестов).
func pageAssetFiles(suffix string) []string {
	var out []string
	fs.WalkDir(assets, "assets", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, suffix) {
			out = append(out, strings.TrimPrefix(p, "assets/"))
		}
		return nil
	})
	sort.Strings(out)
	return out
}
