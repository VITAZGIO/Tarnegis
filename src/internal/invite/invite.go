// Package invite — устройство по приглашению из панели сервера (ТЗ 47/48,
// клиентская сторона). Общий для ПК (internal/app, internal/webui) и
// телефона (android/core/mobile).
//
// Путь устройства:
//  1. человек вставляет текст «sshtun1:…» (Parse — часть A контракта);
//  2. программа создаёт свою пару ключей (NewKey, закрытый ключ не покидает
//     устройство), входит на сервер пользователем tun_join с этим ключом и
//     обменивает одноразовый токен на учётную запись (Join — часть B);
//  3. дальше входит своей учётной записью и читает профиль устройства
//     GET /api/me/profile на 127.0.0.1:47832 сервера (Fetch — часть C):
//     какие серверы ему можно и есть ли у него сеть устройств.
//
// Какие профили завести, убрать, скрыть, решает config.ApplyDevice; здесь —
// только формат и связь.
package invite

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Prefix — начало текста приглашения.
const Prefix = "sshtun1:"

// Invite — приглашение (часть A контракта).
type Invite struct {
	V    int    `json:"v"`
	Host string `json:"host"`
	Port int    `json:"port"`
	// HK — строка ключа SSH сервера («тип base64»): программа записывает его
	// в known_hosts сама, без вопроса «доверять ли этому ключу?».
	HK   string `json:"hk"`
	Tok  string `json:"tok"`
	Exp  int64  `json:"exp"`
	Name string `json:"name,omitempty"`
}

// Ошибки разбора — человеку.
var (
	ErrNotInvite = errors.New("это не приглашение Tarnegis: текст должен начинаться с «sshtun1:»")
	ErrBroken    = errors.New("приглашение повреждено — скопируй его из панели целиком")
	ErrExpired   = errors.New("приглашение истекло — попроси в панели новое")
)

// IsInvite — похож ли текст на приглашение (по нему «Вставить» и сканер
// QR решают, что это не файл сервера).
func IsInvite(text string) bool {
	return strings.Contains(strings.TrimSpace(text), Prefix)
}

// Parse разбирает приглашение. Вокруг текста может быть что угодно
// (скопировали вместе с подписью из письма): берётся первое «sshtun1:…» до
// пробела. Срок не проверяет — это Expired (ошибка «истекло» — отдельная).
func Parse(text string) (Invite, error) {
	text = strings.TrimSpace(text)
	i := strings.Index(text, Prefix)
	if i < 0 {
		return Invite{}, ErrNotInvite
	}
	body := text[i+len(Prefix):]
	if j := strings.IndexAny(body, " \t\r\n\"'<>"); j >= 0 {
		body = body[:j]
	}
	raw, err := decodeB64(body)
	if err != nil {
		return Invite{}, ErrBroken
	}
	var inv Invite
	if err := json.Unmarshal(raw, &inv); err != nil {
		return Invite{}, ErrBroken
	}
	if inv.V != 1 {
		return Invite{}, fmt.Errorf("приглашение версии %d — обнови программу", inv.V)
	}
	inv.Host = strings.TrimSpace(inv.Host)
	if inv.Port == 0 {
		inv.Port = 22
	}
	if inv.Host == "" || strings.ContainsAny(inv.Host, " /@\n") || inv.Port < 1 || inv.Port > 65535 ||
		inv.Tok == "" || inv.Exp <= 0 {
		return Invite{}, ErrBroken
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(inv.HK)); err != nil {
		return Invite{}, ErrBroken
	}
	inv.Name = strings.TrimSpace(inv.Name)
	return inv, nil
}

// Expired — срок приглашения вышел.
func (i Invite) Expired(now time.Time) bool { return now.Unix() >= i.Exp }

// Encode — текст приглашения (так его делает панель; здесь — для тестов и
// поддельного сервера).
func Encode(i Invite) string {
	data, _ := json.Marshal(i)
	return Prefix + base64.RawURLEncoding.EncodeToString(data)
}

// decodeB64 — base64url без дополнения, как в контракте; на всякий случай
// понимает и с дополнением, и обычный base64 (переносчики любят «чинить»).
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// HostKey — ключ сервера из приглашения.
func (i Invite) HostKey() (ssh.PublicKey, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(i.HK))
	return k, err
}

// Addr — «адрес:порт» сервера приглашения.
func (i Invite) Addr() string { return joinHostPort(i.Host, i.Port) }

func parseKeyLine(line string) (ssh.PublicKey, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	return k, err
}

func fingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }
