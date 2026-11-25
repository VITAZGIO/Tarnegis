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

import "strings"

// GroupPrefix — запись списка «@id» — готовая группа.
const GroupPrefix = "@"

// Group — готовая группа доменов.
type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	NameEn  string   `json:"nameEn"`
	Domains []string `json:"domains"`
}

// groups — готовые группы. Только крупные общие сервисы, без частных сайтов.
var groups = []Group{
	{ID: "vk", Name: "ВКонтакте", NameEn: "VK", Domains: []string{
		"vk.com", "vk.ru", "vkontakte.ru", "vk.me", "vk-portal.net", "vkvideo.ru", "vk-cdn.net",
		"userapi.com", "vkuser.net", "vkuseraudio.net", "vkuservideo.net", "mycdn.me", "okcdn.ru",
	}},
	{ID: "yandex", Name: "Яндекс", NameEn: "Yandex", Domains: []string{
		"yandex.ru", "ya.ru", "yandex.net", "yastatic.net", "yandex.com", "yandex.by", "yandex.kz",
		"yandexcloud.net", "yandex.cloud", "kinopoisk.ru", "kinopoisk.net", "dzen.ru", "dzeninfra.ru",
	}},
	{ID: "gosuslugi", Name: "Госуслуги", NameEn: "Gosuslugi (public services)", Domains: []string{
		"gosuslugi.ru", "gov.ru", "mos.ru", "nalog.ru", "cbr.ru",
	}},
	{ID: "banks", Name: "Банки РФ", NameEn: "Russian banks", Domains: []string{
		"sberbank.ru", "sber.ru", "sberbank.com", "tbank.ru", "tinkoff.ru", "tbank-online.com", "vtb.ru",
		"alfabank.ru", "gazprombank.ru", "raiffeisen.ru", "sovcombank.ru", "open.ru", "psbank.ru",
		"rshb.ru", "pochtabank.ru", "mkb.ru", "nspk.ru",
	}},
	{ID: "mailru", Name: "Mail.ru / OK", NameEn: "Mail.ru / OK", Domains: []string{
		"mail.ru", "ok.ru", "imgsmail.ru", "mradx.net", "mycdn.me", "okcdn.ru", "list.ru", "bk.ru",
		"inbox.ru", "internet.ru", "my.com",
	}},
}

// Groups — готовые группы (копия: экран и API их только показывают).
func Groups() []Group {
	out := make([]Group, len(groups))
	for i, g := range groups {
		g.Domains = append([]string(nil), g.Domains...)
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
