package panel

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Разделы страницы (ТЗ 25): у каждого свои sections/<имя>.html/.js/.css и
// переводы i18n/<имя>.<язык>.js.
var pageSections = []string{"overview", "devices", "access", "nets", "settings", "plugins", "monitor"}

func pageString(t *testing.T) string {
	t.Helper()
	raw, err := pageHTML()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assetString(t *testing.T, name string) string {
	t.Helper()
	b, err := assets.ReadFile("assets/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ТЗ 23: у каждой вкладки раздела есть свой блок .sub и перевод на обоих
// языках; сетки «карточки в 4–5 колонок» (auto-fill по 420 px) больше нет.
func TestВкладкиРазделовСходятся(t *testing.T) {
	page := pageString(t)
	if strings.Contains(page, "minmax(min(100%,420px),1fr)") {
		t.Error("вернулась сетка карточек auto-fill 420px")
	}
	secRe := regexp.MustCompile(`(?s)<section id="tab(\w+)".*?</section>`)
	btnRe := regexp.MustCompile(`<button type="button" data-sub="(\w+)" data-i18n="(\w+)">`)
	subRe := regexp.MustCompile(`<div class="sub" data-sub="(\w+)"`)
	bars := map[string]bool{}
	for _, m := range secRe.FindAllStringSubmatch(page, -1) {
		sec := m[0]
		i := strings.Index(sec, `<div class="subtabs" data-tab="`+m[1]+`">`)
		if i < 0 {
			continue
		}
		bars[m[1]] = true
		subs := map[string]bool{}
		for _, s := range subRe.FindAllStringSubmatch(sec, -1) {
			subs[s[1]] = true
		}
		for _, b := range btnRe.FindAllStringSubmatch(sec, -1) {
			if !subs[b[1]] {
				t.Errorf("%s: у вкладки %s нет блока", m[1], b[1])
			}
			delete(subs, b[1])
			if strings.Count(page, "\n  "+b[2]+":") < 2 {
				t.Errorf("%s: перевод %s не на обоих языках", m[1], b[2])
			}
		}
		for s := range subs {
			t.Errorf("%s: блок %s без вкладки", m[1], s)
		}
	}
	// ТЗ 28: «Сети» — одна страница без вкладок; «Плагины» решает ТЗ 29.
	if !bars["Access"] || !bars["Settings"] || bars["Mesh"] {
		t.Errorf("разделы с вкладками %v: ждали «Доступ» и «Настройки», «Сети» — без вкладок", bars)
	}
}

// ТЗ 25: рамка одна — ширину содержимого не ограничивает ни раздел, ни
// вкладка (нет колонки 1560 px и узких «форм» 880 px, которые меняла Книга).
func TestРамкаОднойШирины(t *testing.T) {
	page := pageString(t)
	for _, bad := range []string{"max-width:1560px", "section.narrow", "NARROW_SUBS", "classList.toggle('narrow'"} {
		if strings.Contains(page, bad) {
			t.Errorf("на странице снова %q — ширина раздела опять зависит от вкладки", bad)
		}
	}
	if !strings.Contains(page, "scrollbar-gutter:stable") {
		t.Error("нет scrollbar-gutter: ширина прыгнет, когда раздел станет длиннее окна")
	}
	// Тонкие полосы прокрутки: и для Chromium, и для Firefox, без стрелок.
	for _, want := range []string{"::-webkit-scrollbar{width:10px", "::-webkit-scrollbar-button{display:none",
		"scrollbar-width:thin", "scrollbar-color:"} {
		if !strings.Contains(page, want) {
			t.Errorf("полосы прокрутки: нет %q", want)
		}
	}
	// «Мониторинг» — пунктом внизу меню, после «Плагинов» и разделителя.
	nav := page[strings.Index(page, `<nav class="side"`):strings.Index(page, `</nav>`)]
	if p, sep, m := strings.Index(nav, `data-tab="Plugins"`), strings.Index(nav, `side-sep`), strings.Index(nav, `data-tab="Monitor"`); p < 0 || sep < p || m < sep {
		t.Error("«Мониторинг» должен стоять внизу меню, отдельно, после «Плагинов»")
	}
}

// ТЗ 25: у каждого раздела свои файлы, каркас их подключает, и раздел не
// зовёт функции другого раздела напрямую (только через события каркаса).
func TestРазделыВСвоихФайлах(t *testing.T) {
	frame := assetString(t, "index.html")
	if n := strings.Count(frame, "\n"); n > 400 {
		t.Errorf("каркас index.html разросся до %d строк — разделы должны жить в sections/", n)
	}
	defs := map[string]string{} // функция или переменная верхнего уровня → файл
	defRe := regexp.MustCompile(`(?m)^(?:async )?function (\w+)|^(?:const|let) (\w+)`)
	src := map[string]string{}
	for _, s := range pageSections {
		for _, ext := range []string{".html", ".js", ".css"} {
			name := "sections/" + s + ext
			if !strings.Contains(frame, name) {
				t.Errorf("каркас не подключает %s", name)
			}
			body := assetString(t, name)
			if ext == ".js" {
				src[name] = body
				for _, m := range defRe.FindAllStringSubmatch(body, -1) {
					defs[m[1]+m[2]] = name
				}
			}
		}
		for _, lang := range []string{"ru", "en"} {
			if !strings.Contains(frame, "i18n/"+s+"."+lang+".js") {
				t.Errorf("каркас не подключает перевод %s.%s", s, lang)
			}
		}
	}
	for file, body := range src {
		for name, owner := range defs {
			if owner == file {
				continue
			}
			if regexp.MustCompile(`(?:^|[^\w.$'"])` + regexp.QuoteMeta(name) + `\b`).MatchString(body) {
				t.Errorf("%s зовёт %s из %s напрямую — только через onPanel/emitPanel или core.js", file, name, owner)
			}
		}
	}
	// Порядок загрузки: core.js первым, переводы до разделов, main.js последним.
	sc, err := pageScripts()
	if err != nil {
		t.Fatal(err)
	}
	if sc[0] != "look-boot.js" || sc[1] != "core.js" || sc[len(sc)-1] != "main.js" {
		t.Errorf("порядок скриптов: %v", sc)
	}
	lastI18n, firstSec := 0, len(sc)
	for i, s := range sc {
		if strings.HasPrefix(s, "i18n/") {
			lastI18n = i
		}
		if strings.HasPrefix(s, "sections/") && i < firstSec {
			firstSec = i
		}
	}
	if lastI18n > firstSec {
		t.Error("переводы должны идти раньше разделов")
	}
}

// Переводы: у каждого файла-раздела набор ключей ru и en один и тот же, ключ
// живёт ровно в одном разделе.
func TestПереводыRuEnСовпадают(t *testing.T) {
	keyRe := regexp.MustCompile(`(?m)^  (\w+):`)
	owner := map[string]string{}
	for _, f := range pageAssetFiles(".ru.js") {
		if !strings.HasPrefix(f, "i18n/") {
			continue
		}
		en := strings.TrimSuffix(f, ".ru.js") + ".en.js"
		keys := func(name string) map[string]bool {
			out := map[string]bool{}
			for _, m := range keyRe.FindAllStringSubmatch(assetString(t, name), -1) {
				if out[m[1]] {
					t.Errorf("%s: ключ %s дважды", name, m[1])
				}
				out[m[1]] = true
			}
			return out
		}
		ru, eng := keys(f), keys(en)
		for k := range ru {
			if !eng[k] {
				t.Errorf("%s: %s нет в en", f, k)
			}
			if o, ok := owner[k]; ok {
				t.Errorf("ключ %s и в %s, и в %s", k, o, f)
			}
			owner[k] = f
		}
		for k := range eng {
			if !ru[k] {
				t.Errorf("%s: %s нет в ru", en, k)
			}
		}
	}
	if len(owner) < 500 {
		t.Fatalf("подозрительно мало переводов: %d", len(owner))
	}
	// Каждая строка data-i18n на странице переведена.
	page := pageString(t)
	for _, m := range regexp.MustCompile(`data-i18n(?:-ph|-title)?="(\w+)"`).FindAllStringSubmatch(page, -1) {
		if owner[m[1]] == "" {
			t.Errorf("нет перевода для %s", m[1])
		}
	}
}

// Каждый скрипт страницы — целый сам по себе (node --check по файлам).
func TestСкриптыСтраницыЦелые(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node не найден")
	}
	sc, err := pageScripts()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for i, name := range sc {
		body, err := pageFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		f := filepath.Join(dir, strings.ReplaceAll(name, "/", "_"))
		os.WriteFile(f, body, 0o644)
		if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
			t.Errorf("%d %s: %v\n%s", i, name, err, out)
		}
	}
}
