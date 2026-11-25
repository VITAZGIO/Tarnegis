//go:generate go run geosite_gen.go

package routing

// Готовые группы «всегда напрямую» (ТЗ 51). Сайт — это десяток доменов:
// лента ВКонтакте тянет картинки с userapi.com, видео — с vkuservideo.net и
// okcdn.ru, API — с vk.me. Человек вписывал «vk.com», а остальное уходило
// через сервер, и сайт, который режет чужие адреса, грузился минутами.
//
// Группа в списке — одна запись «@id» (например «@vk»): одним нажатием,
// одной строкой, и раскрывается здесь, в правилах. Каждое имя группы
// покрывает и свои поддомены, как обычная запись. Старая программа примет
// «@vk» за имя, которого не бывает, — ничего не сломается.
//
// Домены групп — не самописные (ТЗ 52): это категории geosite из
// v2fly/domain-list-community, общего списка, которым пользуются Xray,
// v2ray, sing-box и mihomo. Снимок — geosite_data.go, обновляется
// генератором (go generate) вместе с программой.

import "strings"

// GroupPrefix — запись списка «@id» — готовая группа.
const GroupPrefix = "@"

// Group — готовая группа доменов.
type Group struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	NameEn string `json:"nameEn"`
	// Geosite — категории domain-list-community, из которых собрана группа.
	Geosite []string `json:"geosite"`
	// Domains — имена группы (каждое с поддоменами). Правило «full:имя» —
	// только само имя, без поддоменов.
	Domains []string `json:"domains"`
	// Hidden — старый id той же группы: работает в списке, но кнопкой не
	// предлагается.
	Hidden bool `json:"hidden,omitempty"`
}

// groups — готовые группы. Только крупные общие сервисы, без частных сайтов.
var groups = []Group{
	// Вся VK Group (ВКонтакте, OK, Mail.ru, Дзен): лента ВК тянет видео с
	// okcdn.ru и счётчики с mail.ru — так советует и сам список («use
	// geosite:mailru-group to match the entire VK Group»).
	{ID: "vk", Name: "ВКонтакте", NameEn: "VK", Geosite: []string{"mailru-group"}},
	{ID: "yandex", Name: "Яндекс", NameEn: "Yandex", Geosite: []string{"yandex"}},
	{ID: "gosuslugi", Name: "Госуслуги и госсайты", NameEn: "Russian government sites", Geosite: []string{"category-gov-ru"}},
	{ID: "banks", Name: "Банки РФ", NameEn: "Russian banks", Geosite: []string{"category-bank-ru"}},
	{ID: "mailru", Name: "Mail.ru / OK", NameEn: "Mail.ru / OK", Geosite: []string{"mailru-group"}, Hidden: true},
}

func init() {
	for i := range groups {
		groups[i].Domains = GeositeDomains(groups[i].Geosite...)
	}
}

// GeositeDomains — правила категорий geosite из снимка, без повторов и без
// лишних: «full:ads.vk.com» не нужно, когда в группе уже есть «vk.com» со
// всеми поддоменами. Незнакомая категория пуста.
func GeositeDomains(categories ...string) []string {
	domains := map[string]bool{}
	for _, c := range categories {
		for _, d := range geosite[strings.ToLower(c)] {
			if !strings.HasPrefix(d, FullPrefix) {
				domains[d] = true
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range categories {
		for _, d := range geosite[strings.ToLower(c)] {
			if seen[d] {
				continue
			}
			seen[d] = true
			if name, full := strings.CutPrefix(d, FullPrefix); full && coveredBy(name, domains) {
				continue
			}
			if !strings.HasPrefix(d, FullPrefix) && coveredBy(parentOf(d), domains) {
				continue // поддомен уже покрыт родителем
			}
			out = append(out, d)
		}
	}
	return out
}

// FullPrefix — правило «только это имя, без поддоменов» (как full: в
// geosite).
const FullPrefix = "full:"

// parentOf — имя без первой метки ("a.b.c" → "b.c"); у имени из одной метки
// пусто.
func parentOf(name string) string {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return ""
}

// coveredBy — name или один из его родителей есть в domains.
func coveredBy(name string, domains map[string]bool) bool {
	for n := name; n != ""; n = parentOf(n) {
		if domains[n] {
			return true
		}
	}
	return false
}

// GeositeCommit — с какого коммита domain-list-community снят снимок.
func GeositeCommit() string { return geositeCommit }

// Groups — готовые группы (копия: экран и API их только показывают).
func Groups() []Group {
	out := make([]Group, len(groups))
	for i, g := range groups {
		g.Domains = append([]string(nil), g.Domains...)
		g.Geosite = append([]string(nil), g.Geosite...)
		out[i] = g
	}
	return out
}

// GroupByID — группа по id («vk», «@vk» — тоже).
func GroupByID(id string) (Group, bool) {
	id = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), GroupPrefix)
	for _, g := range groups {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

// ExpandGroups — записи списка с раскрытыми группами: «@vk» заменяется её
// доменами, незнакомая группа выпадает, остальное — как есть.
func ExpandGroups(entries []string) []string {
	has := false
	for _, e := range entries {
		if strings.HasPrefix(strings.TrimSpace(e), GroupPrefix) {
			has = true
			break
		}
	}
	if !has {
		return entries
	}
	out := make([]string, 0, len(entries)+16)
	for _, e := range entries {
		if !strings.HasPrefix(strings.TrimSpace(e), GroupPrefix) {
			out = append(out, e)
			continue
		}
		if g, ok := GroupByID(e); ok {
			out = append(out, g.Domains...)
		}
	}
	return out
}
