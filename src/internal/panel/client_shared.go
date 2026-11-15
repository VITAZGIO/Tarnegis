package panel

// Учётные записи устройств в сети серверов (ТЗ 22): одна и та же запись
// (SSH-пользователь tun_*, его открытые ключи, заморозка, группы) есть на
// каждом сервере сети — устройство входит на любой из них тем же ключом.
// Закрытый ключ и пароль создателя по сети не ходят: закрытый остаётся на
// сервере, где запись заведена, пароль создателя — свой у каждого сервера.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// acctRec — учётная запись в общих данных сети.
type acctRec struct {
	Name    string     `json:"name"`
	Type    DeviceType `json:"type,omitempty"`
	Keys    []string   `json:"keys"`
	Off     bool       `json:"off,omitempty"`
	Groups  []string   `json:"groups,omitempty"`
	Origin  string     `json:"origin,omitempty"` // имя сервера, где заведена
	Created time.Time  `json:"created,omitempty"`
}

// recOfClient — запись для сети из клиента этого сервера.
func recOfClient(c Client, selfName string) acctRec {
	origin := c.Origin
	if origin == "" {
		origin = selfName
	}
	return acctRec{Name: c.Name, Type: c.DeviceType, Keys: c.keys(), Off: c.State == StateFrozen,
		Groups: append([]string(nil), c.Groups...), Origin: origin, Created: c.CreatedAt}
}

// cleanKeys — открытые ключи из сети: только разбираемые, без опций и
// комментариев, без повторов. В authorized_keys попадёт только такое.
func cleanKeys(in []string) []string {
	var out []string
	for _, k := range in {
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k))
		if err != nil {
			continue
		}
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
		if !slices.Contains(out, line) {
			out = append(out, line)
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}

// ApplyShared приводит учётную запись username к записи из сети: заводит,
// меняет ключи, замораживает, удаляет. Об этих изменениях сеть уже знает —
// onChange не зовётся.
func (m *ClientManager) ApplyShared(username string, rec *acctRec) error {
	if !ValidUsername(username) {
		return fmt.Errorf("неверное имя пользователя из сети: %q", username)
	}
	m.editMu.Lock()
	defer m.editMu.Unlock()
	c, exists := m.store.Get(ClientID(username))
	if rec == nil {
		if !exists {
			return nil
		}
		return m.deleteClient(c)
	}
	keys := cleanKeys(rec.Keys)
	if len(keys) == 0 {
		return errors.New("у учётной записи из сети нет годного ключа")
	}
	groups, err := cleanGroups(rec.Groups)
	if err != nil {
		groups = nil
	}
	name := strings.TrimSpace(rec.Name)
	if name == "" || len([]rune(name)) > maxNameLength {
		name = username
	}
	if !exists {
		return m.importClient(username, name, rec, keys, groups)
	}

	// Ключи: свой (у которого здесь есть закрытый) остаётся основным.
	next := c
	next.Name, next.Groups = name, groups
	if validDeviceType(rec.Type) {
		next.DeviceType = rec.Type
	}
	if c.PrivateKey == "" || !slices.Contains(keys, c.PublicKey) {
		next.PublicKey, next.ExtraKeys = keys[0], keys[1:]
	} else {
		next.ExtraKeys = slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k == c.PublicKey })
	}
	if len(next.ExtraKeys) == 0 {
		next.ExtraKeys = nil
	}
	keysChanged := !slices.Equal(c.keys(), next.keys())
	switch {
	case rec.Off && c.State != StateFrozen:
		if err := m.freeze(next); err != nil {
			return err
		}
		next.State = StateFrozen
	case !rec.Off && c.State == StateFrozen:
		next.State = StateActive
		content, err := m.provisioner.ReadAuthorizedKeys(username)
		if err != nil {
			return err
		}
		updated, err := withClientKeys(content, next)
		if err != nil {
			return err
		}
		if err := m.provisioner.WriteAuthorizedKeys(username, updated); err != nil {
			return err
		}
	case keysChanged && c.State != StateFrozen:
		content, err := m.provisioner.ReadAuthorizedKeys(username)
		if err != nil {
			return err
		}
		updated, err := withClientKeys(content, next)
		if err != nil {
			return err
		}
		if err := m.provisioner.WriteAuthorizedKeys(username, updated); err != nil {
			return err
		}
	}
	if sharedEqual(c, next) {
		return nil
	}
	return m.store.Put(next)
}

func sharedEqual(a, b Client) bool {
	return a.Name == b.Name && a.DeviceType == b.DeviceType && a.State == b.State &&
		a.PublicKey == b.PublicKey && slices.Equal(a.ExtraKeys, b.ExtraKeys) && slices.Equal(a.Groups, b.Groups)
}

// importClient заводит пользователя, пришедшего из сети: те же имя и ключи,
// своего закрытого ключа нет.
func (m *ClientManager) importClient(username, name string, rec *acctRec, keys, groups []string) error {
	if err := m.provisioner.EnsureGroup(); err != nil {
		return fmt.Errorf("не могу подготовить группу %s: %w", sshdGroup, err)
	}
	c := Client{
		ID: ClientID(username), Name: name, DeviceType: rec.Type, CreatedAt: rec.Created,
		State: StateActive, Username: username, PublicKey: keys[0], ExtraKeys: keys[1:],
		Groups: groups, Origin: rec.Origin,
	}
	if !validDeviceType(c.DeviceType) {
		c.DeviceType = DeviceWindows
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	if len(c.ExtraKeys) == 0 {
		c.ExtraKeys = nil
	}
	first, err := AuthorizedKeysLine(keys[0], c.ID)
	if err != nil {
		return err
	}
	if err := m.provisioner.CreateUser(username, first); err != nil {
		return fmt.Errorf("не могу завести пользователя %s: %w", username, err)
	}
	if rec.Off {
		c.State = StateFrozen
		if err := m.provisioner.WriteAuthorizedKeys(username, ""); err != nil {
			_ = m.provisioner.DeleteUser(username)
			return err
		}
	} else if len(keys) > 1 {
		content, err := withClientKeys("", c)
		if err == nil {
			err = m.provisioner.WriteAuthorizedKeys(username, content)
		}
		if err != nil {
			_ = m.provisioner.DeleteUser(username)
			return err
		}
	}
	uid, err := m.provisioner.UID(username)
	if err != nil {
		_ = m.provisioner.DeleteUser(username)
		return fmt.Errorf("пользователь создан, но не удалось узнать его uid: %w", err)
	}
	c.UID = uid
	if err := m.store.Put(c); err != nil {
		_ = m.provisioner.DeleteUser(username)
		return err
	}
	if m.traffic != nil {
		if err := m.traffic.AddClient(c.ID, uid); err != nil {
			m.warn("не удалось завести правило учёта трафика для %s: %v", c.ID, err)
		}
	}
	return nil
}
