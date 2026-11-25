package mobile

// «Ключ сервера изменился» на телефоне (ТЗ 46): тот же /api/hostkey, что на
// ПК, — AndroidApi.kt отдаёт сюда запрос как есть. Файл known_hosts лежит в
// личной папке приложения, человеку до него не добраться — правит его
// программа: «принять новый ключ», «отмена», «забыть ключ».

import (
	"encoding/json"

	"sshtunnel/internal/hostkey"
)

// HostKeyAPI отвечает на /api/hostkey: method — "GET"/"POST", host и port —
// параметры адреса (для GET «ключ этого сервера»), body — тело POST.
// Возвращает JSON; ошибка — {"error":"…"}.
func HostKeyAPI(knownHostsPath, method, host, port, body string) string {
	res, err := hostkey.HandleAPI(knownHostsPath, method,
		map[string]string{"host": host, "port": port}, []byte(body))
	if err != nil {
		res = map[string]string{"error": err.Error()}
	}
	b, _ := json.Marshal(res)
	return string(b)
}
