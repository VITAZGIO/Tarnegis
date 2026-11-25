package meshsvc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Device — устройство сети глазами панели (см. adminDevice в cmd/meshd).
type Device struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Alias       string  `json:"alias,omitempty"`
	Display     string  `json:"display"`
	Host        string  `json:"host"`
	IP          string  `json:"ip"`
	User        string  `json:"user,omitempty"`
	Online      bool    `json:"online"`
	Platform    string  `json:"platform,omitempty"`
	OS          string  `json:"os,omitempty"`
	App         string  `json:"app,omitempty"`
	Mode        string  `json:"mode,omitempty"`
	Via         string  `json:"via,omitempty"`
	Hostname    string  `json:"hostname,omitempty"`
	FirstSeen   int64   `json:"firstSeen,omitempty"`
	LastSeen    int64   `json:"lastSeen,omitempty"`
	ConnectedAt int64   `json:"connectedAt,omitempty"`
	Sessions    int64   `json:"sessions"`
	RTTMs       float64 `json:"rttMs,omitempty"`
	RTTAvgMs    float64 `json:"rttAvgMs,omitempty"`
	JitterMs    float64 `json:"jitterMs,omitempty"`
	LossPct     float64 `json:"lossPct"`
	Quality     string  `json:"quality"`
	BytesUp     int64   `json:"bytesUp"`
	BytesDown   int64   `json:"bytesDown"`
	ActiveCalls int64   `json:"activeCalls"`
	NAT         *NAT    `json:"nat,omitempty"`
	// Direct — с какими устройствами у этого сейчас прямое соединение
	// (минуя сервер), по отчёту самого устройства.
	Direct []DirectLink `json:"direct,omitempty"`
	// Профиль устройства в панели (meshd 5+, ТЗ 16): ждёт одобрения,
	// заморожено, серверы устройства с их цепочками выхода и цепочка,
	// которую последней задала панель этого сервера.
	Pending  bool         `json:"pending,omitempty"`
	Frozen   bool         `json:"frozen,omitempty"`
	Profiles []ProfileRef `json:"profiles,omitempty"`
	ExitSet  *ExitOrder   `json:"exitSet,omitempty"`
	// Обновление сети (meshd 8, ТЗ 40): сборка (vpn / proxy), автообновление
	// ("on" / "off", "" — старый клиент) и когда его меняли, как прошло
	// последнее обновление (по словам устройства), роль «Моё устройство».
	Build    string `json:"build,omitempty"`
	AutoUpd  string `json:"autoUpd,omitempty"`
	AutoUpdT int64  `json:"autoUpdT,omitempty"`
	UpdSt    string `json:"updSt,omitempty"`
	Mine     bool   `json:"mine,omitempty"`
}

// ProfileRef — сервер, для которого у устройства есть профиль (ключ
// «адрес:порт»), и цепочка выхода этого профиля: Via — ключи звеньев ("" —
// звено, которого на устройстве уже нет), T — когда её задали, By — какая
// панель (пусто — человек на самом устройстве).
type ProfileRef struct {
	Key    string   `json:"k"`
	Name   string   `json:"n,omitempty"`
	Active bool     `json:"a,omitempty"`
	Via    []string `json:"v,omitempty"`
	T      int64    `json:"t,omitempty"`
	By     string   `json:"b,omitempty"`
}

// ExitOrder — цепочка выхода, которую задала панель.
type ExitOrder struct {
	Entry string   `json:"entry"`
	Via   []string `json:"via,omitempty"`
	T     int64    `json:"t"`
	By    string   `json:"by,omitempty"`
	ByID  string   `json:"byId,omitempty"`
}

// DirectLink — прямое соединение с другим устройством сети.
type DirectLink struct {
	IP    string  `json:"ip"`
	RTTMs float64 `json:"rttMs,omitempty"`
}

// NAT — что устройство узнало о своём NAT (см. mesh.NATInfo).
type NAT struct {
	Tested        int64  `json:"tested"`
	UDP           bool   `json:"udp"`
	Mapping       string `json:"mapping,omitempty"`
	Filtering     string `json:"filtering,omitempty"`
	PublicIP      string `json:"publicIp,omitempty"`
	PortPreserved bool   `json:"portPreserved,omitempty"`
	PortDelta     int    `json:"portDelta,omitempty"`
	IPv6          bool   `json:"ipv6,omitempty"`
}

// Network — одна сеть (все устройства с одинаковым ключом).
type Network struct {
	ID string `json:"id"`
	// Name — имя сети из панели; Created — когда сеть появилась (unix);
	// EmptySince — с какого момента в ней нет устройств (0 — есть).
	// Старый meshd этих полей не присылает — они нулевые.
	Name       string `json:"name"`
	Created    int64  `json:"created"`
	EmptySince int64  `json:"emptySince"`
	// Approval — «Новые устройства — после одобрения» (meshd 5+).
	Approval bool     `json:"approval,omitempty"`
	Devices  []Device `json:"devices"`
}

// Server — сервер, подключённый к этому старым способом (проброс).
type Server struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Host        string   `json:"host"`
	IP          string   `json:"ip,omitempty"` // адрес в сети устройств
	MeshHost    string   `json:"meshHost,omitempty"`
	Addrs       []string `json:"addrs,omitempty"`
	App         string   `json:"app,omitempty"`
	ConnectedAt int64    `json:"connectedAt"`
	RTTMs       float64  `json:"rttMs,omitempty"`
	RTTAvgMs    float64  `json:"rttAvgMs,omitempty"`
	JitterMs    float64  `json:"jitterMs,omitempty"`
	LossPct     float64  `json:"lossPct"`
	Quality     string   `json:"quality"`
}

// Self — этот сервер как узел сети устройств.
type Self struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	IP      string `json:"ip"`
	Enabled bool   `json:"enabled"`
	Ports   string `json:"ports,omitempty"`
	// ID — id этого сервера, Pol — отпечаток его правил доступа (meshd 4+).
	ID  string `json:"id,omitempty"`
	Pol string `json:"pol,omitempty"`
}

// SeenServer — другой сервер той же сети, о котором рассказали устройства
// (они на связи и с ним). Same — правила доступа там такие же (по
// отпечатку); Devices — сколько устройств этого сервера сейчас на связи и
// с тем.
type SeenServer struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Pol      string `json:"pol,omitempty"`
	Same     bool   `json:"same"`
	Devices  int    `json:"devices"`
	LastSeen int64  `json:"lastSeen,omitempty"`
}

// KnownServer — сервер сети, который панель советует устройствам. С meshd 6
// (ТЗ 22) ещё id его meshd, SSH-ключ сервера и id сети серверов: по ним
// устройство само добавляет сервер, если его учётная запись там есть.
type KnownServer struct {
	Name    string `json:"name,omitempty"`
	Host    string `json:"host"`
	Port    int    `json:"port,omitempty"`
	ID      string `json:"id,omitempty"`
	HostKey string `json:"hk,omitempty"`
	Net     string `json:"sn,omitempty"`
}

// State — ответ meshd на /v1/state.
type State struct {
	Version   string `json:"version"`
	StartedAt int64  `json:"startedAt"`
	// Release — из какого релиза собран meshd ("v1.10.5"; пусто — собран
	// руками или на сервере), Built — время его файла (ТЗ 25; у старого
	// meshd обоих нет).
	Release  string    `json:"release,omitempty"`
	Built    int64     `json:"built,omitempty"`
	Networks []Network `json:"networks"`
	Servers  []Server  `json:"servers"`
	Self     Self      `json:"self"`
	// Peers и Known — несколько серверов без главного (meshd 4+; у старого
	// пусто).
	Peers []SeenServer  `json:"peers"`
	Known []KnownServer `json:"known"`
	// Relays — идущие пересланные соединения; Relay — панель ждёт вызовов
	// на пересылку (meshd 7, ТЗ 24).
	Relays []RelayLine `json:"relays,omitempty"`
	Relay  bool        `json:"relay,omitempty"`
	// NetVer — версия сети устройств, которую meshd говорит устройствам
	// (meshd 8, ТЗ 40).
	NetVer string `json:"netVer,omitempty"`
}

// Admin — клиент входа meshd для панели (HTTP по unix-сокету).
type Admin struct {
	http *http.Client
	// dial — сырое соединение с сокетом (пересылка между серверами, meshd 7).
	dial func(ctx context.Context) (net.Conn, error)
}

// NewAdmin — клиент для сокета sock (обычно AdminSocket).
func NewAdmin(sock string) *Admin {
	dial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}
	return &Admin{dial: dial, http: &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}}
}

// ErrNotRunning — meshd не отвечает: не установлен, остановлен или запущен
// без входа для панели (старая версия).
var ErrNotRunning = errors.New("meshd не отвечает")

// ErrUnsupported — meshd старой версии не знает такого запроса.
var ErrUnsupported = errors.New("эта версия meshd не умеет этого — обновите meshd на сервере (Сеть устройств → переустановить)")

func (a *Admin) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://meshd"+path, rd)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound && !json.Valid(data) {
		// Старый meshd: путь неизвестен, отвечает текстом «404 page not found».
		return ErrUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("meshd ответил %s", resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// State — все сети, устройства и подключённые серверы.
func (a *Admin) State(ctx context.Context) (State, error) {
	var st State
	err := a.do(ctx, http.MethodGet, "/v1/state", nil, &st)
	return st, err
}

type deviceReq struct {
	Net    string `json:"net"`
	Device string `json:"device"`
	Alias  string `json:"alias,omitempty"`
}

// Rename задаёт устройству имя; пустое — вернуть его собственное.
func (a *Admin) Rename(ctx context.Context, netID, device, alias string) error {
	return a.do(ctx, http.MethodPost, "/v1/rename", deviceReq{Net: netID, Device: device, Alias: alias}, nil)
}

// Forget убирает устройство из сети.
func (a *Admin) Forget(ctx context.Context, netID, device string) error {
	return a.do(ctx, http.MethodPost, "/v1/forget", deviceReq{Net: netID, Device: device}, nil)
}

// Event — запись журнала событий meshd (см. /v1/events). Net, Device и Peer —
// id; Text — подпись для человека.
type Event struct {
	Seq    int64  `json:"seq"`
	Time   int64  `json:"time"`
	Type   string `json:"type"`
	Net    string `json:"net,omitempty"`
	Device string `json:"device,omitempty"`
	Peer   string `json:"peer,omitempty"`
	Port   int    `json:"port,omitempty"`
	Text   string `json:"text,omitempty"`
}

// Events — ответ /v1/events: Seq — номер последнего события в журнале.
type Events struct {
	Seq    int64   `json:"seq"`
	Events []Event `json:"events"`
}

// CheckResult — ответ /v1/check: можно ли устройству From открыть соединение
// к To на порт. Rule — индекс правила или -1; Reason — «rule», «policy off»,
// «no rule», «unknown device», «server cannot initiate», а с meshd 5 ещё
// «pending» и «frozen» (одно из двух ждёт одобрения или заморожено).
type CheckResult struct {
	Allowed bool   `json:"allowed"`
	Rule    int    `json:"rule"`
	Reason  string `json:"reason"`
	Visible bool   `json:"visible"`
}

// RenameNetwork задаёт сети имя (пустое — убрать).
func (a *Admin) RenameNetwork(ctx context.Context, netID, name string) error {
	return a.do(ctx, http.MethodPost, "/v1/net/rename", map[string]string{"net": netID, "name": name}, nil)
}

// DeleteNetwork удаляет сеть и отключает её устройства.
func (a *Admin) DeleteNetwork(ctx context.Context, netID string) error {
	return a.do(ctx, http.MethodPost, "/v1/net/delete", map[string]string{"net": netID}, nil)
}

// Events — события журнала с номером больше after (limit 0 — по умолчанию).
func (a *Admin) Events(ctx context.Context, after int64, limit int) (Events, error) {
	var ev Events
	path := fmt.Sprintf("/v1/events?after=%d&limit=%d", after, limit)
	err := a.do(ctx, http.MethodGet, path, nil, &ev)
	if ev.Events == nil {
		ev.Events = []Event{}
	}
	return ev, err
}

// Check спрашивает, пустят ли устройство from к устройству to на порт.
func (a *Admin) Check(ctx context.Context, netID, from, to string, port int) (CheckResult, error) {
	var res CheckResult
	err := a.do(ctx, http.MethodPost, "/v1/check", map[string]any{"net": netID, "from": from, "to": to, "port": port}, &res)
	return res, err
}

// SetSelf — как этот сервер выглядит в сети устройств: имя, пускать ли к нему
// устройства и на какие порты ("" — на все).
func (a *Admin) SetSelf(ctx context.Context, name string, enabled bool, ports string) error {
	return a.do(ctx, http.MethodPost, "/v1/self", map[string]any{"name": name, "enabled": enabled, "ports": ports}, nil)
}

// SetSelfIP — адрес этого сервера в сети устройств (meshd 4+): если два
// сервера сети получили один и тот же или нужно сохранить прежний.
func (a *Admin) SetSelfIP(ctx context.Context, ip string) error {
	st, err := a.State(ctx)
	if err != nil {
		return err
	}
	return a.do(ctx, http.MethodPost, "/v1/self", map[string]any{"name": st.Self.Name,
		"enabled": st.Self.Enabled, "ports": st.Self.Ports, "ip": ip}, nil)
}

// SetKnown — какие серверы сети советовать устройствам (meshd 4+).
func (a *Admin) SetKnown(ctx context.Context, list []KnownServer) error {
	if list == nil {
		list = []KnownServer{}
	}
	return a.do(ctx, http.MethodPost, "/v1/known", map[string]any{"servers": list}, nil)
}

// SetKnownShared — серверы сети для устройств и SSH-пользователи, чьи
// учётные записи есть на всех серверах сети (meshd 6+; старый meshd возьмёт
// только список серверов).
func (a *Admin) SetKnownShared(ctx context.Context, list []KnownServer, shared []string) error {
	if list == nil {
		list = []KnownServer{}
	}
	if shared == nil {
		shared = []string{}
	}
	return a.do(ctx, http.MethodPost, "/v1/known", map[string]any{"servers": list, "shared": shared}, nil)
}

// SetApproval включает «Новые устройства — после одобрения» (meshd 5+).
func (a *Admin) SetApproval(ctx context.Context, netID string, on bool) error {
	return a.do(ctx, http.MethodPost, "/v1/net/approval", map[string]any{"net": netID, "on": on}, nil)
}

// Approve пускает в сеть устройство, которое ждёт одобрения (meshd 5+).
func (a *Admin) Approve(ctx context.Context, netID, device string) error {
	return a.do(ctx, http.MethodPost, "/v1/approve", deviceReq{Net: netID, Device: device}, nil)
}

// Freeze замораживает (on) или размораживает устройство (meshd 5+).
func (a *Admin) Freeze(ctx context.Context, netID, device string, on bool) error {
	return a.do(ctx, http.MethodPost, "/v1/freeze", map[string]any{"net": netID, "device": device, "on": on}, nil)
}

// SetExit задаёт устройству цепочку выхода: вход через entry, выход через
// via (ключи «адрес:порт»; пусто — без цепочки). meshd 5+.
func (a *Admin) SetExit(ctx context.Context, netID, device, entry string, via []string) (ExitOrder, error) {
	if via == nil {
		via = []string{}
	}
	var out struct {
		Exit *ExitOrder `json:"exit"`
	}
	err := a.do(ctx, http.MethodPost, "/v1/exit", map[string]any{"net": netID, "device": device, "entry": entry, "via": via}, &out)
	if err == nil && out.Exit != nil {
		return *out.Exit, nil
	}
	return ExitOrder{}, err
}

// ForgetServer забывает адрес отключённого сервера старого способа.
func (a *Admin) ForgetServer(ctx context.Context, id string) error {
	return a.do(ctx, http.MethodPost, "/v1/forget-server", deviceReq{Device: id}, nil)
}

// PolicyRule — правило доступа: устройства группы From могут открывать
// соединения к устройствам группы To на порты Ports (пусто — на все).
// Группа "*" — все, "@servers" — серверы сети.
type PolicyRule struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Ports string `json:"ports,omitempty"`
}

// Policy — права доступа устройств для meshd: Users — SSH-пользователь
// сервера (tun_*) → его группы.
type Policy struct {
	Enabled bool                `json:"enabled"`
	Users   map[string][]string `json:"users,omitempty"`
	Rules   []PolicyRule        `json:"rules,omitempty"`
	// NoMesh — пользователи, которым сеть устройств не разрешена (meshd 9,
	// ТЗ 47). Старый meshd поле не знает и пускает их, как прежде.
	NoMesh []string `json:"nomesh,omitempty"`
}

// SetPolicy передаёт meshd права доступа устройств (он их сохраняет сам).
func (a *Admin) SetPolicy(ctx context.Context, p Policy) error {
	return a.do(ctx, http.MethodPost, "/v1/policy", p, nil)
}

// ---------- пересылка между серверами (meshd 7, ТЗ 24) ----------

// RemotePeer — устройство или сервер сети на другом сервере (см.
// remotePeer в cmd/meshd).
type RemotePeer struct {
	IP       string `json:"ip"`
	Host     string `json:"host"`
	Name     string `json:"name,omitempty"`
	User     string `json:"user,omitempty"`
	Platform string `json:"platform,omitempty"`
	OS       string `json:"os,omitempty"`
	Server   bool   `json:"server,omitempty"`
	Srv      string `json:"srv"`
	SrvName  string `json:"srvName,omitempty"`
	Online   bool   `json:"online"`
}

// RelayFrom — звонящее устройство.
type RelayFrom struct {
	IP       string `json:"ip"`
	Host     string `json:"host"`
	Name     string `json:"name,omitempty"`
	PID      string `json:"pid,omitempty"`
	User     string `json:"user,omitempty"`
	Platform string `json:"platform,omitempty"`
}

// RelayAsk — вызов на пересылку (от meshd этого сервера или с другого).
type RelayAsk struct {
	Call string    `json:"call,omitempty"`
	Net  string    `json:"net"`
	To   string    `json:"to"`
	Port int       `json:"port"`
	Srv  string    `json:"srv"`
	From RelayFrom `json:"from"`
}

// RelayLine — идущее пересланное соединение.
type RelayLine struct {
	Net   string `json:"net"`
	Dir   string `json:"dir"`
	Dev   string `json:"device"`
	To    string `json:"to"`
	Port  int    `json:"port"`
	Srv   string `json:"srv,omitempty"`
	Since int64  `json:"since"`
}

// SetRemote — кто есть в сети на других серверах (по id сетей).
func (a *Admin) SetRemote(ctx context.Context, nets map[string][]RemotePeer) error {
	return a.do(ctx, http.MethodPost, "/v1/remote", map[string]any{"nets": nets}, nil)
}

// bufConn — соединение после ответа 200: остаток буфера не теряется.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// CloseWrite — полузакрытие, если соединение его умеет.
func (c *bufConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

// raw — запрос, после ответа 200 на котором соединение остаётся под сырые
// байты.
func (a *Admin) raw(ctx context.Context, method, path string, body any) (net.Conn, *bufio.Reader, error) {
	if a.dial == nil {
		return nil, nil, ErrUnsupported
	}
	c, err := a.dial(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, "http://meshd"+path, bytes.NewReader(data))
	if body == nil {
		req.Body, req.ContentLength = nil, 0
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	if err := req.Write(c); err != nil {
		c.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		c.Close()
		if resp.StatusCode == http.StatusNotFound && !json.Valid(raw) {
			return nil, nil, ErrUnsupported
		}
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return nil, nil, errors.New(e.Error)
		}
		return nil, nil, fmt.Errorf("meshd ответил %s", resp.Status)
	}
	c.SetDeadline(time.Time{})
	return c, br, nil
}

// RelayWait ждёт вызовов на пересылку и отдаёт каждый в on, пока не
// отменят ctx или meshd не закроет соединение. Старый meshd —
// ErrUnsupported.
func (a *Admin) RelayWait(ctx context.Context, on func(RelayAsk)) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	c, br, err := a.raw(cctx, http.MethodGet, "/v1/relay-wait", nil)
	cancel()
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	defer c.Close()
	for {
		// meshd шлёт «жив» раз в 20 секунд: тишина дольше — связь пропала.
		c.SetReadDeadline(time.Now().Add(70 * time.Second))
		line, err := br.ReadBytes('\n')
		if err != nil {
			return err
		}
		var q RelayAsk
		if json.Unmarshal(line, &q) == nil && q.Call != "" {
			on(q)
		}
	}
}

// RelayOut — ответ на вызов: ok — соединение до звонящего устройства.
func (a *Admin) RelayOut(ctx context.Context, call string, ok bool, errText string) (net.Conn, error) {
	body := map[string]any{"call": call, "ok": ok, "error": errText}
	if !ok {
		return nil, a.do(ctx, http.MethodPost, "/v1/relay-out", body, nil)
	}
	c, br, err := a.raw(ctx, http.MethodPost, "/v1/relay-out", body)
	if err != nil {
		return nil, err
	}
	return &bufConn{Conn: c, r: br}, nil
}

// RelayIn — вызов с другого сервера к своему устройству или к самому этому
// серверу: соединение с целью или отказ meshd.
func (a *Admin) RelayIn(ctx context.Context, q RelayAsk) (net.Conn, error) {
	c, br, err := a.raw(ctx, http.MethodPost, "/v1/relay-in", q)
	if err != nil {
		return nil, err
	}
	return &bufConn{Conn: c, r: br}, nil
}
