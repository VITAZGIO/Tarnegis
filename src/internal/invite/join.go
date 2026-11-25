package invite

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// JoinUser — пользователь SSH, под которым обменивают приглашение.
const JoinUser = "tun_join"

// Dialer открывает TCP до сервера. nil — обычный net.Dialer; на телефоне —
// свой (сокет мимо VPN).
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// JoinRequest / JoinResponse — строка JSON туда и обратно (часть B).
type JoinRequest struct {
	Tok  string `json:"tok"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type JoinResponse struct {
	OK   bool   `json:"ok"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Err  string `json:"err,omitempty"`
}

// JoinError — сервер отказал (код из контракта: expired, used, unknown,
// frozen, limit).
type JoinError struct{ Code string }

func (e *JoinError) Error() string {
	switch e.Code {
	case "expired":
		return ErrExpired.Error()
	case "used":
		return "приглашение уже использовано — попроси в панели новое"
	case "unknown":
		return "сервер не знает этого приглашения: его отозвали или оно с другого сервера"
	case "frozen":
		return "устройство заморожено в панели сервера"
	case "limit":
		return "сервер временно не принимает приглашения (слишком много попыток или устройств) — попробуй позже"
	}
	return "сервер отказал: " + e.Code
}

// ErrOldServer — на сервере нет входа по приглашениям (панель старше ТЗ 47).
var ErrOldServer = errors.New("сервер не принимает приглашения — обнови на нём панель")

// Join обменивает приглашение на учётную запись: вход tun_join своим ключом
// signer, exec «join», одна строка JSON туда и одна обратно. Ключ сервера
// сверяется с ключом из приглашения — другого не примем.
func Join(ctx context.Context, inv Invite, signer ssh.Signer, name, typ string, dial Dialer) (JoinResponse, error) {
	hk, err := inv.HostKey()
	if err != nil {
		return JoinResponse{}, ErrBroken
	}
	cl, err := connect(ctx, dial, inv.Addr(), JoinUser, signer, FixedHostKey(hk), algosFor(hk.Type()))
	if err != nil {
		var ae *authError
		if errors.As(err, &ae) {
			return JoinResponse{}, ErrOldServer
		}
		return JoinResponse{}, err
	}
	defer cl.Close()
	sess, err := cl.NewSession()
	if err != nil {
		return JoinResponse{}, fmt.Errorf("сервер не открыл канал: %w", err)
	}
	defer sess.Close()
	stdin, err := sess.StdinPipe()
	if err != nil {
		return JoinResponse{}, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return JoinResponse{}, err
	}
	if err := sess.Start("join"); err != nil {
		return JoinResponse{}, fmt.Errorf("сервер не выполнил вход по приглашению: %w", err)
	}
	req, _ := json.Marshal(JoinRequest{Tok: inv.Tok, Name: name, Type: typ})
	if _, err := stdin.Write(append(req, '\n')); err != nil {
		return JoinResponse{}, err
	}
	type res struct {
		line []byte
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadBytes('\n')
		ch <- res{line, err}
	}()
	var r res
	select {
	case r = <-ch:
	case <-ctx.Done():
		return JoinResponse{}, errors.New("сервер не ответил на приглашение вовремя")
	}
	stdin.Close()
	if len(strings.TrimSpace(string(r.line))) == 0 {
		if r.err != nil {
			return JoinResponse{}, fmt.Errorf("сервер не ответил на приглашение: %w", r.err)
		}
		return JoinResponse{}, errors.New("сервер не ответил на приглашение")
	}
	var resp JoinResponse
	if err := json.Unmarshal(r.line, &resp); err != nil {
		return JoinResponse{}, fmt.Errorf("непонятный ответ сервера на приглашение: %w", err)
	}
	if !resp.OK {
		return resp, &JoinError{Code: resp.Err}
	}
	if resp.ID == "" || strings.ContainsAny(resp.ID, " /:@\n") {
		return resp, errors.New("сервер не назвал учётную запись устройства")
	}
	return resp, nil
}

// FixedHostKey — проверка ключа сервера: только этот ключ.
func FixedHostKey(want ssh.PublicKey) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if string(key.Marshal()) == string(want.Marshal()) {
			return nil
		}
		return fmt.Errorf("ключ сервера не совпал с ключом из приглашения (сейчас %s) — подключаться нельзя",
			ssh.FingerprintSHA256(key))
	}
}

// algosFor — какие алгоритмы ключа сервера просить, чтобы он показал
// именно ключ этого типа.
func algosFor(t string) []string {
	if t == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{t}
}

// authError — сервер не пустил с этим ключом и пользователем.
type authError struct{ err error }

func (e *authError) Error() string { return e.err.Error() }
func (e *authError) Unwrap() error { return e.err }

// Gone — сервер не знает это устройство: не пустил по его ключу или ответил,
// что такого клиента нет (устройство удалили в панели, ТЗ 49).
func Gone(err error) bool {
	var ae *authError
	return errors.As(err, &ae) || errors.Is(err, ErrUnknownDevice)
}

// ErrUnknownDevice — /api/me/profile: «этот сервер тебя не знает».
var ErrUnknownDevice = errors.New("сервер не знает это устройство")

// connect — SSH до addr. Время — из ctx (по умолчанию 20 с).
func connect(ctx context.Context, dial Dialer, addr, user string, signer ssh.Signer,
	hk ssh.HostKeyCallback, algos []string) (*ssh.Client, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
	}
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("не подключиться к %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:              user,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   hk,
		HostKeyAlgorithms: algos,
		Timeout:           20 * time.Second,
	})
	if err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, &authError{err}
		}
		return nil, fmt.Errorf("SSH к %s: %w", addr, err)
	}
	conn.SetDeadline(time.Time{})
	return ssh.NewClient(cc, chans, reqs), nil
}

func joinHostPort(host string, port int) string {
	if port <= 0 {
		port = 22
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
