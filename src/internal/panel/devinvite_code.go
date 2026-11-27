package panel

// Вход по короткому коду (ТЗ 54) — сторона панели. Протокол — в
// invite/pake.go: две строки через команду join, между ними сеанс PAKE
// живёт здесь в памяти (sid, минута).
//
// Ограничения:
//   - по адресу — тот же loginLimiter, что у длинного приглашения
//     («join|адрес»), плюс fail2ban по строке отказа;
//   - по приглашению — свой ключ лимитера («joincode|id»: растущая
//     задержка) и счётчик Fails: каждая начатая и не подтверждённая
//     попытка, после maxCodeFails код заблокирован насовсем;
//   - одноразовый и со сроком — как длинное приглашение (takeID).
//
// Ключ хоста: программа присылает тот, что ей показал sshd; панель
// принимает только свой (hostKeys) и кладёт его в контекст PAKE — у
// подменившего сервера со своим ключом подтверждения не сойдутся.

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/invite"
)

const pakeTTL = time.Minute

type pakeSession struct {
	inviteID string
	want     []byte // подтверждение программы
	key      string // ключ входа (SSH_USER_AUTH) первой строки
	from     string
	name     string
	typ      string
	until    time.Time
}

type pakeSessions struct {
	mu   sync.Mutex
	list map[string]*pakeSession
}

func (p *pakeSessions) put(ps *pakeSession) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.list == nil {
		p.list = map[string]*pakeSession{}
	}
	now := time.Now()
	for k, v := range p.list {
		if now.After(v.until) {
			delete(p.list, k)
		}
	}
	sid := randomHex(16)
	p.list[sid] = ps
	return sid
}

// take — сеанс один раз.
func (p *pakeSessions) take(sid string) (*pakeSession, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ps, ok := p.list[sid]
	delete(p.list, sid)
	if !ok || time.Now().After(ps.until) {
		return nil, false
	}
	return ps, true
}

func (s *Server) redeemCode(ctx context.Context, req joinReq, from, key string,
	fail func(code, why string) joinResp) joinResp {
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req.Key)); err != nil {
		return fail(joinUnknown, "нет годного ключа входа")
	}
	if req.Sid != "" {
		return s.redeemCodeConfirm(ctx, req, from, key, fail)
	}
	if len(req.Tag) != 2 || len(req.Pake) == 0 || len(req.Pake) > 256 {
		return fail(joinUnknown, "")
	}
	hk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req.HK))
	if err != nil || !slices.ContainsFunc(s.hostKeys(), func(line string) bool {
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		return err == nil && string(k.Marshal()) == string(hk.Marshal())
	}) {
		// Программе показали не наш ключ — между ней и нами кто-то есть.
		return fail(joinConfirm, "ключ хоста у программы не наш: "+strings.TrimSpace(req.HK))
	}
	inv, code := s.inviteStore().attemptByTag(strings.ToLower(req.Tag))
	if code != "" {
		return fail(code, "")
	}
	invKey := "joincode|" + inv.ID
	if locked, _ := s.limiter.Before(invKey); locked {
		return fail(joinLimit, "")
	}
	raw, err := hex.DecodeString(inv.Secret)
	if err != nil || len(raw) != invite.SecretLen {
		return fail(joinUnknown, "у приглашения нет секрета кода")
	}
	var secret [invite.SecretLen]byte
	copy(secret[:], raw)
	msgB, conf, want, err := invite.PakeRespond(secret, hk, inv.Tag, req.Pake)
	if err != nil {
		s.limiter.Fail(invKey)
		return fail(joinUnknown, "PAKE: "+err.Error())
	}
	// Пока не подтверждено — неудача по приглашению и по адресу (снимет
	// Success после подтверждения).
	s.limiter.Fail(invKey)
	s.limiter.Fail(key)
	sid := s.pake.put(&pakeSession{inviteID: inv.ID, want: want, key: req.Key, from: from,
		name: req.Name, typ: req.Type, until: time.Now().Add(pakeTTL)})
	return joinResp{OK: true, Pake: msgB, Conf: conf, Sid: sid}
}

func (s *Server) redeemCodeConfirm(ctx context.Context, req joinReq, from, key string,
	fail func(code, why string) joinResp) joinResp {
	ps, ok := s.pake.take(req.Sid)
	if !ok || ps.key != req.Key || ps.from != from {
		return fail(joinUnknown, "нет такого сеанса кода")
	}
	if !hmac.Equal(req.Conf, ps.want) {
		return fail(joinConfirm, "")
	}
	inv, code := s.inviteStore().takeID(ps.inviteID)
	if code != "" {
		return fail(code, "")
	}
	s.limiter.Success("joincode|" + inv.ID)
	return s.admit(ctx, inv, joinReq{Name: ps.name, Type: ps.typ, Key: ps.key, From: from}, from, key, fail)
}

// inviteCode — короткий код приглашения (пусто — адрес не помещается).
func inviteCode(host string, port int, d devInvite) string {
	raw, err := hex.DecodeString(d.Secret)
	if err != nil || len(raw) != invite.SecretLen {
		return ""
	}
	var secret [invite.SecretLen]byte
	copy(secret[:], raw)
	code, err := invite.EncodeCode(host, port, secret)
	if err != nil {
		return ""
	}
	return code
}
