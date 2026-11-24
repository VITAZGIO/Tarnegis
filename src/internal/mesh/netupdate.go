package mesh

// Обновление сети устройств (ТЗ 40) — сторона устройства. Сервер (meshd 8)
// говорит версию сети в welcome и в op netver: Update — «обновись сейчас»
// (у устройства включено автообновление, или в панели нажали «Обновить» —
// тогда ещё и Force). Что делать — решает хозяин (updater.Auto): сеть
// передаёт только номер, файл каждое устройство берёт само из релиза на
// GitHub.
//
// Автообновление устройства уходит в hello и в op upd (AutoUpdate,
// ReportUpdate); панель может поменять его (op autoupd) — новее ли оно,
// решает хозяин (OnAutoUpdate).
//
// RequestNetUpdate — «Обновить сеть до vX» с устройства с ролью «Моё
// устройство»: просьба идёт на серверы, где роль есть, ответ — от панели.
// Старый meshd про всё это не знает: версии сети нет, просьба не уходит.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// NetVersion — что сервер сказал о версии сети.
type NetVersion struct {
	// Version — версия сети (самая новая по серверам на связи).
	Version string `json:"version"`
	// Update — сервер просит обновиться сейчас; Force — это нажали в панели
	// (автообновление не смотрится).
	Update bool `json:"update,omitempty"`
	Force  bool `json:"force,omitempty"`
	// Mine — у устройства роль «Моё устройство» хоть на одном сервере.
	Mine bool `json:"mine,omitempty"`
	// Server — какой сервер сказал.
	Server string `json:"server,omitempty"`
}

// ErrNetUpdateUnsupported — ни один сервер на связи не умеет «обновить сеть»
// (старый meshd) или нигде нет роли «Моё устройство».
var ErrNetUpdateUnsupported = errors.New("обновить сеть отсюда нельзя: нет сервера, где у этого устройства роль «Моё устройство» (или там старый meshd)")

// updWire — автообновление и сборка в hello / op upd.
func (c *Client) updWire(m *wire) {
	m.Build = c.cfg.Build
	if c.cfg.AutoUpdate == nil {
		return
	}
	on, t := c.cfg.AutoUpdate()
	m.Au, m.AuT = "off", t
	if on {
		m.Au = "on"
	}
	if c.cfg.UpdateStatus != nil {
		m.UpdSt = c.cfg.UpdateStatus()
	}
}

// netInfoLocked — версия сети по серверам на связи и есть ли роль «моё».
func (c *Client) netInfoLocked() (ver string, mine bool) {
	for _, l := range c.links {
		if l.ctrl == nil {
			continue
		}
		if l.netVer != "" && (ver == "" || versionLess(ver, l.netVer)) {
			ver = l.netVer
		}
		mine = mine || l.mine && l.netVer != ""
	}
	return
}

// NetInfo — версия сети и роль «Моё устройство» (см. Status).
func (c *Client) NetInfo() (ver string, mine bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.netInfoLocked()
}

// netVersionFrom — сервер l сказал версию сети (welcome или op netver).
func (c *Client) netVersionFrom(l *srvLink, m wire) {
	if c.cfg.AutoUpdate == nil {
		return // устройство в обновлении сети не участвует
	}
	if m.AuT > 0 && (m.Au == "on" || m.Au == "off") && c.cfg.OnAutoUpdate != nil {
		if _, own := c.cfg.AutoUpdate(); m.AuT > own {
			go c.cfg.OnAutoUpdate(m.Au == "on", m.AuT)
		}
	}
	if m.NetVer == "" || c.cfg.OnNetVersion == nil {
		return
	}
	c.mu.Lock()
	l.netVer, l.mine = m.NetVer, m.Mine
	ver, mine := c.netInfoLocked()
	name := l.info.Name
	if name == "" {
		name = l.key
	}
	c.mu.Unlock()
	nv := NetVersion{Version: ver, Mine: mine, Server: name}
	if m.Upd && m.NetVer == ver {
		nv.Update, nv.Force = true, m.Force
	}
	go c.cfg.OnNetVersion(nv)
}

// netMessage — op netver, autoupd и ответ на netupdate.
func (c *Client) netMessage(l *srvLink, m wire) {
	switch m.Op {
	case "netver":
		c.netVersionFrom(l, m)
	case "autoupd":
		if c.cfg.OnAutoUpdate != nil && (m.Au == "on" || m.Au == "off") {
			go c.cfg.OnAutoUpdate(m.Au == "on", m.AuT)
		}
	case "netupdate":
		c.mu.Lock()
		ch := c.netCalls[m.Call]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- m:
			default:
			}
		}
	}
}

// ReportUpdate рассказывает серверам автообновление и ход обновления (op
// upd): после того как человек или панель их поменяли.
func (c *Client) ReportUpdate() {
	if c.cfg.AutoUpdate == nil {
		return
	}
	m := wire{Op: "upd"}
	c.updWire(&m)
	c.mu.Lock()
	var targets []func(wire) error
	for _, l := range c.links {
		if l.ctrl != nil {
			targets = append(targets, l.ctrl)
		}
	}
	c.mu.Unlock()
	for _, f := range targets {
		f(m)
	}
}

var netCallSeq atomic.Int64

// netUpdateWait — сколько ждать ответа панели (она забирает просьбы раз в
// несколько секунд).
var netUpdateWait = 45 * time.Second

// RequestNetUpdate просит серверы, где у устройства роль «Моё устройство»,
// обновить сеть до ver («Обновить на всех серверах»). nil — хоть один
// сервер принял.
func (c *Client) RequestNetUpdate(ctx context.Context, ver string) error {
	ver = strings.TrimPrefix(strings.TrimSpace(ver), "v")
	if ver == "" {
		return errors.New("неверный номер версии")
	}
	call := "nu" + strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatInt(netCallSeq.Add(1), 36)
	ch := make(chan wire, 8)
	c.mu.Lock()
	var targets []func(wire) error
	for _, l := range c.links {
		if l.ctrl != nil && l.mine && l.netVer != "" {
			targets = append(targets, l.ctrl)
		}
	}
	if len(targets) > 0 {
		if c.netCalls == nil {
			c.netCalls = map[string]chan wire{}
		}
		c.netCalls[call] = ch
	}
	c.mu.Unlock()
	if len(targets) == 0 {
		return ErrNetUpdateUnsupported
	}
	defer func() {
		c.mu.Lock()
		delete(c.netCalls, call)
		c.mu.Unlock()
	}()
	sent := 0
	for _, f := range targets {
		if f(wire{Op: "netupdate", Call: call, NetVer: ver}) == nil {
			sent++
		}
	}
	if sent == 0 {
		return errNoServer
	}
	ctx, cancel := context.WithTimeout(ctx, netUpdateWait)
	defer cancel()
	var last error
	for got := 0; got < sent; got++ {
		select {
		case m := <-ch:
			if m.OK {
				return nil
			}
			last = errors.New(m.Error)
		case <-ctx.Done():
			if last != nil {
				return last
			}
			return errors.New("сервер не ответил — панель там старая или выключена")
		}
	}
	return last
}

// versionLess — a старше b (номера через точку, «v» и хвост «-…» не в счёт);
// не номер — false.
func versionLess(a, b string) bool {
	pa, oka := verParts(a)
	pb, okb := verParts(b)
	if !oka || !okb {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func verParts(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	parts := strings.Split(v, ".")
	if v == "" || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
