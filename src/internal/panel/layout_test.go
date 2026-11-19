package panel

import (
	"regexp"
	"strings"
	"testing"
)

// ТЗ 23: у каждой вкладки раздела есть свой блок .sub и перевод на обоих
// языках; сетки «карточки в 4–5 колонок» (auto-fill по 420 px) больше нет.
func TestВкладкиРазделовСходятся(t *testing.T) {
	raw, err := pageHTML()
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	if strings.Contains(page, "minmax(min(100%,420px),1fr)") {
		t.Error("вернулась сетка карточек auto-fill 420px")
	}
	secRe := regexp.MustCompile(`(?s)<section id="tab(\w+)".*?</section>`)
	btnRe := regexp.MustCompile(`<button type="button" data-sub="(\w+)" data-i18n="(\w+)">`)
	subRe := regexp.MustCompile(`<div class="sub" data-sub="(\w+)"`)
	bars := 0
	for _, m := range secRe.FindAllStringSubmatch(page, -1) {
		sec := m[0]
		i := strings.Index(sec, `<div class="subtabs" data-tab="`+m[1]+`">`)
		if i < 0 {
			continue
		}
		bars++
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
	if bars != 4 {
		t.Errorf("разделов с вкладками %d, ждали 4 (Доступ, Сети, Настройки, Плагины)", bars)
	}
}
