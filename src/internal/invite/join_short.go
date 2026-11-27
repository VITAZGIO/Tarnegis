package invite

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"
)

// JoinRequestV2 / первая и вторая строка входа по короткому коду (pake.go).
type joinV2Req struct {
	V    int    `json:"v"`
	Tag  string `json:"tag,omitempty"`
	HK   string `json:"hk,omitempty"`
	Pake []byte `json:"pake,omitempty"`
	Conf []byte `json:"conf,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

type joinV2Resp struct {
	JoinResponse
	Pake []byte `json:"pake,omitempty"`
	Conf []byte `json:"conf,omitempty"`
}

// joinShort — вход по короткому коду: ключа хоста заранее нет, его
// подтверждает PAKE (pake.go). Секрет не уходит на сервер ни в каком виде.
// Возвращает ответ и строку ключа хоста, подтверждённого PAKE.
func joinShort(ctx context.Context, inv Invite, signer ssh.Signer, name, typ string, dial Dialer) (JoinResponse, string, error) {
	var seen ssh.PublicKey
	accept := func(_ string, _ net.Addr, key ssh.PublicKey) error { seen = key; return nil }
	cl, err := connect(ctx, dial, inv.Addr(), JoinUser, signer, accept, nil)
	if err != nil {
		var ae *authError
		if errors.As(err, &ae) {
			return JoinResponse{}, "", ErrOldServer
		}
		return JoinResponse{}, "", err
	}
	defer cl.Close()
	if seen == nil {
		return JoinResponse{}, "", errors.New("сервер не показал свой ключ")
	}
	hkLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(seen)))
	sess, err := cl.NewSession()
	if err != nil {
		return JoinResponse{}, "", fmt.Errorf("сервер не открыл канал: %w", err)
	}
	defer sess.Close()
	stdin, err := sess.StdinPipe()
	if err != nil {
		return JoinResponse{}, "", err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return JoinResponse{}, "", err
	}
	if err := sess.Start("join"); err != nil {
		return JoinResponse{}, "", fmt.Errorf("сервер не выполнил вход по приглашению: %w", err)
	}
	defer stdin.Close()
	rd := bufio.NewReader(stdout)
	msgA, pc, err := StartPake(inv.Secret, seen, inv.Tag)
	if err != nil {
		return JoinResponse{}, "", err
	}
	send := func(r joinV2Req) error {
		data, _ := json.Marshal(r)
		_, err := stdin.Write(append(data, '\n'))
		return err
	}
	if err := send(joinV2Req{V: JoinV2, Tag: inv.Tag, HK: hkLine, Pake: msgA, Name: name, Type: typ}); err != nil {
		return JoinResponse{}, "", err
	}
	first, err := readJoinLine(ctx, rd)
	if err != nil {
		return JoinResponse{}, "", err
	}
	if !first.OK {
		return first.JoinResponse, "", &JoinError{Code: first.Err, Short: true}
	}
	conf, err := pc.Finish(first.Pake, first.Conf)
	if err != nil {
		return JoinResponse{}, "", err
	}
	if err := send(joinV2Req{V: JoinV2, Conf: conf}); err != nil {
		return JoinResponse{}, "", err
	}
	last, err := readJoinLine(ctx, rd)
	if err != nil {
		return JoinResponse{}, "", err
	}
	resp := last.JoinResponse
	if !resp.OK {
		return resp, "", &JoinError{Code: resp.Err, Short: true}
	}
	if resp.ID == "" || strings.ContainsAny(resp.ID, " /:@\n") {
		return resp, "", errors.New("сервер не назвал учётную запись устройства")
	}
	return resp, hkLine, nil
}

// readJoinLine — одна строка ответа join (со сроком из ctx).
func readJoinLine(ctx context.Context, rd *bufio.Reader) (joinV2Resp, error) {
	type res struct {
		line []byte
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := rd.ReadBytes('\n')
		ch <- res{line, err}
	}()
	var r res
	select {
	case r = <-ch:
	case <-ctx.Done():
		return joinV2Resp{}, errors.New("сервер не ответил на приглашение вовремя")
	}
	if len(strings.TrimSpace(string(r.line))) == 0 {
		if r.err != nil {
			return joinV2Resp{}, fmt.Errorf("сервер не ответил на приглашение: %w", r.err)
		}
		return joinV2Resp{}, errors.New("сервер не ответил на приглашение")
	}
	var resp joinV2Resp
	if err := json.Unmarshal(r.line, &resp); err != nil {
		return joinV2Resp{}, fmt.Errorf("непонятный ответ сервера на приглашение: %w", err)
	}
	return resp, nil
}
