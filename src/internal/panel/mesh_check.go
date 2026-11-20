package panel

// «Доступ → Проверка» на любом сервере сети (ТЗ 31, часть B).
//
// meshd отвечает только про устройства, которые у него есть: на Amsterdam
// проверка MOBILA → WORK_COMP давала «устройство не найдено», потому что оба
// сидят через Frankfurt. Правила и учётные записи общие для всей сети
// (access, acct:), устройства — в общей картине (srvnet_view.go), поэтому
// панель, когда своему meshd ответить нечего, решает сама тем же правилом,
// что и meshd (policy.match / visible в cmd/meshd/main.go): ответ одинаковый
// на всех серверах.
//
// «Устройство не найдено» — только когда устройства нет ни у одного сервера
// сети.

import (
	"context"

	"sshtunnel/internal/meshsvc"
)

const checkUnknown = "unknown device"

// Check — пустят ли устройство from к устройству to на порт port.
func (m *MeshManager) Check(ctx context.Context, netID, from, to string, port int) (meshsvc.CheckResult, error) {
	res, err := m.admin.Check(ctx, netID, from, to, port)
	if err == nil && res.Reason != checkUnknown {
		return res, nil
	}
	// Своему meshd одного из устройств (или всей сети) не видно — ищем в
	// картине всей сети.
	if got, ok := m.checkMerged(ctx, netID, from, to, port); ok {
		return got, nil
	}
	return res, err
}

// checkEnd — одна сторона проверки.
type checkEnd struct {
	user   string
	server bool
	hold   string
}

// checkMerged решает по общей картине сети и общим правилам. ok=false —
// хотя бы одного из устройств нет ни у одного сервера (или проверка не про
// эту сеть).
func (m *MeshManager) checkMerged(ctx context.Context, netID, from, to string, port int) (meshsvc.CheckResult, bool) {
	if port < 0 || port > 65535 || m.net.srv == nil {
		return meshsvc.CheckResult{}, false
	}
	st := m.State(ctx)
	end := func(id string) (checkEnd, bool) {
		for _, nw := range st.Networks {
			if nw.ID != netID {
				continue
			}
			for _, d := range nw.Devices {
				if d.ID != id {
					continue
				}
				e := checkEnd{user: d.User, server: d.Platform == "server"}
				switch {
				case d.Frozen:
					e.hold = "frozen"
				case d.Pending:
					e.hold = "pending"
				}
				if e.server {
					e.user = GroupServers
				}
				return e, true
			}
		}
		// Сервер сети по своему адресу в сети устройств — как в meshd.
		if st.Self != nil && st.Self.IP != "" && id == st.Self.IP {
			return checkEnd{user: GroupServers, server: true}, true
		}
		for _, s := range st.Servers {
			if s.MeshIP != "" && id == s.MeshIP {
				return checkEnd{user: GroupServers, server: true}, true
			}
		}
		return checkEnd{}, false
	}
	f, ok1 := end(from)
	t, ok2 := end(to)
	if !ok1 || !ok2 {
		return meshsvc.CheckResult{}, false
	}
	p := buildPolicy(m.settings.Get().Access, m.net.srv.clients.List())
	return policyCheck(p, f, t, port), true
}

// policyCheck — то же решение, что meshd check (cmd/meshd/main.go).
func policyCheck(p meshsvc.Policy, f, t checkEnd, port int) meshsvc.CheckResult {
	res := meshsvc.CheckResult{Rule: -1, Visible: policyVisible(p, f.user, t.user)}
	held := f.hold
	if held == "" {
		held = t.hold
	}
	switch {
	case held != "":
		res.Reason, res.Visible = held, false
	case f.server:
		res.Reason = "server cannot initiate"
	case !p.Enabled:
		res.Allowed, res.Reason = true, "policy off"
	default:
		res.Rule = policyMatch(p, f.user, t.user, port)
		if res.Rule >= 0 {
			res.Allowed, res.Reason = true, "rule"
		} else {
			res.Reason = "no rule"
		}
	}
	return res
}

func policyMember(p meshsvc.Policy, user, group string) bool {
	if group == GroupAll {
		return true
	}
	if user == GroupServers {
		return group == GroupServers
	}
	for _, g := range p.Users[user] {
		if g == group {
			return true
		}
	}
	return false
}

func policyMatch(p meshsvc.Policy, from, to string, port int) int {
	if from == GroupServers {
		return -1
	}
	for i, r := range p.Rules {
		if !policyMember(p, from, r.From) || !policyMember(p, to, r.To) {
			continue
		}
		if r.Ports == "" || (port > 0 && meshsvc.PortAllowed(r.Ports, port)) {
			return i
		}
	}
	return -1
}

func policyVisible(p meshsvc.Policy, a, b string) bool {
	if !p.Enabled {
		return true
	}
	for _, r := range p.Rules {
		if (policyMember(p, a, r.From) && policyMember(p, b, r.To)) || (policyMember(p, b, r.From) && policyMember(p, a, r.To)) {
			return true
		}
	}
	return false
}
