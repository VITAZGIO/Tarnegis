package hostkey

import (
	"os"
	"strconv"
	"strings"
)

// Pin записывает SSH-ключ сервера ("тип ключ") в known_hosts, если про этот
// адрес там ещё ничего нет. Ключ пришёл от сервера сети по SSH (сеть
// серверов, ТЗ 22) — это надёжнее, чем поверить первому соединению. Уже
// записанный ключ не трогает: сверку и смену решает Callback.
// Общий для ПК (internal/app) и телефона (android/core/mobile).
func Pin(path, host string, port int, key string) {
	f := strings.Fields(key)
	if path == "" || host == "" || len(f) < 2 {
		return
	}
	name := host
	if port != 22 && port > 0 {
		name = "[" + host + "]:" + strconv.Itoa(port)
	}
	data, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(data), "\n") {
		if hosts, _, ok := strings.Cut(strings.TrimSpace(l), " "); ok {
			for _, h := range strings.Split(hosts, ",") {
				if h == name {
					return
				}
			}
		}
	}
	line := name + " " + f[0] + " " + f[1] + "\n"
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		line = "\n" + line
	}
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	fh.WriteString(line)
	fh.Close()
}
