package machines

// Включить и выключить машину — дополнительная настройка машины.
//
// Включить (Wake-on-LAN): «волшебный пакет» (6×FF и 16 раз MAC)
// широковещанием в локальную сеть — отсюда и руками всех своих устройств в
// сети устройств, у которых добавлен этот плагин: каждое шлёт пакет в свою
// локалку, так с работы будится ПК дома. Выключить: просьба самой машине
// через сеть устройств (на ней Tarnegis с этим плагином и разрешено
// выключать).

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"sshtunnel/internal/devnet"
	"sshtunnel/internal/lanscan"
	"sshtunnel/internal/mesh"
)

// WolPort — порт плагина в сети устройств: «разбуди MAC», «выключись».
const WolPort = 47842

// normMAC — MAC в виде aa:bb:cc:dd:ee:ff из любого привычного написания.
func normMAC(s string) (string, net.HardwareAddr, error) {
	s = strings.TrimSpace(s)
	clean := strings.NewReplacer("-", "", ":", "", ".", "", " ", "").Replace(s)
	if len(clean) == 12 {
		var b strings.Builder
		for i := 0; i < 12; i += 2 {
			if i > 0 {
				b.WriteByte(':')
			}
			b.WriteString(clean[i : i+2])
		}
		s = b.String()
	}
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 {
		return "", nil, errors.New("MAC не похож на адрес вида AA:BB:CC:DD:EE:FF")
	}
	return strings.ToLower(hw.String()), hw, nil
}

// Target — что будить: MAC и, по желанию, свой широковещательный адрес и
// порт (пусто/0 — общий адрес и адреса своих сетей, порты 9 и 7).
type Target struct {
	MAC   string `json:"mac"`
	Bcast string `json:"bcast,omitempty"`
	Port  int    `json:"port,omitempty"`
}

// wake — wakeTo без своего адреса и порта.
func wake(hw net.HardwareAddr) error { return wakeTo(hw, "", 0) }

// wakeTo шлёт волшебный пакет на общий широковещательный адрес и на адрес
// каждой своей сети (некоторые роутеры пропускают только второй), плюс на
// заданный адрес; порт 0 — 9 и 7.
func wakeTo(hw net.HardwareAddr, bcast string, port int) error {
	pkt := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		pkt = append(pkt, 0xff)
	}
	for i := 0; i < 16; i++ {
		pkt = append(pkt, hw...)
	}
	dsts := []net.IP{net.IPv4bcast}
	if ip := net.ParseIP(strings.TrimSpace(bcast)).To4(); ip != nil {
		dsts = append([]net.IP{ip}, dsts...)
	}
	ports := []int{9, 7}
	if port > 0 && port < 65536 {
		ports = []int{port}
	}
	lans, _ := lanscan.FindLANs()
	for _, l := range lans {
		p := l.Prefix.Masked()
		if !p.Addr().Is4() {
			continue
		}
		a := p.Addr().As4()
		host := uint32(1)<<(32-p.Bits()) - 1
		v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3]) | host
		dsts = append(dsts, net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)))
	}
	sent := 0
	var lastErr error
	for _, ip := range dsts {
		for _, port := range ports {
			c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: ip, Port: port})
			if err != nil {
				lastErr = err
				continue
			}
			if _, err := c.Write(pkt); err == nil {
				sent++
			} else {
				lastErr = err
			}
			c.Close()
		}
	}
	if sent == 0 {
		return lastErr
	}
	return nil
}

// serveWol — просьбы своих устройств: разбудить MAC в своей локалке или
// выключиться самому.
func serveWol(c net.Conn, _ mesh.Caller) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	var req struct {
		Op string `json:"op"`
		Target
	}
	if json.NewDecoder(io.LimitReader(c, 512)).Decode(&req) != nil {
		return
	}
	var err error
	if req.Op == "shutdown" {
		err = shutdownSelf()
	} else {
		var hw net.HardwareAddr
		if _, hw, err = normMAC(req.MAC); err == nil {
			err = wakeTo(hw, req.Bcast, req.Port)
		}
	}
	ans := "ok"
	if err != nil {
		ans = err.Error()
	}
	_, _ = c.Write([]byte(ans))
}

// shutdownSelf — выключить это устройство (если разрешено) через 30 с.
func shutdownSelf() error {
	if !loadSettings().AllowShutdown {
		return errors.New("на этом устройстве выключение по сети не разрешено — галка в плагине «Машины»")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("shutdown", "/s", "/t", "30", "/c", "Tarnegis: выключение по просьбе с другого устройства")
	case "linux":
		cmd = exec.Command("shutdown", "-h", "+1")
	default:
		return errors.New("это устройство так выключить нельзя")
	}
	return cmd.Start()
}

// askPeer — просьба устройству сети (op: wake с mac или shutdown).
func askPeer(peer, op, mac string) error { return askPeerT(peer, op, Target{MAC: mac}) }

// askPeerT — то же с адресом и портом пробуждения (старые устройства их
// не знают и будят как раньше).
func askPeerT(peer, op string, t Target) error {
	c, err := devnet.DialPeer(net.JoinHostPort(peer, strconv.Itoa(WolPort)))
	if err != nil {
		return errors.New("не отвечает в сети устройств — выключено или на нём нет плагина «Машины» или «Удалённый стол»")
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	b, _ := json.Marshal(struct {
		Op string `json:"op"`
		Target
	}{op, t})
	_, _ = c.Write(b)
	r, _ := io.ReadAll(io.LimitReader(c, 300))
	if string(r) != "ok" {
		if len(r) == 0 {
			return errors.New("не ответило")
		}
		return errors.New(string(r))
	}
	return nil
}

// wakeAll — отсюда и всеми своими устройствами в сети; кто отправил.
func wakeAll(mac string) (sent, failed []string) { return WakeAll(Target{MAC: mac}) }

// WakeAll — разбудить отсюда и руками всех своих устройств в сети (у кого
// включён плагин «Машины» или «Удалённый стол»); кто отправил.
func WakeAll(t Target) (sent, failed []string) {
	mac, hw, err := normMAC(t.MAC)
	if err != nil {
		return nil, []string{err.Error()}
	}
	t.MAC = mac
	if err := wakeTo(hw, t.Bcast, t.Port); err == nil {
		sent = append(sent, SelfName)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range devnet.Status().Peers {
		if p.Self || !p.Online || !p.IP.IsValid() {
			continue
		}
		name := p.Name
		if name == "" {
			name = p.Host
		}
		wg.Add(1)
		go func(ip, name string) {
			defer wg.Done()
			err := askPeerT(ip, "wake", t)
			mu.Lock()
			if err == nil {
				sent = append(sent, name)
			} else if !strings.HasPrefix(err.Error(), "не отвечает") {
				failed = append(failed, name+": "+err.Error())
			}
			mu.Unlock()
		}(p.IP.String(), name)
	}
	wg.Wait()
	return sent, failed
}

// SelfName — так в списке «кто отправил» записано само это устройство.
const SelfName = "это устройство"

// NormMAC — MAC в виде aa:bb:cc:dd:ee:ff или ошибка.
func NormMAC(s string) (string, error) { m, _, err := normMAC(s); return m, err }

// Порт пробуждения слушают и «Машины», и «Удалённый стол»: кто первый
// включился — открывает, последний выключившийся — закрывает.
var (
	wolMu   sync.Mutex
	wolRefs int
)

// ServeWol — плагин включился: слушать просьбы разбудить.
func ServeWol() {
	wolMu.Lock()
	defer wolMu.Unlock()
	if wolRefs++; wolRefs == 1 {
		mesh.Serve(WolPort, serveWol)
	}
}

// StopWol — плагин выключился.
func StopWol() {
	wolMu.Lock()
	defer wolMu.Unlock()
	if wolRefs > 0 {
		if wolRefs--; wolRefs == 0 {
			mesh.Serve(WolPort, nil)
		}
	}
}
