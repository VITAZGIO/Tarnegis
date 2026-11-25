package routing

import (
	"slices"
	"strings"
	"testing"
)

// ТЗ 51: группа «@vk» раскрывается в свои домены, и каждый покрывает
// поддомены (CDN ВКонтакте — sun9-12.userapi.com и т.п.).
func TestГруппаВКонтакте(t *testing.T) {
	d := NewDirectList([]string{"@vk", "example.org"})
	for _, h := range []string{"vk.com", "m.vk.com:443", "sun9-12.userapi.com", "pp.userapi.com", "vkvd123.okcdn.ru",
		"api.vk.me", "i.mycdn.me", "vkuservideo.net", "cs1.vkuseraudio.net", "example.org", "a.example.org",
		"vkvideo.ru", "top-fwz1.mail.ru", "dzen.ru"} {
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
	got := CleanEntries([]string{"@banks"})
	if !slices.Contains(got, "sberbank.ru") || !slices.Contains(got, "tbank.ru") {
		t.Errorf("исключения прокси без банков: %d записей", len(got))
	}
	for _, e := range got {
		if strings.Contains(e, ":") {
			t.Errorf("в исключения прокси попало правило с префиксом: %q", e)
		}
	}
	for _, g := range Groups() {
		if g.ID == "" || g.Name == "" || g.NameEn == "" || len(g.Domains) == 0 || len(g.Geosite) == 0 {
			t.Errorf("группа без имени, доменов или категории: %+v", g.ID)
		}
		if _, ok := GroupByID(GroupPrefix + g.ID); !ok {
			t.Errorf("%s не находится", g.ID)
		}
	}
}

// ТЗ 52: группы собраны из категорий geosite (domain-list-community), а не
// вписаны руками: include раскрыт, «full:» — только само имя, лишние правила
// (поддомен при родителе) выкинуты.
func TestГруппыИзGeosite(t *testing.T) {
	if GeositeCommit() == "" {
		t.Fatal("снимок без коммита")
	}
	for _, c := range []string{"mailru-group", "yandex", "category-gov-ru", "category-bank-ru"} {
		if len(geosite[c]) < 20 {
			t.Errorf("категория %s почти пуста: %d", c, len(geosite[c]))
		}
	}
	vk := GeositeDomains("mailru-group")
	// mailru-group включает vk, ok и dzen (include:) — их домены должны быть.
	for _, want := range []string{"vk.com", "userapi.com", "okcdn.ru", "ok.ru", "dzen.ru", "mail.ru"} {
		if !slices.Contains(vk, want) {
			t.Errorf("в mailru-group нет %s", want)
		}
	}
	for _, r := range vk {
		name, full := strings.CutPrefix(r, FullPrefix)
		if full && slices.Contains(vk, parentOf(name)) {
			t.Errorf("лишнее правило %s: родитель уже в группе", r)
		}
	}
	if len(GeositeDomains("нет-такой")) != 0 {
		t.Error("незнакомая категория не пуста")
	}

	// «full:» — без поддоменов.
	d := NewDirectList([]string{"full:a.example", "b.example", ".c.example"})
	for h, want := range map[string]bool{
		"a.example": true, "x.a.example": false,
		"b.example": true, "x.b.example": true,
		"c.example": false, "x.c.example": true, "y.x.c.example": true,
	} {
		if d.Match(h) != want {
			t.Errorf("%s: %v, ждали %v", h, !want, want)
		}
	}
}

// Имена групп заранее не разрешаются (это домены целиком, соединения идут к
// поддоменам) — Names отдаёт только вписанное человеком и спутников.
func TestNamesБезГрупп(t *testing.T) {
	d := NewDirectList([]string{"@vk", "@banks", "home.example.org"})
	names := d.Names()
	if !slices.Contains(names, "home.example.org") {
		t.Errorf("нет вписанного имени: %v", names)
	}
	if len(names) > 5 {
		t.Errorf("Names тащит домены групп: %d", len(names))
	}
}

// Поиск по меткам: длинный список не замедляет проверку.
func BenchmarkMatchГруппы(b *testing.B) {
	d := NewDirectList([]string{"@vk", "@yandex", "@gosuslugi", "@banks", "example.org"})
	for i := 0; i < b.N; i++ {
		d.Match("sun9-12.userapi.com:443")
		d.Match("www.google.com:443")
	}
}
