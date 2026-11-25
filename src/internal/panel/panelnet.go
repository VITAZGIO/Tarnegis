package panel

// Сеть панели (ТЗ 49): одна сеть устройств, которую держит сама панель
// (сеть серверов). Сюда попадают устройства по приглашению — их программа
// берёт ключ сети из профиля устройства (/api/me/profile, поле meshKey) и
// своего ключа не заводит. Сервера знают сети только по id (хэш ключа),
// поэтому ключ хранит панель:
//   - на сервере — panel-net.json в папке данных (0600);
//   - в сети серверов — общая запись kMeshKey (подписанный обмен, как у
//     учётных записей), так ключ есть на каждом сервере сети.
// Две панели завели ключ одновременно — остаётся тот, что заведён раньше
// (одинаково на всех серверах), а не новее: ключ однажды выданный устройствам
// менять нельзя (смена ключа — вне ТЗ 49). Ключ не пишется в журнал, не
// уходит в приглашение и не отдаётся устройствам без сети устройств.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// kMeshKey — общая запись «ключ сети панели».
const kMeshKey = "meshkey"

// panelNetRec — ключ сети панели и когда он заведён (мс).
type panelNetRec struct {
	Key     string `json:"key"`
	Created int64  `json:"created"`
}

func (r panelNetRec) valid() bool { return len(r.Key) >= 16 && len(r.Key) <= 128 && r.Created > 0 }

// earlier — r заведён раньше o (при равном времени — меньший ключ): такой
// и остаётся сетью панели везде.
func (r panelNetRec) earlier(o panelNetRec) bool {
	if !o.valid() {
		return r.valid()
	}
	if !r.valid() {
		return false
	}
	if r.Created != o.Created {
		return r.Created < o.Created
	}
	return r.Key < o.Key
}

// meshNetID — id сети устройств по ключу, как считает meshd (netID).
func meshNetID(key string) string {
	h := sha256.Sum256([]byte("ssh_tunnel mesh v1\x00" + key))
	return hex.EncodeToString(h[:])
}

// newPanelMeshKey — ключ в том же виде, что заводит программа устройства
// (config.newMeshKey).
func newPanelMeshKey() string {
	var raw [24]byte
	rand.Read(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func (n *ServerNet) panelNetPath() string {
	if n.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(n.path), "panel-net.json")
}

// loadPanelNetLocked — ключ из файла этого сервера.
func (n *ServerNet) loadPanelNetLocked() panelNetRec {
	if n.pnet.valid() {
		return n.pnet
	}
	if p := n.panelNetPath(); p != "" {
		var r panelNetRec
		if data, err := os.ReadFile(p); err == nil && json.Unmarshal(data, &r) == nil && r.valid() {
			n.pnet = r
		}
	}
	return n.pnet
}

func (n *ServerNet) savePanelNetLocked(r panelNetRec) error {
	n.pnet = r
	p := n.panelNetPath()
	if p == "" {
		return nil
	}
	data, _ := json.Marshal(r)
	os.MkdirAll(filepath.Dir(p), 0o700)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// netPanelRecLocked — ключ из общих данных сети серверов.
func (n *ServerNet) netPanelRecLocked() (panelNetRec, bool) {
	it, ok := n.f.Items[kMeshKey]
	if !ok || it.Del {
		return panelNetRec{}, false
	}
	r, ok := decodeItem[panelNetRec](it)
	return r, ok && r.valid()
}

// PanelNet — ключ сети панели. create — завести, если его ещё нет. Сверяет
// здешний файл с общими данными: остаётся заведённый раньше; здешнего в
// сети нет (или он раньше) — уходит в сеть.
func (n *ServerNet) PanelNet(create bool) (panelNetRec, error) {
	n.mu.Lock()
	local := n.loadPanelNetLocked()
	inNet := n.f.Net != ""
	shared, hasShared := panelNetRec{}, false
	if inNet {
		shared, hasShared = n.netPanelRecLocked()
	}
	best := local
	if hasShared && shared.earlier(best) {
		best = shared
	}
	if !best.valid() {
		if !create {
			n.mu.Unlock()
			return panelNetRec{}, nil
		}
		best = panelNetRec{Key: newPanelMeshKey(), Created: n.now().UnixMilli()}
	}
	var err error
	if best != local {
		err = n.savePanelNetLocked(best)
	}
	push := inNet && (!hasShared || best != shared) && !n.applying
	if push {
		n.stampLocked(kMeshKey, false, best)
		n.saveLocked()
	}
	n.mu.Unlock()
	if push {
		n.poke()
	}
	if err != nil {
		return best, errors.New("ключ сети панели не записан: " + err.Error())
	}
	return best, nil
}

// applyPanelNet — после обмена: ключ из сети берётся себе, здешний, которого
// в сети нет, уходит в сеть.
func (n *ServerNet) applyPanelNet() {
	if _, err := n.PanelNet(false); err != nil && n.mesh != nil {
		n.mesh.logf("сеть серверов: %v", err)
	}
}

// PanelNetID — id сети панели ("" — её ещё нет).
func (n *ServerNet) PanelNetID() string {
	r, _ := n.PanelNet(false)
	if !r.valid() {
		return ""
	}
	return meshNetID(r.Key)
}

// mergePanelNet — правило слияния записи kMeshKey: берётся заведённый
// раньше ключ, а не правка новее (см. начало файла).
func mergePanelNet(old, in srvItem) bool {
	if in.Del {
		return false // удалять сеть панели обменом нельзя
	}
	ri, ok := decodeItem[panelNetRec](in)
	if !ok || !ri.valid() {
		return false
	}
	if old.Del || len(old.D) == 0 {
		return true
	}
	ro, ok := decodeItem[panelNetRec](old)
	return !ok || ri.earlier(ro)
}
