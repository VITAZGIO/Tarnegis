package panel

// Устройства выдаются из панели (ТЗ 47). Владелец заводит устройство в
// панели и даёт программе одну строку-приглашение; дальше панель решает, на
// какие серверы сети устройству можно и есть ли у него сеть устройств, а
// программа только следует профилю (/api/me/profile, selfservice.go).
//
// Приглашение — «sshtun1:» + base64url(JSON) с адресом и ключом этого
// сервера и разовым секретом (32 случайных байта, 24 часа, один раз; здесь
// хранится только его sha256). Закрытый ключ устройства панель больше не
// заводит: программа делает пару сама и входит по SSH пользователем
// tun_join (любой ключ, но только команда join — joinsys.go), отдаёт секрет
// и получает id своей учётной записи; её ключом становится тот, которым она
// вошла.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	devInvitePrefix = "sshtun1:"
	devInviteTTL    = 24 * time.Hour
	maxDevInvites   = 20
	inviteTokenLen  = 32
)

// Ответы join (контракт ТЗ 47 ↔ 48, раздел B).
const (
	joinExpired = "expired"
	joinUsed    = "used"
	joinUnknown = "unknown"
	joinFrozen  = "frozen"
	joinLimit   = "limit"
)

// inviteDoc — то, что внутри строки приглашения (контракт, раздел A).
type inviteDoc struct {
	V    int    `json:"v"`
	Host string `json:"host"`
	Port int    `json:"port"`
	HK   string `json:"hk"`
	Tok  string `json:"tok"`
	Exp  int64  `json:"exp"`
	Name string `json:"name"`
}

func encodeDevInvite(d inviteDoc) string {
	data, _ := json.Marshal(d)
	return devInvitePrefix + base64.RawURLEncoding.EncodeToString(data)
}

// parseDevInvite — обратное encodeDevInvite (программа разбирает сама, здесь — для
// тестов и проверки). Лишние поля не мешают.
func parseDevInvite(s string) (inviteDoc, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, devInvitePrefix) {
		return inviteDoc{}, errors.New("это не приглашение ssh_tunnel")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, devInvitePrefix))
	if err != nil {
		return inviteDoc{}, errors.New("приглашение повреждено")
	}
	var d inviteDoc
	if err := json.Unmarshal(data, &d); err != nil || d.V != 1 || d.Host == "" || d.Tok == "" {
		return inviteDoc{}, errors.New("приглашение повреждено")
	}
	if d.Port == 0 {
		d.Port = 22
	}
	return d, nil
}

// devInvite — выданное приглашение (хранится только на этом сервере).
type devInvite struct {
	ID      string     `json:"id"`   // открытый id: список и «Отозвать»
	Hash    string     `json:"hash"` // sha256 секрета, hex
	Name    string     `json:"name"`
	Type    DeviceType `json:"deviceType"`
	Servers []string   `json:"servers,omitempty"`
	NoMesh  bool       `json:"noMesh,omitempty"`
	// User — «Новое приглашение» для уже заведённого устройства: ключ
	// добавится к его учётной записи, старые ключи остаются.
	User    string `json:"user,omitempty"`
	Created int64  `json:"created"`
	Expires int64  `json:"expires"`
	Used    int64  `json:"used,omitempty"`
	UsedBy  string `json:"usedBy,omitempty"`
}

// inviteView — приглашение для страницы (без хеша).
type inviteView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	DeviceType DeviceType `json:"deviceType"`
	Servers    []string   `json:"servers,omitempty"`
	Mesh       bool       `json:"mesh"`
	User       string     `json:"user,omitempty"`
	Created    int64      `json:"created"`
	Expires    int64      `json:"expires"`
}

func (d devInvite) view() inviteView {
	return inviteView{ID: d.ID, Name: d.Name, DeviceType: d.Type, Servers: d.Servers, Mesh: !d.NoMesh,
		User: d.User, Created: d.Created, Expires: d.Expires}
}

// inviteStore — device-invites.json рядом с остальными файлами панели.
type inviteStore struct {
	path string
	now  func() time.Time

	mu   sync.Mutex
	list []devInvite
}

func openInviteStore(path string) *inviteStore {
	st := &inviteStore{path: path, now: time.Now}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			json.Unmarshal(data, &st.list)
		}
	}
	return st
}

func tokenHash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// pruneLocked — истёкшие уходят совсем (и использованные — тоже по
// истечении: до того на повторную попытку ответ «used», а не «unknown»).
func (st *inviteStore) pruneLocked() bool {
	now := st.now().Unix()
	before := len(st.list)
	st.list = slices.DeleteFunc(st.list, func(d devInvite) bool { return d.Expires <= now })
	return len(st.list) != before
}

func (st *inviteStore) saveLocked() {
	if st.path == "" {
		return
	}
	data, err := json.MarshalIndent(st.list, "", " ")
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(st.path), 0o700)
	tmp := st.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, st.path)
	}
}

// add — новое приглашение; возвращает секрет (он есть только в ответе).
func (st *inviteStore) add(d devInvite) (string, devInvite, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked()
	open := 0
	for _, x := range st.list {
		if x.Used == 0 {
			open++
		}
	}
	if open >= maxDevInvites {
		return "", devInvite{}, fmt.Errorf("уже %d неиспользованных приглашений — отзови ненужные", open)
	}
	raw := make([]byte, inviteTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return "", devInvite{}, err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	now := st.now()
	d.ID = randomHex(6)
	d.Hash = tokenHash(tok)
	d.Created = now.Unix()
	d.Expires = now.Add(devInviteTTL).Unix()
	st.list = append(st.list, d)
	st.saveLocked()
	return tok, d, nil
}

// open — неиспользованные и не истёкшие, новые первыми.
func (st *inviteStore) open() []inviteView {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.pruneLocked() {
		st.saveLocked()
	}
	out := []inviteView{}
	for i := len(st.list) - 1; i >= 0; i-- {
		if st.list[i].Used == 0 {
			out = append(out, st.list[i].view())
		}
	}
	return out
}

func (st *inviteStore) revoke(id string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	n := len(st.list)
	st.list = slices.DeleteFunc(st.list, func(d devInvite) bool { return d.ID == id && d.Used == 0 })
	if len(st.list) == n {
		return false
	}
	st.saveLocked()
	return true
}

// take — найти приглашение по секрету и сразу пометить использованным (два
// одновременных входа с одним секретом не пройдут оба). Не вышло завести
// учётную запись — release вернёт его.
func (st *inviteStore) take(tok string) (devInvite, string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now().Unix()
	h := tokenHash(tok)
	for i := range st.list {
		d := &st.list[i]
		if subtle.ConstantTimeCompare([]byte(d.Hash), []byte(h)) != 1 {
			continue
		}
		switch {
		case d.Used != 0:
			return devInvite{}, joinUsed
		case d.Expires <= now:
			return devInvite{}, joinExpired
		}
		d.Used = now
		st.saveLocked()
		return *d, ""
	}
	return devInvite{}, joinUnknown
}

func (st *inviteStore) release(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.list {
		if st.list[i].ID == id {
			st.list[i].Used, st.list[i].UsedBy = 0, ""
		}
	}
	st.saveLocked()
}

func (st *inviteStore) markUsed(id, user string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.list {
		if st.list[i].ID == id {
			st.list[i].UsedBy = user
		}
	}
	st.saveLocked()
}

// joinDeviceType — тип из запроса программы: её словами (phone, pc) или
// нашими; непонятное — "".
func joinDeviceType(s string) DeviceType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "phone", "android":
		return DeviceAndroid
	case "pc", "windows":
		return DeviceWindows
	case "linux":
		return DeviceLinux
	}
	return ""
}

// validServerID — похоже на id сервера сети (randomHex).
func validServerID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
