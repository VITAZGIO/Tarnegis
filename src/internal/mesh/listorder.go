package mesh

import (
	"sort"
	"strings"
)

// Порядок списка устройств на экране (ТЗ 26): сначала серверы сети — все,
// что знает программа, на связи или нет, в том числе достижимые через другой
// сервер (у них Platform "server" и поле relay в списке meshd), — потом
// устройства. Внутри каждой группы — сначала на связи, потом по имени
// (без учёта регистра), при равных именах — по домену и адресу, чтобы
// порядок не прыгал между опросами. Экран только делит готовый список на
// две группы по Platform, сам не сортирует.

// IsServer — запись списка устройств — сервер сети.
func (p Peer) IsServer() bool { return p.Platform == "server" }

// SortForList упорядочивает список на месте так, как его показывает экран.
func SortForList(peers []Peer) {
	sort.SliceStable(peers, func(i, j int) bool {
		a, b := peers[i], peers[j]
		if a.IsServer() != b.IsServer() {
			return a.IsServer()
		}
		if a.Online != b.Online {
			return a.Online
		}
		an, bn := strings.ToLower(listName(a)), strings.ToLower(listName(b))
		if an != bn {
			return an < bn
		}
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		return a.IP.Less(b.IP)
	})
}

// listName — имя, которое видно в строке списка.
func listName(p Peer) string {
	if p.Name != "" {
		return p.Name
	}
	return p.Host
}
