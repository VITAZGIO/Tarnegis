package invite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/hostkey"
)

// Options — где лежат ключ и known_hosts, как назвать устройство.
type Options struct {
	// KeyDir — папка ключа устройства (на ПК — папка настроек).
	KeyDir string
	// KnownHosts — known_hosts программы: сюда записываются ключи серверов.
	KnownHosts string
	// Name — имя устройства (пусто — из приглашения); Type — «pc», «phone»…
	Name, Type string
	Dial       Dialer
	// Now — часы (тесты); FetchTries/FetchPause — сколько раз и с какой
	// паузой читать профиль сразу после входа (учётная запись на сервере
	// может появиться не мгновенно).
	Now        func() time.Time
	FetchTries int
	FetchPause time.Duration
}

// Redeemed — итог входа по приглашению.
type Redeemed struct {
	Device  config.Device
	Profile config.DeviceProfile
	// ProfileErr — профиль прочитать не вышло: в Profile только сервер
	// приглашения, остальное придёт со следующей сверкой.
	ProfileErr error
}

// Redeem — весь путь: разобрать, создать ключ, войти, записать ключ
// сервера, прочитать профиль. Ключ устройства создаётся на устройстве и
// никуда не отправляется (уходит только открытая часть — при входе).
func Redeem(ctx context.Context, text string, o Options) (Redeemed, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	inv, err := Parse(text)
	if err != nil {
		return Redeemed{}, err
	}
	if inv.Expired(now()) {
		return Redeemed{}, ErrExpired
	}
	name := strings.TrimSpace(o.Name)
	if name == "" {
		name = inv.Name
	}
	var rnd [4]byte
	rand.Read(rnd[:])
	tmp := filepath.Join(o.KeyDir, "device_new_"+hex.EncodeToString(rnd[:]))
	signer, err := NewKey(tmp)
	if err != nil {
		return Redeemed{}, errors.New("не создать ключ устройства: " + err.Error())
	}
	drop := func() { os.Remove(tmp); os.Remove(tmp + ".pub") }
	resp, err := Join(ctx, inv, signer, name, o.Type, o.Dial)
	if err != nil {
		drop()
		return Redeemed{}, err
	}
	keyPath := filepath.Join(o.KeyDir, "device_"+resp.ID)
	if err := os.Rename(tmp, keyPath); err != nil {
		keyPath = tmp // ключ рабочий и под временным именем
	} else {
		os.Rename(tmp+".pub", keyPath+".pub")
	}
	// Сервер только что доказал, что ключ из приглашения — его: этот ключ
	// и записываем (прежний под тем же адресом — заменяем).
	pinExact(o.KnownHosts, inv.Host, inv.Port, inv.HK)

	if resp.Name != "" {
		name = resp.Name
	}
	dev := config.Device{ID: resp.ID, Name: name, KeyPath: keyPath, Join: inv.Addr(), Mesh: true}
	hk, _ := inv.HostKey()
	tries := o.FetchTries
	if tries <= 0 {
		tries = 5
	}
	pause := o.FetchPause
	if pause <= 0 {
		pause = time.Second
	}
	var prof Profile
	for i := 0; i < tries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(pause):
			}
			if ctx.Err() != nil {
				break
			}
		}
		prof, err = Fetch(ctx, Target{Host: inv.Host, Port: inv.Port, User: resp.ID, Signer: signer,
			HostKey: FixedHostKey(hk), Dial: o.Dial})
		if err == nil || errors.Is(err, ErrNoProfile) {
			break
		}
	}
	out := Redeemed{Device: dev}
	if err == nil && prof.ID == resp.ID {
		out.Profile = prof.Config()
		PinServers(o.KnownHosts, out.Profile.Servers)
		return out, nil
	}
	if err == nil {
		err = errors.New("сервер отдал профиль другой учётной записи")
	}
	// Без профиля — хотя бы сервер приглашения: на него уже можно войти.
	out.ProfileErr = err
	out.Profile = config.DeviceProfile{ID: resp.ID, Name: name, Mesh: true,
		Servers: []config.DeviceServer{{Name: inv.Host, Host: inv.Host, Port: inv.Port, HostKey: inv.HK}}}
	return out, nil
}

// PinServers записывает ключи серверов из профиля в known_hosts (только
// для адресов, где ключа ещё нет: смену ключа решает hostkey, ТЗ 46).
func PinServers(knownHosts string, list []config.DeviceServer) {
	for _, s := range list {
		hostkey.Pin(knownHosts, s.Host, s.Port, s.HostKey)
	}
}

// pinExact — ключ, проверенный живым соединением: записать, заменив
// прежний под этим адресом.
func pinExact(path, host string, port int, key string) {
	if path == "" {
		return
	}
	for _, k := range hostkey.Known(path, host, port) {
		if pk, err := parseKeyLine(key); err == nil && k.Fingerprint == fingerprint(pk) {
			return
		}
	}
	if len(hostkey.Known(path, host, port)) > 0 {
		_ = hostkey.ReplaceLine(path, []string{hostkey.HostName(host, port)}, key)
		return
	}
	hostkey.Pin(path, host, port, key)
}
