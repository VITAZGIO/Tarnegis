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

// ТЗ 27: «Устройства» — две равные половины всегда (никакой узкой боковой
// панели, которая сужает список), справа фирменный фон, пока ничего не
// выбрано; страница раздела не листается — листаются половины.
func TestУстройстваДвеПоловины(t *testing.T) {
	css := assetString(t, "sections/devices.css")
	js := assetString(t, "sections/devices.js")
	html := assetString(t, "sections/devices.html")
	for _, want := range []string{
		".devs{grid-template-columns:minmax(0,1fr) minmax(0,1fr)}",
		"body:has(#tabDevices:not([hidden])){display:flex;flex-direction:column;height:100dvh}",
		"body:has(#tabDevices:not([hidden])) .app{flex:1 1 0;min-height:0}",
		".dscroll{flex:1 1 0;min-height:0;overflow-y:auto",
		".dpane{display:none;flex:1 1 0;min-height:0;flex-direction:column;gap:14px;overflow-y:auto",
		".devs.open .dbrand{display:none}",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("devices.css: нет %q", want)
		}
	}
	for _, bad := range []string{"460px", "position:sticky", "position:fixed"} {
		if strings.Contains(css, bad) {
			t.Errorf("devices.css: снова %q — правая колонка опять живёт отдельно от левой", bad)
		}
	}
	if !strings.Contains(js, "$('devBrand').appendChild(brandBlock())") {
		t.Error("devices.js: справа нет фирменного фона")
	}
	if strings.Contains(js, "WIDE_DETAIL") || strings.Contains(js, "DEV_SEL = list[0]") {
		t.Error("devices.js: снова сам открывает первое устройство — фон не виден")
	}
	for _, id := range []string{`id="devBrand"`, `id="devPane"`, `id="devCloseBtn"`, `id="devBackBtn"`, `id="devScroll"`} {
		if !strings.Contains(html, id) {
			t.Errorf("devices.html: нет %s", id)
		}
	}
	if strings.Contains(html, "devMapSlot") {
		t.Error("devices.html: вернулось место под схему")
	}
}

// ТЗ 27: «Обзор» на широком экране — плитки | схема | клиенты в первом
// экране, схема не шире двух высот; ниже состояние сервера и трафик по
// трети; состояние сервера — из /api/server/stats со ссылкой в «Мониторинг».
func TestОбзорШирокийЭкран(t *testing.T) {
	css := assetString(t, "sections/overview.css")
	js := assetString(t, "sections/overview.js")
	// ТЗ 30: клиенты помещаются целиком (не уже 540 px), схема не уже
	// 640 px; где обоим не хватает места — раскладка стопкой.
	wide := css[strings.Index(css, "@media (min-width:1680px)"):]
	for _, want := range []string{
		`grid-template-areas:"tiles map clients"`,
		"grid-template-columns:210px minmax(640px,200cqh) minmax(540px,1fr)",
		"container-type:size",
		".ov-screen{height:calc(100dvh - var(--ov-top, 69px) - 14px)}",
		".ov-bot{grid-template-columns:repeat(3,minmax(0,1fr))}",
		".ov.nomap .ov-top{container-type:normal}",
	} {
		if !strings.Contains(wide, want) {
			t.Errorf("overview.css (от 1280 px): нет %q", want)
		}
	}
	// Телефон: плитки, схема, клиенты, трафик, состояние сервера.
	for _, want := range []string{`grid-template-areas:"tiles" "map" "clients"`, `grid-template-areas:"traffic" "status"`} {
		if !strings.Contains(css, want) {
			t.Errorf("overview.css (телефон): нет %q", want)
		}
	}
	if strings.Contains(css, "@media (min-width:1280px)") {
		t.Error("overview.css: снова раскладка в ряд с 1280 px — клиенты не помещаются")
	}
	if !strings.Contains(css, ".ov-clients .ov-scroll{overflow-x:hidden}") {
		t.Error("overview.css: таблица клиентов снова листается вбок")
	}
	for _, want := range []string{"api('/api/server/stats')", "showTab('Monitor')", "onPanel('show:Home'", "cell(row, clientLink(c), 'link')"} {
		if !strings.Contains(js, want) {
			t.Errorf("overview.js: нет %q", want)
		}
	}
}

// ТЗ 30: название раздела — в шапке по центру, с «?» (пояснение и ссылка
// на главу Книги). Старого заголовка и серой строки под ним в разделах нет.
func TestНазваниеРазделаВШапке(t *testing.T) {
	frame := assetString(t, "index.html")
	top := frame[strings.Index(frame, `<header class="topbar">`):strings.Index(frame, `</header>`)]
	for _, want := range []string{`id="pageHead"`, `id="pageTitle"`, `class="hint pt-help" id="pageHelp"`} {
		if !strings.Contains(top, want) {
			t.Errorf("шапка: нет %s", want)
		}
	}
	for _, s := range pageSections {
		html := assetString(t, "sections/"+s+".html")
		for _, bad := range []string{`class="sec-head"`, `class="page-title"`, `class="page-sub"`} {
			if strings.Contains(html, bad) {
				t.Errorf("sections/%s.html: снова старый заголовок раздела (%s)", s, bad)
			}
		}
	}
	page := pageString(t)
	for _, bad := range []string{".sec-head{", ".page-sub{", "'page-title'"} {
		if strings.Contains(page, bad) {
			t.Errorf("на странице снова %q", bad)
		}
	}
	// У каждого раздела — название и пояснение в PAGE_HEAD, ключи переведены
	// на оба языка.
	core := assetString(t, "core.js")
	i := strings.Index(core, "const PAGE_HEAD = {")
	if i < 0 {
		t.Fatal("core.js: нет PAGE_HEAD")
	}
	head := core[i : i+strings.Index(core[i:], "};")]
	rowRe := regexp.MustCompile(`(\w+):\s*\{title: '(\w+)',\s*help: \[([^\]]+)\]\}`)
	got := map[string]bool{}
	for _, m := range rowRe.FindAllStringSubmatch(head, -1) {
		got[m[1]] = true
		keys := []string{m[2]}
		for _, k := range regexp.MustCompile(`'(\w+)'`).FindAllStringSubmatch(m[3], -1) {
			keys = append(keys, k[1])
		}
		if len(keys) < 2 {
			t.Errorf("PAGE_HEAD.%s: нет пояснения", m[1])
		}
		for _, k := range keys {
			if strings.Count(page, "\n  "+k+":") < 2 {
				t.Errorf("PAGE_HEAD.%s: ключ %s не переведён на оба языка", m[1], k)
			}
		}
	}
	tabs := regexp.MustCompile(`const TABS = \[([^\]]+)\]`).FindStringSubmatch(core)
	if tabs == nil {
		t.Fatal("core.js: нет TABS")
	}
	for _, m := range regexp.MustCompile(`'(\w+)'`).FindAllStringSubmatch(tabs[1], -1) {
		if !got[m[1]] {
			t.Errorf("раздел %s без названия в шапке (PAGE_HEAD)", m[1])
		}
	}
	for _, want := range []string{"renderPageHead();", "help.dataset.hints"} {
		if !strings.Contains(core, want) {
			t.Errorf("core.js: нет %q", want)
		}
	}
}

// ТЗ 30: «Скопировано» не сдвигает соседей — облачко поверх страницы, а не
// элемент рядом со значением.
func TestСкопированоНичегоНеДвигает(t *testing.T) {
	core := assetString(t, "core.js")
	css := assetString(t, "core.css")
	if strings.Contains(core, "node.after(tip)") {
		t.Error("core.js: «Скопировано» снова вставляется рядом со значением")
	}
	if !strings.Contains(core, "function copyFlash(anchor, ok, place)") || !strings.Contains(core, "document.body.appendChild(tip)") {
		t.Error("core.js: нет copyFlash с облачком в <body>")
	}
	if !strings.Contains(css, ".copy-tip{position:fixed;") {
		t.Error("core.css: облачко «Скопировано» не поверх страницы")
	}
	if strings.Contains(assetString(t, "sections/devices.js"), "showMsg($('clientConfigMsg'), t('copied')") {
		t.Error("devices.js: «Скопировано» снова строкой в карточке — она прыгает")
	}
}

// ТЗ 30: «Устройства» — крестик в рамке карточки, правая половина — одна
// скруглённая рамка, верх на уровне списка.
func TestУстройстваКрестикВРамке(t *testing.T) {
	css := assetString(t, "sections/devices.css")
	for _, want := range []string{
		".dpane{position:relative;gap:0;padding:0;background:var(--card);border:1px solid var(--line);border-radius:12px;",
		".dbar{position:absolute;",
		"#devicesMsg:empty{display:none}",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("devices.css: нет %q", want)
		}
	}
}

// ТЗ 30: «Сети» — слева серверы и сети, справа прогноз в рамке инструкции и
// сервер в сети; двух вариантов ТЗ 28 больше нет.
func TestСетиРаскладкаВиталия(t *testing.T) {
	html := assetString(t, "sections/nets.html")
	css := assetString(t, "sections/nets.css")
	js := assetString(t, "sections/nets.js")
	main := html[strings.Index(html, `<div class="nets-main">`):strings.Index(html, `<aside class="nets-side">`)]
	side := html[strings.Index(html, `<aside class="nets-side">`):]
	if a, b := strings.Index(main, `id="meshServersCard"`), strings.Index(main, `id="meshNets"`); a < 0 || b < a {
		t.Error("nets.html: слева должны быть «Серверы сети», под ними сети")
	}
	if a, b := strings.Index(side, `class="card instr" id="meshForecastCard"`), strings.Index(side, `id="meshSelfCard"`); a < 0 || b < a {
		t.Error("nets.html: справа должны быть прогноз (рамка инструкции), под ним «Сервер в сети устройств»")
	}
	for _, want := range []string{
		"grid-template-columns:minmax(0,3fr) minmax(0,2fr)",
		"@media (min-width:1920px){\n  .nets-page{grid-template-columns:minmax(0,2fr) minmax(0,1fr)}",
		"#meshForecastCard{order:4}",
	} {
		if !strings.Contains(strings.ReplaceAll(css, "\r\n", "\n"), want) {
			t.Errorf("nets.css: нет %q", want)
		}
	}
	for _, bad := range []string{"NETS_V", "srvStrip", "netTabs", "nets-v1", "nets-v2", "get('v')"} {
		if strings.Contains(html+css+js, bad) {
			t.Errorf("«Сети»: остался вариант ТЗ 28 (%s)", bad)
		}
	}
	core := assetString(t, "core.css")
	for _, want := range []string{".card.instr{", ".card.instr::before{content:\"i\"", ".card.instr > h1{color:var(--accent)}"} {
		if !strings.Contains(core, want) {
			t.Errorf("core.css: рамка инструкции — нет %q", want)
		}
	}
}

// ТЗ 34: в «Настройках» карточки не перескакивают из столбца в столбец,
// когда появляется журнал установки: столбцы постоянные, журнал — внутри
// карточки с кнопкой, отдельной карточки журнала нет.
func TestНастройкиЖурналНеДвигаетКарточки(t *testing.T) {
	css := assetString(t, "sections/settings.css")
	if strings.Contains(css, "columns:2") {
		t.Error("settings.css: снова газета columns:2 — карточки прыгают по столбцам")
	}
	if !strings.Contains(css, "grid-template-columns:minmax(0,1fr) minmax(0,1fr)") {
		t.Error("settings.css: нет постоянных двух столбцов .set-col")
	}
	for _, f := range []string{"sections/settings.html", "sections/plugins.html"} {
		html := assetString(t, f)
		for _, bad := range []string{`id="meshJobCard"`, `id="nodeJobCard"`} {
			if strings.Contains(html, bad) {
				t.Errorf("%s: снова отдельная карточка журнала %s", f, bad)
			}
		}
	}
	if !strings.Contains(assetString(t, "sections/settings.js"), "host.appendChild(box)") ||
		!strings.Contains(assetString(t, "sections/plugins.js"), "host.appendChild(box)") {
		t.Error("журнал установки не переносится в карточку с кнопкой")
	}
}

// ТЗ 34: «Устройства» — адрес и имя в сети копируются каждый своим щелчком,
// «Скопировано» в пустом месте строки, без пунктира; значки систем из Simple
// Icons; пустая правая половина — сплошная карточка с одним знаком VG.
func TestУстройстваКопированиеЗначкиФон(t *testing.T) {
	core := assetString(t, "core.js")
	css := assetString(t, "core.css")
	js := assetString(t, "sections/devices.js")
	dcss := assetString(t, "sections/devices.css")
	if !strings.Contains(css, ".copy{cursor:copy}\n") {
		t.Error("core.css: у копируемых значений снова пунктир")
	}
	for _, want := range []string{"const OS_ICON = {", "windows:'M0,0H11.377", "function osIcon(platform)"} {
		if !strings.Contains(core, want) {
			t.Errorf("core.js: нет %q", want)
		}
	}
	for _, want := range []string{"{hint: 'copy_name_hint', place}", "{hint: 'copy_ip_hint', place}", "osIcon(itemPlatform(it))"} {
		if !strings.Contains(js, want) {
			t.Errorf("devices.js: нет %q", want)
		}
	}
	for _, want := range []string{
		".dbrand .brand-bg{min-height:0;background:var(--card);border:1px solid var(--line)}",
		".dbrand .brand-bg > svg:first-child:not(.brand-vg){display:none}",
	} {
		if !strings.Contains(dcss, want) {
			t.Errorf("devices.css: нет %q", want)
		}
	}
}
