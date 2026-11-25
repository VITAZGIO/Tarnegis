package routing

import "testing"

// ТЗ 51: группа «@vk» раскрывается в свои домены, и каждый покрывает
// поддомены (CDN ВКонтакте — sun9-12.userapi.com и т.п.).
func TestГруппаВКонтакте(t *testing.T) {
	d := NewDirectList([]string{"@vk", "example.org"})
	for _, h := range []string{"vk.com", "m.vk.com:443", "sun9-12.userapi.com", "pp.userapi.com", "vkvd123.okcdn.ru",
		"api.vk.me", "i.mycdn.me", "vkuservideo.net", "cs1.vkuseraudio.net", "example.org", "a.example.org"} {
		if !d.Match(h) {
			t.Errorf("%s не напрямую", h)
		}
	}
	for _, h := range []string{"vk.com.evil.example", "google.com", "uservk.com", "@vk"} {
		if d.Match(h) {
			t.Errorf("%s напрямую, а не должен", h)
		}
	}
	if NewDirectList([]string{"@нет-такой"}).Match("нет-такой") {
		t.Error("незнакомая группа совпала как имя")
	}
	if got := CleanEntries([]string{"@banks"}); len(got) < 5 || got[0] != "sberbank.ru" {
		t.Errorf("исключения прокси: %v", got)
	}
	for _, g := range Groups() {
		if g.ID == "" || g.Name == "" || g.NameEn == "" || len(g.Domains) == 0 {
			t.Errorf("группа без имени или доменов: %+v", g)
		}
		if _, ok := GroupByID(GroupPrefix + g.ID); !ok {
			t.Errorf("%s не находится", g.ID)
		}
	}
}
