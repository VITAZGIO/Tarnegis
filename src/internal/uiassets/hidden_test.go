package uiassets

// Сторож [hidden] (ТЗ 51): элемент с атрибутом hidden и своим display во
// встроенном style не прячется никогда — style бьёт правило браузера
// [hidden]{display:none}. Так у пустого «Server 2» висела подпись «выдан
// панелью» от сервера, открытого перед ним. Разрешено, только если скрипт
// страницы сам переключает этому элементу style.display.

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var (
	tagRe     = regexp.MustCompile(`(?s)<[a-zA-Z][a-zA-Z0-9]*\s[^>]*>`)
	hiddenRe  = regexp.MustCompile(`\shidden(\s|>|/|$)`)
	styleDisp = regexp.MustCompile(`style="[^"]*display\s*:`)
	idRe      = regexp.MustCompile(`\sid="([^"]+)"`)
)

func TestHiddenБезВстроенногоDisplay(t *testing.T) {
	err := fs.WalkDir(FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".html") {
			return err
		}
		data, _ := fs.ReadFile(FS(), name)
		src := string(data)
		for _, tag := range tagRe.FindAllString(src, -1) {
			if !hiddenRe.MatchString(tag) || !styleDisp.MatchString(tag) {
				continue
			}
			id := ""
			if m := idRe.FindStringSubmatch(tag); m != nil {
				id = m[1]
			}
			if id != "" && strings.Contains(src, "$('"+id+"').style.display") {
				continue
			}
			t.Errorf("%s: hidden вместе со style display — элемент не спрячется: %s", name, tag)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Форма сервера считает подпись «выдан панелью» и кнопку «Вернуть в список»
// заново из открытого профиля: подпись очищается, кнопка «Удалить сервер»
// есть у любого сервера (ТЗ 51).
func TestФормаСервераБезЧужойПодписи(t *testing.T) {
	data, _ := ReadFile("index.html")
	src := strings.ReplaceAll(string(data), "\r", "")
	body := funcBody(t, src, "function renderPanelServer(")
	for _, want := range []string{"note.hidden = !on", "note.textContent = ''", "hb.hidden = !(on && p.hidden)"} {
		if !strings.Contains(body, want) {
			t.Errorf("renderPanelServer: нет %q", want)
		}
	}
	vis := funcBody(t, src, "function renderGeneralVisibility(")
	if !strings.Contains(vis, "$('removeServerBtn').hidden = !cp.id;") {
		t.Error("«Удалить сервер» снова зависит не только от того, открыт ли профиль")
	}
	if !strings.Contains(src, `<div class="srvActs" id="srvActs">`) {
		t.Error("нет сетки действий сервера")
	}
}

func funcBody(t *testing.T, src, head string) string {
	t.Helper()
	i := strings.Index(src, head)
	if i < 0 {
		t.Fatalf("нет %s", head)
	}
	j := strings.Index(src[i:], "\n}\n")
	if j < 0 {
		t.Fatalf("не нашёл конец %s", head)
	}
	return src[i : i+j]
}
