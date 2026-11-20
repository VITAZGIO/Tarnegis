package panel

// «Подключённые клиенты» глазами всей сети (ТЗ 31, п. 3). Сессии SSH панель
// видит только свои: клиент, который сидит через Frankfurt, на Amsterdam
// числился «ещё не подключался». Устройства сети видны на всех серверах
// (общая картина, srvnet_view.go), и устройство приходит под SSH-пользователем
// своей учётной записи, — по нему клиенту и добавляются сведения сети.
//
// Поля ответа /api/clients (у каждого клиента, только добавлены):
//   - onlineAny — его устройство сейчас на связи хоть с одним сервером сети
//     (или у клиента есть сессия здесь);
//   - viaServer / viaServerId — через какой сервер (имя и id из
//     /api/mesh/state servers[]; "main" — этот сервер);
//   - viaRttMs — задержка устройства до того сервера;
//   - lastSeenAny — когда последний раз был на связи где-либо в сети (unix,
//     0 — нигде не видели): больший из «был здесь» (lastSeenAt) и того, что
//     помнят серверы сети о его устройствах.

import (
	"context"
	"time"
)

// clientNet — сведения сети об одном клиенте.
type clientNet struct {
	OnlineAny   bool    `json:"onlineAny"`
	ViaServer   string  `json:"viaServer,omitempty"`
	ViaServerID string  `json:"viaServerId,omitempty"`
	ViaRTTMs    float64 `json:"viaRttMs,omitempty"`
	LastSeenAny int64   `json:"lastSeenAny,omitempty"`
}

// clientWithNet — клиент в ответе /api/clients.
type clientWithNet struct {
	Client
	clientNet
}

// clientNetOf — сведения сети о клиентах по SSH-пользователю из общей
// картины st.
func clientNetOf(st MeshState) map[string]clientNet {
	names := map[string]string{}
	for _, s := range st.Servers {
		names[s.ID] = s.Name
	}
	out := map[string]clientNet{}
	for _, nw := range st.Networks {
		for _, d := range nw.Devices {
			if d.User == "" {
				continue
			}
			cn := out[d.User]
			if d.LastSeen > cn.LastSeenAny {
				cn.LastSeenAny = d.LastSeen
			}
			if d.Online {
				// Лучший путь — с наименьшей задержкой среди серверов, с
				// которыми устройство на связи.
				for _, c := range d.Conns {
					if !c.Online {
						continue
					}
					if !cn.OnlineAny || c.RTTMs > 0 && (cn.ViaRTTMs == 0 || c.RTTMs < cn.ViaRTTMs) {
						cn.ViaServerID, cn.ViaServer, cn.ViaRTTMs = c.Server, names[c.Server], c.RTTMs
					}
					cn.OnlineAny = true
				}
				if !cn.OnlineAny {
					// Старая картина без conns.
					cn.OnlineAny = true
					cn.ViaServerID, cn.ViaServer, cn.ViaRTTMs = d.Server, names[d.Server], d.RTTMs
				}
			}
			out[d.User] = cn
		}
	}
	return out
}

// withNet добавляет к клиентам сведения сети; selfName — имя этого сервера
// (клиент с сессией здесь, но без устройства в сети — «через него»).
func withNet(list []Client, nets map[string]clientNet, selfName string) []clientWithNet {
	out := make([]clientWithNet, 0, len(list))
	for _, c := range list {
		cn := nets[c.Username]
		if !c.LastSeenAt.IsZero() && c.LastSeenAt.Unix() > cn.LastSeenAny {
			cn.LastSeenAny = c.LastSeenAt.Unix()
		}
		if c.Sessions > 0 {
			if !cn.OnlineAny {
				cn.ViaServerID, cn.ViaServer = "main", selfName
			}
			cn.OnlineAny = true
			cn.LastSeenAny = time.Now().Unix()
		}
		out = append(out, clientWithNet{Client: c, clientNet: cn})
	}
	return out
}

// clientsWithNet — клиенты этого сервера со сведениями сети.
func (s *Server) clientsWithNet(ctx context.Context, list []Client) []clientWithNet {
	if s.mesh == nil {
		return withNet(list, nil, "")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st := s.mesh.State(ctx)
	self := s.mesh.selfName(s.mesh.settings.Get().Mesh)
	return withNet(list, clientNetOf(st), self)
}
