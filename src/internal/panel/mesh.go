package panel

// Сеть устройств на сервере: панель ставит и настраивает meshd и показывает
// подключённые устройства. Серверы между собой равны и связываются в сеть
// серверов одним кодом (ТЗ 22, srvnet*.go).
//
// На сервере сеть устройств либо включена (свой meshd; в настройках
// Role == MeshRoleMain — значение осталось от прошлых версий), либо нет.
//
// Старый способ (до ТЗ 22): сервер без своего meshd, чей 127.0.0.1:47831 —
// SSH-проброс к meshd другого сервера (служба meshsvc.UplinkUnitName,
// Role == MeshRoleSecondary). Панель его больше не предлагает; код остался,
// чтобы ещё не обновлённый сервер работал, а обновлённый перешёл в сеть
// серверов кнопкой «Перейти» (ServerNet.Migrate).

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/meshsvc"
)

// MeshSystem — всё, что сеть устройств делает с системой. Настоящая
// реализация — systemMesh (mesh_system.go), в тестах — подделка.
type MeshSystem interface {
	// UnitState — состояние службы systemd.
	UnitState(unit string) UnitState
	// RunScript выполняет скрипт bash от root, построчно отдавая вывод.
	RunScript(script string, onLine func(string)) error
	// InstallUnit пишет юнит, включает его и перезапускает.
	InstallUnit(name, text string) error
	// DisableUnit останавливает и выключает службу (нет её — не ошибка).
	DisableUnit(unit string) error
	// UnitLog — последние строки журнала службы.
	UnitLog(unit string, n int) []string

	// EnsureLinkUser заводит пользователя meshsvc.LinkUser, если его нет.
	EnsureLinkUser() error
	// WriteLinkKeys заменяет его authorized_keys (пользователя нет — не ошибка
	// для пустого списка).
	WriteLinkKeys(lines []string) error
	// KillLinkSessions рвёт соединения побочных серверов: после удаления
	// ключа уже открытые соединения сами не закроются.
	KillLinkSessions() error
	// EnsureLinkSSHD дописывает в sshd_config ограничения для meshlink.
	EnsureLinkSSHD() error

	// HostKeys — открытые SSH-ключи этого сервера ("тип ключ").
	HostKeys() []string
	// LocalAddrs — адреса этого сервера.
	LocalAddrs() []string
}

// UnitState — состояние службы systemd.
type UnitState struct {
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Enabled   bool   `json:"enabled"`
	Detail    string `json:"detail,omitempty"`
}

// meshAdmin — вход meshd для панели (meshsvc.Admin; в тестах — подделка).
type meshAdmin interface {
	State(ctx context.Context) (meshsvc.State, error)
	Rename(ctx context.Context, netID, device, alias string) error
	Forget(ctx context.Context, netID, device string) error
	SetSelf(ctx context.Context, name string, enabled bool, ports string) error
	ForgetServer(ctx context.Context, id string) error
	SetKnown(ctx context.Context, list []meshsvc.KnownServer) error
	SetKnownShared(ctx context.Context, list []meshsvc.KnownServer, shared []string) error
	SetSelfIP(ctx context.Context, ip string) error
	SetPolicy(ctx context.Context, p meshsvc.Policy) error
	RenameNetwork(ctx context.Context, netID, name string) error
	DeleteNetwork(ctx context.Context, netID string) error
	Events(ctx context.Context, after int64, limit int) (meshsvc.Events, error)
	Check(ctx context.Context, netID, from, to string, port int) (meshsvc.CheckResult, error)
	SetApproval(ctx context.Context, netID string, on bool) error
	Approve(ctx context.Context, netID, device string) error
	Freeze(ctx context.Context, netID, device string, on bool) error
	SetExit(ctx context.Context, netID, device, entry string, via []string) (meshsvc.ExitOrder, error)
}

const meshdUnit = "meshd.service"

// MeshManager — сеть устройств в панели.
type MeshManager struct {
	settings *SettingsStore
	sys      MeshSystem
	admin    meshAdmin
	dataDir  string
	version  string

	// sshHost/sshPort — адрес этого сервера, как его вводят устройства.
	sshHost string
	sshPort int

	// lookup — поиск адресов по имени (подменяется в тестах).
	lookup func(ctx context.Context, host string) ([]string, error)
	// linkAddr — куда побочный сервер подключается к главному; пустой —
	// meshsvc.Port на 127.0.0.1, то есть проброс (подменяется в тестах).
	linkAddr string

	// net — сеть серверов (ТЗ 22); logFn — журнал панели.
	net   *ServerNet
	logFn func(format string, args ...any)

	mu         sync.Mutex
	job        *meshJob
	link       *meshsvc.ServerLink
	linkCancel context.CancelFunc
	resolved   map[string]resolvedHost
}

type resolvedHost struct {
	addrs []string
	until time.Time
}

// NewMeshManager собирает сеть устройств панели.
func NewMeshManager(settings *SettingsStore, sys MeshSystem, admin meshAdmin, dataDir, version string) *MeshManager {
	m := &MeshManager{
		settings: settings, sys: sys, admin: admin, dataDir: dataDir, version: version,
		lookup:   net.DefaultResolver.LookupHost,
		resolved: map[string]resolvedHost{},
		logFn:    func(string, ...any) {},
	}
	m.net = newServerNet(m)
	return m
}

// Net — сеть серверов этой панели.
func (m *MeshManager) Net() *ServerNet { return m.net }

// WithLog подключает журнал панели.
func (m *MeshManager) WithLog(f func(format string, args ...any)) *MeshManager {
	m.logFn = f
	return m
}

func (m *MeshManager) logf(format string, args ...any) {
	if m.logFn != nil {
		m.logFn(format, args...)
	}
}

// DirectHosts — куда этому серверу всегда ходить напрямую, мимо выхода
// через другой сервер: серверы сети (и сервер, к которому проброс старого
// способа).
func (m *MeshManager) DirectHosts() []string {
	out := m.net.DirectHosts()
	if s := m.settings.Get().Mesh; s.MainHost != "" && s.Role == MeshRoleSecondary {
		out = append(out, s.MainHost)
	}
	return out
}

// WithSSH задаёт адрес этого сервера для устройств.
func (m *MeshManager) WithSSH(host string, port int) *MeshManager {
	m.sshHost, m.sshPort = host, port
	return m
}

// Start — при запуске панели: сервер старого способа сразу держит связь с
// тем, к которому у него проброс.
func (m *MeshManager) Start() {
	if s := m.settings.Get().Mesh; s.Role == MeshRoleSecondary {
		m.startLink(s)
	}
}

// Stop — при остановке панели.
func (m *MeshManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLinkLocked()
}

// ---------- задания (установка и обновление) ----------

// meshJob — долгое действие (установка meshd), за которым следит интерфейс.
type meshJob struct {
	Kind     string    `json:"kind"`
	Running  bool      `json:"running"`
	Lines    []string  `json:"lines"`
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitempty"`
}

const maxJobLines = 300

var errJobRunning = errors.New("уже идёт установка — дождись её окончания")

func (m *MeshManager) runJob(kind string, fn func(onLine func(string)) error) error {
	m.mu.Lock()
	if m.job != nil && m.job.Running {
		m.mu.Unlock()
		return errJobRunning
	}
	job := &meshJob{Kind: kind, Running: true, Started: time.Now()}
	m.job = job
	m.mu.Unlock()

	onLine := func(line string) {
		m.mu.Lock()
		job.Lines = append(job.Lines, line)
		if len(job.Lines) > maxJobLines {
			job.Lines = job.Lines[len(job.Lines)-maxJobLines:]
		}
		m.mu.Unlock()
	}
	go func() {
		err := fn(onLine)
		m.mu.Lock()
		job.Running = false
		job.Finished = time.Now()
		if err != nil {
			job.Error = err.Error()
		}
		m.mu.Unlock()
	}()
	return nil
}

func (m *MeshManager) jobSnapshot() *meshJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return nil
	}
	j := *m.job
	j.Lines = append([]string(nil), m.job.Lines...)
	return &j
}

// ---------- сеть устройств на этом сервере ----------

// SetMain включает сеть устройств на этом сервере: ставит (или обновляет)
// свой meshd. Имя функции и значение настройки остались от прошлых версий.
func (m *MeshManager) SetMain(name string) error {
	name = cleanName(name)
	if err := m.settings.Update(func(s *Settings) error {
		s.Mesh.Role = MeshRoleMain
		if name != "" {
			s.Mesh.Name = name
		}
		return nil
	}); err != nil {
		return err
	}
	// Был побочным (проброс к другому серверу, ТЗ 12: переход на свой
	// meshd): адрес этого сервера в сети, который знают устройства, —
	// переносим в свой meshd.
	m.mu.Lock()
	oldIP := ""
	if m.link != nil {
		oldIP = m.link.State().MeshIP
	}
	m.stopLinkLocked()
	m.mu.Unlock()
	return m.runJob("install", func(onLine func(string)) error {
		if err := m.installMain(onLine); err != nil {
			return err
		}
		if oldIP != "" {
			m.keepSelfIP(oldIP, onLine)
		}
		return nil
	})
}

// keepSelfIP отдаёт новому meshd прежний адрес этого сервера в сети (meshd
// только что запущен — ждём его до 10 секунд).
func (m *MeshManager) keepSelfIP(ip string, onLine func(string)) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := m.admin.SetSelfIP(ctx, ip)
		cancel()
		if err == nil {
			onLine("Адрес сервера в сети устройств остался прежним: " + ip)
			return
		}
		if time.Now().After(deadline) {
			onLine("Прежний адрес сервера в сети (" + ip + ") не перенёсся: " + err.Error())
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// Reinstall — обновить meshd до версии из релиза.
func (m *MeshManager) Reinstall() error {
	if m.settings.Get().Mesh.Role != MeshRoleMain && !m.sys.UnitState(meshdUnit).Installed {
		return errors.New("сначала включи сеть устройств на этом сервере")
	}
	return m.runJob("install", m.installMain)
}

// InstallUDPRelay ставит или обновляет ретранслятор UDP. Без него у
// клиентов с галочкой «Пробрасывать UDP» звонки, игры и QUIC не работают, а
// видео YouTube подолгу стоит, пока браузер не перейдёт на TCP.
func (m *MeshManager) InstallUDPRelay() error {
	return m.runJob("udprelay", func(onLine func(string)) error {
		onLine("Ставлю ретранслятор UDP")
		if err := m.sys.RunScript(meshsvc.UDPRelayInstallScript(m.version), onLine); err != nil {
			return err
		}
		onLine("Готово: UDP от клиентов идёт через сервер")
		return nil
	})
}

func (m *MeshManager) installMain(onLine func(string)) error {
	// Проброс к другому серверу занимает тот же порт, что и свой meshd.
	if m.sys.UnitState(meshsvc.UplinkUnitName).Installed {
		onLine("Снимаю проброс к другому серверу (" + meshsvc.UplinkUnitName + ")")
		if err := m.sys.DisableUnit(meshsvc.UplinkUnitName); err != nil {
			return err
		}
	}
	onLine("Ставлю meshd — службу сети устройств")
	if err := m.sys.RunScript(meshsvc.InstallScript(m.version), onLine); err != nil {
		return err
	}
	onLine("Готовлю вход для других серверов сети (пользователь " + meshsvc.LinkUser + ")")
	if err := m.syncLinks(); err != nil {
		return fmt.Errorf("вход для других серверов сети: %w", err)
	}
	onLine("Готово: сеть устройств работает")
	return nil
}

// syncLinks приводит пользователя meshlink и его ключи к серверам сети,
// действующим кодам и серверам старого способа.
func (m *MeshManager) syncLinks() error {
	return m.writeLinks(m.net.linkLines(m.settings.Get().Mesh.Links))
}

func (m *MeshManager) writeLinks(lines []string) error {
	if err := m.sys.EnsureLinkUser(); err != nil {
		return err
	}
	if err := m.sys.EnsureLinkSSHD(); err != nil {
		return err
	}
	if err := m.sys.WriteLinkKeys(lines); err != nil {
		return err
	}
	m.net.mu.Lock()
	m.net.lastKeys = "\x00" + strings.Join(lines, "\n")
	m.net.mu.Unlock()
	return nil
}

// meshdVersion — версия meshd, которую ставит эта панель.
const meshdVersion = "7"

// ensureOwnMeshd — перед входом в сеть серверов: своя служба сети есть и
// свежая. Сервер старого способа переходит на свою (адрес в сети тот же).
func (m *MeshManager) ensureOwnMeshd(onLine func(string)) error {
	if m.settings.Get().Mesh.Role == MeshRoleSecondary {
		m.mu.Lock()
		oldIP := ""
		if m.link != nil {
			oldIP = m.link.State().MeshIP
		}
		m.mu.Unlock()
		return m.becomeOwn(onLine, oldIP)
	}
	if err := m.settings.Update(func(s *Settings) error { s.Mesh.Role = MeshRoleMain; return nil }); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	st, err := m.admin.State(ctx)
	cancel()
	have, _ := strconv.Atoi(st.Version)
	want, _ := strconv.Atoi(meshdVersion)
	if err == nil && m.sys.UnitState(meshdUnit).Active && have >= want {
		onLine("meshd уже свежий: версия " + st.Version)
		return nil
	}
	return m.installMain(onLine)
}

// becomeOwn — сервер старого способа получает свою службу сети: проброс
// снимается, адрес сервера в сети, который знают устройства, сохраняется.
func (m *MeshManager) becomeOwn(onLine func(string), oldIP string) error {
	m.mu.Lock()
	m.stopLinkLocked()
	m.mu.Unlock()
	if err := m.settings.Update(func(s *Settings) error { s.Mesh.Role = MeshRoleMain; return nil }); err != nil {
		return err
	}
	if err := m.installMain(onLine); err != nil {
		return err
	}
	if oldIP != "" {
		m.keepSelfIP(oldIP, onLine)
	}
	return nil
}

// Disable выключает сеть устройств на этом сервере. Данные meshd (адреса
// устройств) и сеть серверов остаются — включишь снова, всё вернётся как
// было.
func (m *MeshManager) Disable() error {
	m.mu.Lock()
	if m.job != nil && m.job.Running {
		m.mu.Unlock()
		return errJobRunning
	}
	m.stopLinkLocked()
	m.mu.Unlock()

	var errs []error
	errs = append(errs, m.sys.DisableUnit(meshdUnit), m.sys.DisableUnit(meshsvc.UplinkUnitName))
	// Ключи серверов старого способа убираем: пускать их больше некуда.
	// Серверы сети остаются: правила и учётные записи общие и без meshd.
	errs = append(errs, m.sys.WriteLinkKeys(m.net.linkLines(nil)), m.sys.KillLinkSessions())
	if err := m.settings.Update(func(s *Settings) error {
		s.Mesh.Role = MeshRoleOff
		return nil
	}); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ---------- старый способ: серверы с пробросом к этому ----------

// meshInvite — код старого способа (проброс к этому серверу).
type meshInvite struct {
	V        int      `json:"v"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Key      string   `json:"key"`
	HostKeys []string `json:"hostKeys,omitempty"`
}

const invitePrefix = "sshtunnel-link1:"

func encodeInvite(inv meshInvite) string {
	data, _ := json.Marshal(inv)
	return invitePrefix + base64.RawURLEncoding.EncodeToString(data)
}

func decodeInvite(code string) (meshInvite, error) {
	var inv meshInvite
	code = strings.Join(strings.Fields(code), "") // переносы строк при копировании
	if !strings.HasPrefix(code, invitePrefix) {
		return inv, errors.New("это не код подключения сервера — он начинается с " + invitePrefix)
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, invitePrefix))
	if err != nil {
		return inv, errors.New("код подключения повреждён — скопируй его целиком")
	}
	if err := json.Unmarshal(data, &inv); err != nil {
		return inv, errors.New("код подключения повреждён — скопируй его целиком")
	}
	if inv.ID == "" || inv.Key == "" || validHost(inv.Host) != nil || inv.Port <= 0 || inv.Port > 65535 {
		return inv, errors.New("в коде подключения не хватает данных")
	}
	if _, err := ssh.ParsePrivateKey([]byte(inv.Key)); err != nil {
		return inv, errors.New("в коде подключения испорчен ключ")
	}
	return inv, nil
}

// validHost — адрес сервера пойдёт в юнит systemd и known_hosts: только
// имя или адрес, без пробелов и спецсимволов.
func validHost(h string) error {
	if h == "" || len(h) > 253 {
		return errors.New("нужен адрес сервера")
	}
	for _, r := range h {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '-' || r == ':' || r == '_'
		if !ok {
			return fmt.Errorf("в адресе сервера недопустимый символ %q", r)
		}
	}
	if strings.HasPrefix(h, "-") {
		return errors.New("адрес сервера не может начинаться с «-»")
	}
	return nil
}

// cleanName — имя сервера для людей.
func cleanName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 48 {
		s = string(r[:48])
	}
	return s
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateLink заводит побочный сервер и возвращает код подключения для него.
// host/port — адрес ЭТОГО (главного) сервера, куда будет подключаться
// побочный; пустой — тот, что задан панели при запуске.
func (m *MeshManager) CreateLink(name, host string, port int) (MeshLink, string, error) {
	if m.settings.Get().Mesh.Role != MeshRoleMain {
		return MeshLink{}, "", errors.New("сначала включи сеть устройств на этом сервере")
	}
	name = cleanName(name)
	if name == "" {
		return MeshLink{}, "", errors.New("дай серверу имя — например, по стране: «Германия»")
	}
	if host == "" {
		host = m.sshHost
	}
	if port == 0 {
		port = m.sshPort
	}
	if port == 0 {
		port = 22
	}
	if err := validHost(host); err != nil {
		return MeshLink{}, "", errors.New("укажи адрес этого сервера — по нему к нему подключится другой сервер")
	}
	if port < 1 || port > 65535 {
		return MeshLink{}, "", errors.New("неверный порт SSH")
	}
	pub, priv, err := generateKeyPair()
	if err != nil {
		return MeshLink{}, "", err
	}
	link := MeshLink{ID: randomHex(8), Name: name, PublicKey: pub, Added: time.Now().UTC()}
	if err := m.settings.Update(func(s *Settings) error {
		for _, l := range s.Mesh.Links {
			if strings.EqualFold(l.Name, name) {
				return fmt.Errorf("сервер с именем «%s» уже есть", name)
			}
		}
		if len(s.Mesh.Links) >= 32 {
			return errors.New("слишком много серверов")
		}
		s.Mesh.Links = append(s.Mesh.Links, link)
		return nil
	}); err != nil {
		return MeshLink{}, "", err
	}
	if err := m.syncLinks(); err != nil {
		return link, "", fmt.Errorf("сервер добавлен, но ключ не записан: %w", err)
	}
	code := encodeInvite(meshInvite{
		V: 1, ID: link.ID, Name: name, Host: host, Port: port, Key: priv, HostKeys: m.sys.HostKeys(),
	})
	return link, code, nil
}

// RenameLink — новое имя сервера старого способа (только в этой панели).
func (m *MeshManager) RenameLink(id, name string) error {
	name = cleanName(name)
	if name == "" {
		return errors.New("дай серверу имя")
	}
	found := false
	err := m.settings.Update(func(s *Settings) error {
		for i := range s.Mesh.Links {
			if s.Mesh.Links[i].ID == id {
				s.Mesh.Links[i].Name, found = name, true
			}
		}
		return nil
	})
	if err == nil && !found {
		err = errors.New("нет такого сервера")
	}
	return err
}

// DeleteLink отключает побочный сервер: ключ убирается, соединение рвётся.
func (m *MeshManager) DeleteLink(id string) error {
	found := false
	if err := m.settings.Update(func(s *Settings) error {
		out := s.Mesh.Links[:0]
		for _, l := range s.Mesh.Links {
			if l.ID == id {
				found = true
				continue
			}
			out = append(out, l)
		}
		s.Mesh.Links = out
		return nil
	}); err != nil {
		return err
	}
	if !found {
		return errors.New("нет такого сервера")
	}
	if err := m.syncLinks(); err != nil {
		return err
	}
	// Рвутся все побочные — остальные переподключатся сами за секунды.
	// Адрес отключённого сервера в сети больше не нужен.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m.admin.ForgetServer(ctx, id)
	return m.sys.KillLinkSessions()
}

// SetServerAccess — пускать ли устройства сети к самому этому серверу и на
// какие порты. На главном применяется в meshd сразу, на побочном — к
// следующему же соединению.
func (m *MeshManager) SetServerAccess(ctx context.Context, enabled bool, ports string) error {
	ports = strings.Join(strings.Fields(ports), " ")
	if err := meshsvc.ValidPorts(ports); err != nil {
		return err
	}
	var s MeshSettings
	if err := m.settings.Update(func(st *Settings) error {
		st.Mesh.ServerAccessOff = !enabled
		st.Mesh.ServerPorts = ports
		s = st.Mesh
		return nil
	}); err != nil {
		return err
	}
	if s.Role == MeshRoleMain || m.sys.UnitState(meshdUnit).Active {
		return m.admin.SetSelf(ctx, m.selfName(s), enabled, ports)
	}
	return nil
}

// renameSelf — новое имя этого сервера: в настройки и в meshd (имя в сети
// устройств «имя.mesh» meshd выводит из него сам, как при включении сети).
func (m *MeshManager) renameSelf(ctx context.Context, name string) error {
	var s MeshSettings
	if err := m.settings.Update(func(st *Settings) error {
		st.Mesh.Name = name
		s = st.Mesh
		return nil
	}); err != nil {
		return err
	}
	if s.Role == MeshRoleMain || m.sys.UnitState(meshdUnit).Active {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := m.admin.SetSelf(cctx, m.selfName(s), !s.ServerAccessOff, s.ServerPorts); err != nil && !errors.Is(err, meshsvc.ErrNotRunning) {
			return err
		}
	}
	return nil
}

// selfName — имя этого сервера в сети устройств.
func (m *MeshManager) selfName(s MeshSettings) string {
	if s.Name != "" {
		return s.Name
	}
	if m.sshHost != "" {
		return m.sshHost
	}
	return "Сервер"
}

// allowLocal — пускать ли устройство сети на порт побочного сервера.
func (m *MeshManager) allowLocal(port int) (bool, string) {
	s := m.settings.Get().Mesh
	if s.ServerAccessOff {
		return false, "сервер не пускает устройства сети — это включается в его панели"
	}
	if !meshsvc.PortAllowed(s.ServerPorts, port) {
		return false, "сервер не пускает устройства сети на порт " + strconv.Itoa(port) + " — это настраивается в его панели"
	}
	return true, ""
}

// ---------- побочный сервер ----------

func (m *MeshManager) linkDir() string { return filepath.Join(m.dataDir, "meshlink") }

// Join делает сервер побочным по коду подключения с главного. selfHost —
// адрес этого сервера, как его вводят устройства (по нему главный поймёт,
// какие устройства пришли через него).
func (m *MeshManager) Join(code, selfHost string) error {
	inv, err := decodeInvite(code)
	if err != nil {
		return err
	}
	if selfHost == "" {
		selfHost = m.sshHost
	}
	if selfHost != "" {
		if err := validHost(selfHost); err != nil {
			return err
		}
	}
	m.mu.Lock()
	if m.job != nil && m.job.Running {
		m.mu.Unlock()
		return errJobRunning
	}
	m.mu.Unlock()

	dir := m.linkDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte(inv.Key), 0o600); err != nil {
		return err
	}
	var known []string
	for _, k := range inv.HostKeys {
		if len(strings.Fields(k)) >= 2 {
			known = append(known, meshsvc.KnownHostsLine(inv.Host, inv.Port, k))
		}
	}
	knownPath := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(knownPath, []byte(strings.Join(known, "\n")+"\n"), 0o600); err != nil {
		return err
	}

	// На одном сервере — что-то одно: свой meshd занял бы порт проброса.
	if err := m.sys.DisableUnit(meshdUnit); err != nil {
		return err
	}
	unit := meshsvc.UplinkUnit(inv.Host, inv.Port, keyPath, knownPath, len(known) > 0)
	if err := m.sys.InstallUnit(meshsvc.UplinkUnitName, unit); err != nil {
		return err
	}
	var s MeshSettings
	if err := m.settings.Update(func(st *Settings) error {
		st.Mesh.Role = MeshRoleSecondary
		st.Mesh.Name = inv.Name
		st.Mesh.MainHost = inv.Host
		st.Mesh.MainPort = inv.Port
		st.Mesh.LinkID = inv.ID
		st.Mesh.Host = selfHost
		s = st.Mesh
		return nil
	}); err != nil {
		return err
	}
	m.startLink(s)
	return nil
}

func (m *MeshManager) startLink(s MeshSettings) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLinkLocked()
	host := s.Host
	if host == "" {
		host = m.sshHost
	}
	link := &meshsvc.ServerLink{Addr: m.linkAddr, Info: meshsvc.ServerInfo{
		ID: s.LinkID, Name: s.Name, Host: host, App: m.version, Addrs: m.sys.LocalAddrs(),
	}, Allow: m.allowLocal}
	ctx, cancel := context.WithCancel(context.Background())
	m.link, m.linkCancel = link, cancel
	go link.Run(ctx)
}

func (m *MeshManager) stopLinkLocked() {
	if m.linkCancel != nil {
		m.linkCancel()
	}
	m.link, m.linkCancel = nil, nil
}

// ---------- состояние для интерфейса ----------

// MeshDevice — устройство с тем, через какой сервер оно подключено.
type MeshDevice struct {
	meshsvc.Device
	// Server — id сервера из MeshState.Servers, через который пришло
	// устройство ("main" — этот сервер).
	Server string `json:"server"`
	// Сеть серверов (ТЗ 24): Online выше — на связи хоть с одним сервером
	// сети; Here — с этим сервером; Conns — связь с каждым сервером. Src —
	// с какого сервера сведения ("" — свои), Age — сколько им секунд, Stale —
	// есть только в последней картине сервера без связи.
	Here  bool      `json:"here"`
	Conns []DevConn `json:"conns,omitempty"`
	Src   string    `json:"src,omitempty"`
	Age   int64     `json:"age,omitempty"`
	Stale bool      `json:"stale,omitempty"`
	// Missing — серверы сети, с которыми устройство не на связи, и почему
	// (ТЗ 24, п. 13).
	Missing []DevMiss `json:"missing,omitempty"`
}

// MeshNetwork — сеть с устройствами.
type MeshNetwork struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Created    int64  `json:"created"`
	EmptySince int64  `json:"emptySince"`
	// Approval — «Новые устройства — после одобрения» (meshd 5+, ТЗ 16).
	Approval bool         `json:"approval,omitempty"`
	Devices  []MeshDevice `json:"devices"`
}

// MeshServer — сервер на схеме сети.
type MeshServer struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Host   string          `json:"host,omitempty"`
	Main   bool            `json:"main,omitempty"`
	Online bool            `json:"online"`
	Known  bool            `json:"known"` // добавлен в панели (а не подключился сам)
	Added  time.Time       `json:"added,omitempty"`
	Link   *meshsvc.Server `json:"link,omitempty"`
	// MeshIP/MeshHost — адрес и имя сервера в сети устройств.
	MeshIP   string `json:"meshIp,omitempty"`
	MeshHost string `json:"meshHost,omitempty"`
	// Сервер сети серверов (ТЗ 24): RTTMs — задержка связи с ним; Age —
	// сколько секунд его картине, Stale — он не на связи и картина старая,
	// NoView — картины нет (там старая панель).
	Net    bool    `json:"net,omitempty"`
	RTTMs  float64 `json:"rttMs,omitempty"`
	Age    int64   `json:"age,omitempty"`
	Stale  bool    `json:"stale,omitempty"`
	NoView bool    `json:"noView,omitempty"`
}

// MeshState — всё, что показывает вкладка «Сеть устройств».
type MeshState struct {
	Role          string    `json:"role"`
	EffectiveRole string    `json:"effectiveRole"`
	Name          string    `json:"name"`
	Meshd         UnitState `json:"meshd"`
	Uplink        UnitState `json:"uplink"`
	// UDPRelay — ретранслятор UDP (ставится кнопкой, см. InstallUDPRelay).
	UDPRelay UnitState `json:"udprelay"`
	Job      *meshJob  `json:"job,omitempty"`
	SSHHost  string    `json:"sshHost,omitempty"`
	SSHPort  int       `json:"sshPort,omitempty"`

	// Сам сервер в сети устройств: пускать ли к нему и его адрес там.
	ServerAccess bool          `json:"serverAccess"`
	ServerPorts  string        `json:"serverPorts,omitempty"`
	Self         *meshsvc.Self `json:"self,omitempty"`

	// Главный.
	Version   string        `json:"version,omitempty"`
	StartedAt int64         `json:"startedAt,omitempty"`
	Servers   []MeshServer  `json:"servers"`
	Networks  []MeshNetwork `json:"networks"`
	AdminErr  string        `json:"adminError,omitempty"`

	// Несколько серверов без главного (ТЗ 12). Peers — другие серверы
	// сети, о которых рассказали устройства (с правилами доступа: такие же
	// или нет); Known — серверы, которые эта панель советует устройствам.
	// Правила доступа, журнал, имена и удаление сетей и устройств — свои у
	// каждого сервера: эта панель показывает и меняет только свои.
	Peers []meshsvc.SeenServer  `json:"peers"`
	Known []meshsvc.KnownServer `json:"known"`

	// Побочный.
	MainHost string             `json:"mainHost,omitempty"`
	MainPort int                `json:"mainPort,omitempty"`
	SelfHost string             `json:"selfHost,omitempty"`
	LinkInfo *meshsvc.LinkState `json:"link,omitempty"`
	Log      []string           `json:"log,omitempty"`

	// ServerNet — сеть серверов (ТЗ 22): этот сервер и остальные.
	ServerNet SrvNetState `json:"serverNet"`
	// SelfSrv — id этого сервера в сети серверов; ServerLinks — линии
	// между серверами на схеме (ТЗ 24).
	SelfSrv     string         `json:"selfSrv,omitempty"`
	ServerLinks []SrvLinkState `json:"serverLinks"`
	// Relays — пересланные между серверами соединения (meshd 7).
	Relays      []RelayView         `json:"relays"`
	localRelays []meshsvc.RelayLine // из meshd этого сервера
}

// State собирает состояние сети для интерфейса.
func (m *MeshManager) State(ctx context.Context) MeshState {
	s := m.settings.Get().Mesh
	st := MeshState{
		Role:   s.Role,
		Name:   s.Name,
		Meshd:  m.sys.UnitState(meshdUnit),
		Uplink: m.sys.UnitState(meshsvc.UplinkUnitName),
		Job:    m.jobSnapshot(),

		UDPRelay: m.sys.UnitState(meshsvc.UDPRelayUnitName),
		SSHHost:  m.sshHost,
		SSHPort:  m.sshPort,

		Servers:  []MeshServer{},
		Networks: []MeshNetwork{},
		Peers:    []meshsvc.SeenServer{},
		Known:    []meshsvc.KnownServer{},

		ServerLinks: []SrvLinkState{},
		Relays:      []RelayView{},
	}
	st.ServerAccess, st.ServerPorts = !s.ServerAccessOff, s.ServerPorts
	st.EffectiveRole = s.Role
	// meshd поставлен мастером настройки VPS, а в панели роль не выбрана —
	// по сути это уже главный сервер.
	if s.Role == MeshRoleOff && st.Meshd.Active {
		st.EffectiveRole = MeshRoleMain
	}

	switch st.EffectiveRole {
	case MeshRoleMain:
		m.fillMain(ctx, &st, s)
		// Картины других серверов сети — в ту же схему и тот же список.
		m.net.mergeInto(&st)
	case MeshRoleSecondary:
		st.MainHost, st.MainPort, st.SelfHost = s.MainHost, s.MainPort, s.Host
		m.mu.Lock()
		if m.link != nil {
			ls := m.link.State()
			st.LinkInfo = &ls
		}
		m.mu.Unlock()
		st.Log = m.sys.UnitLog(meshsvc.UplinkUnitName, 15)
	}
	// Серверы старого способа (проброс к этому) — в том же списке, «ждут
	// перехода».
	var links []SrvState
	for _, sv := range st.Servers {
		if !sv.Main && !sv.Net {
			links = append(links, SrvState{ID: legacyPrefix + sv.ID, Name: sv.Name, Host: sv.Host,
				Online: sv.Online, Waiting: true})
		}
	}
	st.ServerNet = m.net.State(links)
	return st
}

// legacyPrefix — id сервера старого способа в списке «Серверы сети».
const legacyPrefix = "link:"

func (m *MeshManager) fillMain(ctx context.Context, st *MeshState, s MeshSettings) {
	name := m.selfName(s)
	st.Servers = append(st.Servers, MeshServer{ID: "main", Name: name, Host: m.sshHost, Main: true, Online: st.Meshd.Active, Known: true})

	ms, err := m.admin.State(ctx)
	if err != nil {
		st.AdminErr = err.Error()
	} else if ms.Self.Name != name || ms.Self.Enabled == s.ServerAccessOff || ms.Self.Ports != s.ServerPorts {
		// meshd переустановили или настройки поменяли, пока он не работал —
		// приводим к тому, что выбрано в панели.
		if m.admin.SetSelf(ctx, name, !s.ServerAccessOff, s.ServerPorts) == nil {
			ms.Self = meshsvc.Self{Name: name, Host: ms.Self.Host, IP: ms.Self.IP, Enabled: !s.ServerAccessOff, Ports: s.ServerPorts}
			if ms2, err := m.admin.State(ctx); err == nil {
				ms = ms2
			}
		}
	}
	if err == nil {
		self := ms.Self
		st.Self = &self
		st.Servers[0].MeshIP, st.Servers[0].MeshHost = self.IP, self.Host
	}
	st.Version, st.StartedAt = ms.Version, ms.StartedAt
	st.localRelays = ms.Relays
	if ms.Peers != nil {
		st.Peers = ms.Peers
	}
	if ms.Known != nil {
		st.Known = ms.Known
	}

	connected := map[string]meshsvc.Server{}
	for _, r := range ms.Servers {
		connected[r.ID] = r
	}
	for _, l := range s.Links {
		srv := MeshServer{ID: l.ID, Name: l.Name, Known: true, Added: l.Added}
		if r, ok := connected[l.ID]; ok {
			r := r
			srv.Online, srv.Link, srv.Host = true, &r, r.Host
			srv.MeshIP, srv.MeshHost = r.IP, r.MeshHost
			delete(connected, l.ID)
		}
		st.Servers = append(st.Servers, srv)
	}
	// Подключились сами, без кода из панели (настроены руками).
	for _, r := range ms.Servers {
		if _, ok := connected[r.ID]; ok {
			r := r
			st.Servers = append(st.Servers, MeshServer{ID: r.ID, Name: r.Name, Host: r.Host, Online: true, Link: &r})
		}
	}

	matcher := m.serverMatcher(ctx, st.Servers)
	for _, n := range ms.Networks {
		mn := MeshNetwork{ID: n.ID, Name: n.Name, Created: n.Created, EmptySince: n.EmptySince, Approval: n.Approval,
			Devices: make([]MeshDevice, 0, len(n.Devices))}
		for _, d := range n.Devices {
			mn.Devices = append(mn.Devices, MeshDevice{Device: d, Server: matcher(d.Via)})
		}
		st.Networks = append(st.Networks, mn)
	}
}

// serverMatcher — по адресу сервера из профиля устройства (via) узнаёт, через
// какой сервер оно подключено. Устройство вводит адрес по-своему (имя или
// IP), поэтому сравниваются адреса после разрешения имён. Не узналось —
// считаем, что через главный.
func (m *MeshManager) serverMatcher(ctx context.Context, servers []MeshServer) func(via string) string {
	type cand struct {
		id    string
		addrs map[string]bool
	}
	var cands []cand
	for _, s := range servers {
		set := map[string]bool{}
		add := func(h string) {
			if h == "" {
				return
			}
			set[strings.ToLower(h)] = true
			for _, a := range m.resolve(ctx, h) {
				set[a] = true
			}
		}
		add(s.Host)
		if s.Main {
			for _, a := range m.sys.LocalAddrs() {
				set[a] = true
			}
		}
		if s.Link != nil {
			for _, a := range s.Link.Addrs {
				add(a)
			}
		}
		cands = append(cands, cand{s.ID, set})
	}
	return func(via string) string {
		if via == "" {
			return "main"
		}
		keys := append([]string{strings.ToLower(via)}, m.resolve(ctx, via)...)
		// Сначала побочные: адрес главного устройство вводит чаще, но
		// совпадение с побочным — более точный ответ.
		for i := len(cands) - 1; i >= 0; i-- {
			for _, k := range keys {
				if cands[i].addrs[k] {
					return cands[i].id
				}
			}
		}
		return "main"
	}
}

// resolve — адреса имени с кешем на 10 минут; IP возвращается как есть.
func (m *MeshManager) resolve(ctx context.Context, host string) []string {
	host = strings.ToLower(strings.Trim(host, "[]"))
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}
	}
	m.mu.Lock()
	r, ok := m.resolved[host]
	m.mu.Unlock()
	if ok && time.Now().Before(r.until) {
		return r.addrs
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := m.lookup(lctx, host)
	if err != nil {
		addrs = nil
	}
	m.mu.Lock()
	if len(m.resolved) > 512 {
		clear(m.resolved)
	}
	m.resolved[host] = resolvedHost{addrs: addrs, until: time.Now().Add(10 * time.Minute)}
	m.mu.Unlock()
	return addrs
}

// SetKnown — какие серверы сети советовать устройствам этого сервера.
// Совет — только подсказка: устройство подключится к серверу, лишь если у
// него есть свой вход туда (профиль с тем же ключом сети).
func (m *MeshManager) SetKnown(ctx context.Context, list []meshsvc.KnownServer) error {
	for i := range list {
		list[i].Host = strings.TrimSpace(list[i].Host)
		list[i].Name = cleanName(list[i].Name)
		if err := validHost(list[i].Host); err != nil {
			return err
		}
		if list[i].Port == 0 {
			list[i].Port = 22
		}
		if list[i].Port < 1 || list[i].Port > 65535 {
			return errors.New("неверный порт")
		}
	}
	return m.admin.SetKnown(ctx, list)
}

// remoteOnly — устройства (device == "" — сети) нет в meshd этого сервера,
// но оно есть у другого сервера сети (ТЗ 24): действие идёт общей записью,
// тот сервер применит её сам при сверке. meshd не ответил — false: пусть
// ошибку скажет сам вызов.
func (m *MeshManager) remoteOnly(ctx context.Context, netID, device string) bool {
	ms, err := m.admin.State(ctx)
	if err != nil {
		return false
	}
	hn, hd := hasDevice(ms.Networks, netID, device)
	if device == "" {
		return !hn && m.net.remoteHasNet(netID)
	}
	return !hd && m.net.deviceServer(netID, device) != ""
}

// Rename переименовывает устройство (пустое имя — вернуть его собственное).
func (m *MeshManager) Rename(ctx context.Context, netID, device, alias string) error {
	if m.remoteOnly(ctx, netID, device) {
		m.net.noteAlias(netID, device, alias)
		return nil
	}
	if err := m.admin.Rename(ctx, netID, device, alias); err != nil {
		return err
	}
	m.net.noteAlias(netID, device, alias)
	return nil
}

// Forget убирает устройство из сети.
func (m *MeshManager) Forget(ctx context.Context, netID, device string) error {
	if m.remoteOnly(ctx, netID, device) {
		m.net.noteForget(netID, device)
		return nil
	}
	if err := m.admin.Forget(ctx, netID, device); err != nil {
		return err
	}
	m.net.noteForget(netID, device)
	return nil
}

// RenameNetwork задаёт сети имя (пустое — убрать).
func (m *MeshManager) RenameNetwork(ctx context.Context, netID, name string) error {
	if m.remoteOnly(ctx, netID, "") {
		m.net.noteNetName(netID, strings.Join(strings.Fields(name), " "))
		return nil
	}
	if err := m.admin.RenameNetwork(ctx, netID, name); err != nil {
		return err
	}
	m.net.noteNetName(netID, strings.Join(strings.Fields(name), " "))
	return nil
}

// DeleteNetwork удаляет сеть и отключает её устройства.
func (m *MeshManager) DeleteNetwork(ctx context.Context, netID string) error {
	if m.remoteOnly(ctx, netID, "") {
		m.net.noteNetDelete(netID)
		return nil
	}
	if err := m.admin.DeleteNetwork(ctx, netID); err != nil {
		return err
	}
	m.net.noteNetDelete(netID)
	return nil
}

// Events — журнал событий сети устройств.
func (m *MeshManager) Events(ctx context.Context, after int64, limit int) (meshsvc.Events, error) {
	return m.admin.Events(ctx, after, limit)
}

// SetApproval — «Новые устройства — после одобрения» у сети (ТЗ 16).
func (m *MeshManager) SetApproval(ctx context.Context, netID string, on bool) error {
	if m.remoteOnly(ctx, netID, "") {
		m.net.noteApproval(netID, on)
		return nil
	}
	if err := m.admin.SetApproval(ctx, netID, on); err != nil {
		return err
	}
	m.net.noteApproval(netID, on)
	return nil
}

// Approve пускает в сеть устройство, которое ждёт одобрения.
func (m *MeshManager) Approve(ctx context.Context, netID, device string) error {
	if m.remoteOnly(ctx, netID, device) {
		m.net.noteApprove(netID, device)
		return nil
	}
	if err := m.admin.Approve(ctx, netID, device); err != nil {
		return err
	}
	m.net.noteApprove(netID, device)
	return nil
}

// Freeze замораживает (on) или размораживает устройство.
func (m *MeshManager) Freeze(ctx context.Context, netID, device string, on bool) error {
	if m.remoteOnly(ctx, netID, device) {
		m.net.noteFreeze(netID, device, on)
		return nil
	}
	if err := m.admin.Freeze(ctx, netID, device, on); err != nil {
		return err
	}
	m.net.noteFreeze(netID, device, on)
	return nil
}

// SetExit задаёт устройству цепочку выхода (ключи «адрес:порт»). Проверяет
// meshd; здесь — только длина, чтобы не гонять мусор по сокету.
func (m *MeshManager) SetExit(ctx context.Context, netID, device, entry string, via []string) (meshsvc.ExitOrder, error) {
	if len(entry) > 80 || len(via) > 2 {
		return meshsvc.ExitOrder{}, errors.New("неверная цепочка выхода")
	}
	// Устройство подключено к другому серверу сети — цепочку ему отдаёт тот
	// сервер (ТЗ 24).
	if m.remoteOnly(ctx, netID, device) {
		return m.net.forwardExit(ctx, m.net.deviceServer(netID, device), netID, device, entry, via)
	}
	return m.admin.SetExit(ctx, netID, device, entry, via)
}

// Check — пустят ли устройство from к устройству to на порт.
func (m *MeshManager) Check(ctx context.Context, netID, from, to string, port int) (meshsvc.CheckResult, error) {
	return m.admin.Check(ctx, netID, from, to, port)
}
