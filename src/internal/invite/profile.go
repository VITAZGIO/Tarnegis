package invite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/config"
	"sshtunnel/internal/hostkey"
)

// SelfAddr — вход «для себя» на сервере (internal/panel/selfservice.go):
// доступен устройству через его же SSH, вызывающий узнаётся по uid сокета.
const SelfAddr = "127.0.0.1:47832"

// ProfilePath — профиль устройства (часть C контракта).
const ProfilePath = "/api/me/profile"

// Server — сервер, на который устройству можно.
type Server struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
	HK   string `json:"hk"`
	Flag string `json:"flag,omitempty"`
}

// Profile — профиль устройства от панели. Незнакомые поля не мешают.
type Profile struct {
	V       int      `json:"v"`
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Rev     int64    `json:"rev"`
	Servers []Server `json:"servers"`
	// Mesh — nil у профиля без поля: старая учётная запись (часть D) — сеть есть.
	Mesh   *bool `json:"mesh"`
	Frozen bool  `json:"frozen"`
	// MeshKey — ключ сети панели (ТЗ 49): устройство из панели — только в
	// ней. Нет (панель старше) — сети устройств у устройства нет.
	MeshKey string `json:"meshKey,omitempty"`
	// Ver — версия панели, отдавшей профиль (ТЗ 50); нет — панель старше.
	Ver string `json:"ver,omitempty"`
	// From — «адрес:порт» сервера, с которого прочитан профиль (не из JSON).
	From string `json:"-"`
}

// ErrNoProfile — сервер не знает /api/me/profile (панель старше ТЗ 47).
var ErrNoProfile = errors.New("сервер не отдаёт профиль устройства — на нём старая версия панели")

// ParseProfile разбирает ответ /api/me/profile.
func ParseProfile(data []byte) (Profile, error) {
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return Profile{}, fmt.Errorf("профиль устройства не разобран: %w", err)
	}
	if p.V != 1 {
		return Profile{}, fmt.Errorf("профиль устройства версии %d — обнови программу", p.V)
	}
	if p.ID == "" {
		return Profile{}, errors.New("в профиле устройства нет учётной записи")
	}
	out := p.Servers[:0]
	for _, s := range p.Servers {
		s.Host = strings.TrimSpace(s.Host)
		if s.Port == 0 {
			s.Port = 22
		}
		if s.Host == "" || strings.ContainsAny(s.Host, " /@\n") || s.Port < 1 || s.Port > 65535 {
			continue
		}
		out = append(out, s)
	}
	p.Servers = out
	return p, nil
}

// MeshAllowed — пустил ли сервер устройство в сеть устройств.
func (p Profile) MeshAllowed() bool { return p.Mesh == nil || *p.Mesh }

// Config — профиль в том виде, в каком его применяет config.ApplyDevice.
func (p Profile) Config() config.DeviceProfile {
	dp := config.DeviceProfile{ID: p.ID, Name: p.Name, Rev: p.Rev, Mesh: p.MeshAllowed(), Frozen: p.Frozen,
		MeshKey: p.MeshKey, From: p.From, Ver: p.Ver}
	for _, s := range p.Servers {
		dp.Servers = append(dp.Servers, config.DeviceServer{Name: s.Name, Host: s.Host, Port: s.Port,
			HostKey: s.HK, Flag: FlagEmoji(s.Flag)})
	}
	return dp
}

// FlagEmoji — код страны «de» флагом-эмодзи (так флаг хранит профиль).
// Уже эмодзи или пусто — как есть.
func FlagEmoji(code string) string {
	code = strings.TrimSpace(code)
	if len(code) != 2 || utf8.RuneCountInString(code) != 2 {
		return code
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		if r < 'A' || r > 'Z' {
			return ""
		}
		b.WriteRune(0x1F1E6 + (r - 'A'))
	}
	return b.String()
}

// FetchVia читает профиль через dial — соединение с адресом на сервере
// (SSH-канал уже поднятого туннеля или свой, см. Fetch).
func FetchVia(ctx context.Context, dial func(network, addr string) (net.Conn, error)) (Profile, error) {
	tr := &http.Transport{
		DialContext:       func(context.Context, string, string) (net.Conn, error) { return dial("tcp", SelfAddr) },
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+SelfAddr+ProfilePath, nil)
	if err != nil {
		return Profile{}, err
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return Profile{}, fmt.Errorf("профиль устройства не прочитан: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Profile{}, ErrNoProfile
	case resp.StatusCode == http.StatusForbidden:
		return Profile{}, fmt.Errorf("профиль устройства: %w", ErrUnknownDevice)
	case resp.StatusCode != http.StatusOK:
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return Profile{}, fmt.Errorf("профиль устройства: %s", e.Error)
		}
		return Profile{}, fmt.Errorf("профиль устройства: сервер ответил %s", resp.Status)
	}
	return ParseProfile(body)
}

// Target — сервер и вход устройства для своего SSH-соединения.
type Target struct {
	Host    string
	Port    int
	User    string
	Signer  ssh.Signer
	HostKey ssh.HostKeyCallback
	Dial    Dialer
	// Algos — какие типы ключа сервера просить. Пусто — по KnownHosts:
	// типы, что там записаны (hostkey.Algorithms). Без этого Go-клиент
	// просит сначала ECDSA, сервер его и показывает, а записан ed25519 из
	// приглашения — и вход срывается «ключ изменился» (ТЗ 50).
	Algos      []string
	KnownHosts string
}

// Fetch — профиль через своё короткое SSH-соединение (туннеля ещё нет:
// сразу после входа по приглашению или вне подключения).
func Fetch(ctx context.Context, t Target) (Profile, error) {
	addr := joinHostPort(t.Host, t.Port)
	algos := t.Algos
	if algos == nil && t.KnownHosts != "" {
		algos = hostkey.Algorithms(t.KnownHosts, addr)
	}
	cl, err := connect(ctx, t.Dial, addr, t.User, t.Signer, t.HostKey, algos)
	if err != nil {
		return Profile{}, err
	}
	defer cl.Close()
	p, err := FetchVia(ctx, cl.Dial)
	if err == nil {
		p.From = addr
	}
	return p, err
}
