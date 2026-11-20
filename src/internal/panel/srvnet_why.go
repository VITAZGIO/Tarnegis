package panel

// Почему устройство не на связи с сервером сети (ТЗ 24, п. 13). Только то,
// что серверы действительно знают: версию meshd серверов, адрес сервера,
// учётные записи каждого сервера (хешами в картине), версию программы и
// систему устройства, его профили (op profiles: «адрес:порт»).
//
// Код причины уходит странице, текст — из её таблиц (ru/en).

import (
	"net"
	"strconv"
	"strings"
)

// DevMiss — сервер сети, с которым устройство не на связи, и почему.
type DevMiss struct {
	Server string `json:"server"`
	// Code: server-down — сервер сам не на связи; old-meshd — на сервере,
	// через который устройство узнаёт о других, meshd старше 6 (By — какой);
	// bad-host — у сервера нет адреса, по которому до него дойти; no-account —
	// учётной записи устройства там нет; profile-off — профиль этого сервера
	// на устройстве есть, но не подключён; old-app — программа устройства
	// старше v1.10.1; phone — программа на телефоне старше v1.10.7 (до неё
	// телефон добавлял серверы только вручную); phone-list — телефон v1.10.7+
	// сам добавляет серверы сети, но списка ещё не получил (программу не
	// открывали после появления сервера); unknown — устройство ничего не
	// сообщало.
	Code string `json:"code"`
	By   string `json:"by,omitempty"`
	Host string `json:"host,omitempty"`
}

// whySrv — что известно о сервере сети для объяснения.
type whySrv struct {
	ID     string
	Host   string
	Port   int
	Online bool
	Meshd  string
	// Accts — хеши действующих учётных записей; nil — неизвестно.
	Accts map[string]bool
}

// minNetApp — с какой версии программа на ПК сама добавляет серверы сети.
const minNetApp = "1.10.1"

// minNetPhone — с какой версии телефон сам добавляет серверы сети (ТЗ 26).
const minNetPhone = "1.10.7"

// versionLess — a старше b («1.9.16» < «1.10.1»; «v» и хвост «-beta» не в
// счёт). Не разобрать — false.
func versionLess(a, b string) bool {
	parse := func(s string) ([]int, bool) {
		s = strings.TrimPrefix(strings.TrimSpace(s), "v")
		if i := strings.IndexAny(s, "-+ "); i >= 0 {
			s = s[:i]
		}
		var out []int
		for _, p := range strings.Split(s, ".") {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil, false
			}
			out = append(out, n)
		}
		return out, len(out) > 0
	}
	x, ok1 := parse(a)
	y, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return p < q
		}
	}
	return false
}

// missingServers — серверы сети, с которыми устройство d не на связи, с
// причиной. servers — все серверы сети (этот — с id "main").
func missingServers(d MeshDevice, servers []whySrv) []DevMiss {
	on := map[string]bool{}
	var via []whySrv
	for _, c := range d.Conns {
		if c.Online {
			on[c.Server] = true
		}
	}
	for _, s := range servers {
		if on[s.ID] {
			via = append(via, s)
		}
	}
	uh := userHash(d.User)
	var out []DevMiss
	for _, s := range servers {
		if on[s.ID] {
			continue
		}
		out = append(out, DevMiss{Server: s.ID, Host: s.Host})
		m := &out[len(out)-1]
		m.Code = whyNot(d, s, via, uh, m)
	}
	return out
}

func whyNot(d MeshDevice, s whySrv, via []whySrv, uh string, m *DevMiss) string {
	if !s.Online {
		return "server-down"
	}
	if len(via) == 0 {
		// Не на связи ни с кем — дело не в списке серверов.
		return "unknown"
	}
	for _, v := range via {
		if n, err := strconv.Atoi(v.Meshd); v.Meshd != "" && (err != nil || n < 6) {
			m.By = v.ID
			return "old-meshd"
		}
	}
	if !deviceReachable(s.Host) {
		return "bad-host"
	}
	if s.Accts != nil && uh != "" && !s.Accts[uh] {
		return "no-account"
	}
	port := s.Port
	if port <= 0 {
		port = 22
	}
	key := net.JoinHostPort(strings.ToLower(s.Host), strconv.Itoa(port))
	for _, p := range d.Profiles {
		if strings.EqualFold(p.Key, key) {
			return "profile-off"
		}
	}
	if d.App != "" && versionLess(d.App, minNetApp) {
		return "old-app"
	}
	if d.Platform == "android" {
		if d.App != "" && !versionLess(d.App, minNetPhone) {
			return "phone-list"
		}
		return "phone"
	}
	return "unknown"
}
