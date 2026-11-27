package invite

// Вход по короткому коду (ТЗ 54): PAKE CPace (filippo.io/cpace, BSD-3,
// Copyright 2019 Google LLC; группа ristretto255 — github.com/gtank/
// ristretto255, BSD-3) внутри SSH-сессии tun_join.
//
//  1. программа → {"v":2,"tag","hk","pake":msgA,"name","type}
//     hk — ключ хоста, который показал ей sshd этой сессии;
//  2. панель → {"ok":true,"pake":msgB,"conf":подтверждение сервера};
//  3. программа сверяет подтверждение (не сошлось — неверный код или
//     подмена сервера, дальше ничего не шлёт) → {"conf":своё};
//  4. панель сверяет и только тогда заводит учётную запись → {"ok","id","name"}.
//
// Ключ хоста входит в контекст PAKE: подменивший сервер со своим ключом
// получает другой общий ключ — подтверждения не сойдутся, а угадать секрет
// он может только одной попыткой на соединение (перебора без сети CPace
// не даёт). Метка tag — 8 бит хеша секрета; секрета до успеха PAKE
// программа не отправляет.

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"

	"filippo.io/cpace"
	"golang.org/x/crypto/ssh"
)

// JoinV2 — версия запроса join по короткому коду.
const JoinV2 = 2

func pakeContext(hostKey ssh.PublicKey, tag string) *cpace.ContextInfo {
	ad := append([]byte("tarnegis invite v2\x00"), hostKey.Marshal()...)
	ad = append(ad, 0)
	ad = append(ad, tag...)
	return cpace.NewContextInfo("tarnegis device", "tarnegis server", ad)
}

func pakeConfirm(key []byte, side string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("tarnegis invite confirm\x00" + side))
	return m.Sum(nil)
}

// PakeClient — сторона программы.
type PakeClient struct {
	st *cpace.State
}

// StartPake — первое сообщение программы.
func StartPake(secret [SecretLen]byte, hostKey ssh.PublicKey, tag string) (msgA []byte, c *PakeClient, err error) {
	msgA, st, err := cpace.Start(string(secret[:]), pakeContext(hostKey, tag))
	if err != nil {
		return nil, nil, err
	}
	return msgA, &PakeClient{st: st}, nil
}

// ErrPakeMismatch — подтверждение не сошлось.
var ErrPakeMismatch = errors.New("сервер не подтвердил код: код неверный или это не тот сервер — подключаться нельзя")

// Finish — ответ панели → подтверждение программы (только если панель
// доказала, что знает тот же секрет и видит тот же ключ хоста).
func (c *PakeClient) Finish(msgB, serverConf []byte) ([]byte, error) {
	key, err := c.st.Finish(msgB)
	if err != nil {
		return nil, ErrPakeMismatch
	}
	if !hmac.Equal(serverConf, pakeConfirm(key, "server")) {
		return nil, ErrPakeMismatch
	}
	return pakeConfirm(key, "device"), nil
}

// PakeRespond — сторона панели: msgB, подтверждение сервера и какое
// подтверждение ждать от программы.
func PakeRespond(secret [SecretLen]byte, hostKey ssh.PublicKey, tag string, msgA []byte) (msgB, serverConf, wantDevice []byte, err error) {
	msgB, key, err := cpace.Exchange(string(secret[:]), pakeContext(hostKey, tag), msgA)
	if err != nil {
		return nil, nil, nil, err
	}
	return msgB, pakeConfirm(key, "server"), pakeConfirm(key, "device"), nil
}
