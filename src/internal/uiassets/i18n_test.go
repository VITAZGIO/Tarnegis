package uiassets

// Сторож языка: в скриптах окна и плагинов нет русских строк «в лоб».
// Любой видимый текст идёт через словарь (tr('ключ'), I18N.ru/en) или L(ру, en).
// Сканер маленький: вырезает <script>, обходит JS (строки, шаблоны, комментарии,
// регулярки) и ругается на строку с кириллицей вне разрешённых мест.

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

// Разрешённые строки: названия, которые не переводятся.
var i18nAllow = map[string]bool{
	"Русский": true, "Рус": true, // переключатель языка
}

var scriptRe = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)

type i18nHit struct {
	line int
	text string
}

func hasCyr(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// scanJS возвращает строки с кириллицей, стоящие вне L(…), tr(…) и объекта I18N.
func scanJS(src string) []i18nHit {
	s := &jsScan{src: src}
	s.code(0, false, false)
	return s.hits
}

type jsScan struct {
	src  string
	hits []i18nHit
	prev byte // последний значимый символ кода (для различения регулярки и деления)
}

func isIdent(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func (s *jsScan) line(pos int) int { return 1 + strings.Count(s.src[:pos], "\n") }

func (s *jsScan) hit(start, end int) {
	lit := s.src[start:end]
	if !hasCyr(lit) || i18nAllow[strings.Trim(lit, "`'\" ")] {
		return
	}
	s.hits = append(s.hits, i18nHit{s.line(start), strings.TrimSpace(lit)})
}

// code разбирает код с позиции i. inTpl — мы внутри ${…} шаблона и выходим на
// парной «}». ok — разрешено ли всё внутри (мы в L(…), tr(…) или I18N).
// Возвращает позицию после закрывающей «}» (или конец текста).
func (s *jsScan) code(i int, inTpl bool, ok bool) int {
	src := s.src
	type frame struct {
		close byte
		ok    bool
	}
	var st []frame
	cur := ok
	depth := 0
	var lastIdent string
	i18nNext := false // после «I18N =» ближайшая «{» открывает словарь
	for i < len(src) {
		c := src[i]
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				return len(src)
			}
			i += j + 4
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(src) && src[j] != c && src[j] != '\n' {
				if src[j] == chr92 {
					j++
				}
				j++
			}
			if !cur {
				s.hit(i, min(j+1, len(src)))
			}
			i = j + 1
			s.prev, lastIdent = 'a', ""
		case c == '`':
			i = s.template(i, cur)
			s.prev, lastIdent = 'a', ""
		case c == '/':
			if strings.IndexByte("(,=:[!&|?{};>+-*%<~^", s.prev) >= 0 || s.prev == 0 || lastIdent == "return" {
				j, cls := i+1, false
				for j < len(src) && src[j] != '\n' {
					if src[j] == chr92 {
						j++
					} else if src[j] == '[' {
						cls = true
					} else if src[j] == ']' {
						cls = false
					} else if src[j] == '/' && !cls {
						break
					}
					j++
				}
				i = j + 1
				s.prev, lastIdent = 'a', ""
			} else {
				s.prev, lastIdent = c, ""
				i++
			}
		case isIdent(c):
			j := i
			for j < len(src) && isIdent(src[j]) {
				j++
			}
			lastIdent = src[i:j]
			s.prev = 'a'
			i = j
		case c == '(' || c == '[' || c == '{':
			closeC := map[byte]byte{'(': ')', '[': ']', '{': '}'}[c]
			st = append(st, frame{closeC, cur})
			if c == '(' && (lastIdent == "L" || lastIdent == "tr" || lastIdent == "many" || strings.HasPrefix(src[i+1:], "I18N.")) {
				cur = true
			}
			if (c == '{' || c == '[') && i18nNext {
				cur = true
			}
			if c == '{' {
				depth++
			}
			i18nNext = false
			s.prev, lastIdent = c, ""
			i++
		case c == ')' || c == ']' || c == '}':
			if c == '}' && inTpl && len(st) == 0 {
				return i + 1
			}
			if len(st) > 0 {
				cur = st[len(st)-1].ok
				st = st[:len(st)-1]
			}
			s.prev, lastIdent = c, ""
			i++
		case c == '=' && (lastIdent == "I18N" || lastIdent == "CITIES"): // словарь и список городов с готовым en
			i18nNext = true
			s.prev = c
			i++
		default:
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				s.prev, lastIdent = c, ""
			}
			i++
		}
	}
	_ = depth
	return i
}

// template разбирает `…${код}…`: текст шаблона проверяется как строка, код — как код.
func (s *jsScan) template(i int, ok bool) int {
	src := s.src
	start, j := i, i+1
	var text strings.Builder
	for j < len(src) && src[j] != '`' {
		if src[j] == chr92 {
			j += 2
			continue
		}
		if src[j] == '$' && j+1 < len(src) && src[j+1] == '{' {
			j = s.code(j+2, true, ok)
			text.WriteString(" ")
			continue
		}
		text.WriteByte(src[j])
		j++
	}
	if !ok && hasCyr(text.String()) && !i18nAllow[strings.TrimSpace(text.String())] {
		s.hits = append(s.hits, i18nHit{s.line(start), strings.TrimSpace(text.String())})
	}
	return j + 1
}

// scanPage вытаскивает из HTML встроенные скрипты и сканирует каждый.
func scanPage(page string) []i18nHit {
	var all []i18nHit
	for _, m := range scriptRe.FindAllStringSubmatchIndex(page, -1) {
		if strings.Contains(page[m[2]:m[3]], "src=") {
			continue
		}
		base := strings.Count(page[:m[4]], "\n")
		for _, h := range scanJS(page[m[4]:m[5]]) {
			all = append(all, i18nHit{base + h.line, h.text})
		}
	}
	return all
}

func TestНетРусскогоВКодеИнтерфейса(t *testing.T) {
	check := func(name, src string, isJS bool) {
		var hits []i18nHit
		if isJS {
			hits = scanJS(src)
		} else {
			hits = scanPage(src)
		}
		for _, h := range hits {
			t.Errorf("%s:%d: русская строка вне словаря: %.70s", name, h.line, h.text)
		}
	}
	for _, f := range []string{"index.html", "apps.html", "book.html", "pages.js"} {
		b, err := fs.ReadFile(FS(), f)
		if err != nil {
			t.Fatal(err)
		}
		check(f, string(b), strings.HasSuffix(f, ".js"))
	}
	ents, err := fs.ReadDir(FS(), "addons")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		b, _ := fs.ReadFile(FS(), "addons/"+e.Name())
		check("addons/"+e.Name(), string(b), false)
	}
}

func TestСторожЛовитПодброшенное(t *testing.T) {
	bad := []string{
		`el.textContent = 'Пример';`,
		"el.innerHTML = `<b>Пример ${x}</b>`;",
		`f(L('да','yes'), 'Нет');`,
		`const t = {a: "Привет"};`,
	}
	for _, src := range bad {
		if len(scanJS(src)) == 0 {
			t.Errorf("сторож пропустил: %s", src)
		}
	}
	good := []string{
		`el.textContent = L('Пример','Sample');`,
		`el.textContent = tr('k') + ' ' + L("Пример", ` + "`Sample ${n}`" + `);`,
		`const I18N = {ru:{a:'Да'}, en:{a:'Yes'}}; x = 1;`,
		"// Пример\n/* Пример */ var r = /[а-я]/g; var d = a / b;",
		`el.textContent = 'Русский';`,
		"x = L('Пример', `Sample ${f('Ok')}`);",
	}
	for _, src := range good {
		if h := scanJS(src); len(h) != 0 {
			t.Errorf("сторож ругается зря на %q: %v", src, h)
		}
	}
}

const chr92 = byte(92) // обратная косая
