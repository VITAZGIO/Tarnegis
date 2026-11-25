package panel

// Вход по приглашению (ТЗ 47, контракт раздел B) — системная часть.
//
// Программа входит по SSH пользователем tun_join со своим новым ключом и
// просит exec «join». sshd пускает tun_join с любым ключом, но только так:
//   - ключ берётся не из файла, а у AuthorizedKeysCommand — системный echo
//     печатает «restrict <тип> <ключ>»: предложенный ключ годится, но без
//     проброса, агента, X11 и терминала; разбирает ключ сам sshd. Именно
//     echo из /usr/bin: sshd запускает AuthorizedKeysCommand, только если
//     весь путь к нему принадлежит root и закрыт для записи другим, а
//     /usr/local/bin бывает и не таким (это поймал стенд join-lab);
//   - Match User tun_join: ForceCommand «ssh_tunnel_panel join» (что бы
//     программа ни попросила), AllowTcpForwarding no, PermitTTY no и прочее
//     закрыто; ExposeAuthInfo yes — sshd кладёт в файл $SSH_USER_AUTH ключ,
//     которым вошли на самом деле;
//   - команда join читает одну строку JSON со stdin, берёт ключ из
//     $SSH_USER_AUTH и адрес из $SSH_CONNECTION и пересылает всё панели на
//     127.0.0.1:47832/v1/join. Панель отвечает только сокету пользователя
//     tun_join (uid по /proc/net/tcp, как у selfservice.go).
//
// Подменить ключ не выйдет и смысла нет: с верным секретом можно завести
// устройство с любым своим ключом, без секрета — ни с каким. Отказы панель
// пишет строкой «ssh_tunnel join: refused (<код>) from <адрес>» — её ловит
// fail2ban (свой фильтр ssh_tunnel-join), плюс ограничение попыток в самой
// панели.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// JoinUser — системный пользователь для входа по приглашению. Не похож на
// пользователя устройства (ValidUsername — только tun_ + 16 hex): из сети
// серверов такую учётную запись не завести.
const JoinUser = "tun_join"

const (
	joinMarkerBegin = "# BEGIN ssh_tunnel_panel join (не редактируй руками — правь через панель)"
	joinMarkerEnd   = "# END ssh_tunnel_panel join"
)

// joinSSHDBlock — ограничения sshd для tun_join; exe — путь к панели.
func joinSSHDBlock(exe string) string {
	echo := "/usr/bin/echo"
	if _, err := os.Stat(echo); err != nil {
		echo = "/bin/echo"
	}
	return joinMarkerBegin + "\n" +
		"Match User " + JoinUser + "\n" +
		"    AuthenticationMethods publickey\n" +
		"    PubkeyAuthentication yes\n" +
		"    AuthorizedKeysFile none\n" +
		"    AuthorizedKeysCommand " + echo + " restrict %t %k\n" +
		"    AuthorizedKeysCommandUser nobody\n" +
		"    ExposeAuthInfo yes\n" +
		"    ForceCommand " + exe + " join\n" +
		"    AllowTcpForwarding no\n" +
		"    AllowStreamLocalForwarding no\n" +
		"    AllowAgentForwarding no\n" +
		"    X11Forwarding no\n" +
		"    PermitTunnel no\n" +
		"    PermitTTY no\n" +
		"    PermitUserRC no\n" +
		"    MaxSessions 1\n" +
		joinMarkerEnd + "\n"
}

// ensureJoinBlock — как ensureLinkBlock: дописать или обновить блок.
func ensureJoinBlock(config, exe string) (string, bool) {
	block := joinSSHDBlock(exe)
	if i := strings.Index(config, joinMarkerBegin); i >= 0 {
		j := strings.Index(config[i:], joinMarkerEnd)
		if j < 0 {
			return config, false
		}
		end := i + j + len(joinMarkerEnd)
		if end < len(config) && config[end] == '\n' {
			end++
		}
		if config[i:end] == block {
			return config, false
		}
		return config[:i] + block + config[end:], true
	}
	out := config
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if out != "" {
		out += "\n"
	}
	return out + block, true
}

// joinExe — путь панели для sshd: AuthorizedKeysCommand должен лежать в
// папке, куда пишет только root.
func joinExe() string {
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		if strings.HasPrefix(exe, "/usr/") || strings.HasPrefix(exe, "/opt/") {
			return exe
		}
	}
	return "/usr/local/bin/ssh_tunnel_panel"
}

// EnsureJoinAccess — при старте панели: пользователь tun_join, блок sshd и
// fail2ban. Ошибки не фатальны: без этого не работает только вход по
// приглашению.
func EnsureJoinAccess(sshPort int) error {
	if _, err := user.Lookup(JoinUser); err != nil {
		// Оболочка /bin/sh нужна, чтобы sshd смог запустить ForceCommand;
		// ничего другого tun_join не запустит (см. блок выше).
		if err := runSystem("useradd", "--system", "--no-create-home", "--home-dir", "/nonexistent",
			"--shell", "/bin/sh", JoinUser); err != nil {
			return err
		}
	}
	// Пароля нет вовсе, но учётка не заблокирована — заблокированную sshd
	// не пустит и по ключу.
	if err := runSystem("usermod", "-p", "*", JoinUser); err != nil {
		return err
	}
	current, err := os.ReadFile(SSHDPath)
	if err != nil {
		return fmt.Errorf("не могу прочитать %s: %w", SSHDPath, err)
	}
	if updated, changed := ensureJoinBlock(string(current), joinExe()); changed {
		if err := applySSHDConfig(updated); err != nil {
			return err
		}
	}
	return ensureJoinFail2ban(sshPort)
}

const (
	f2bFilterPath = "/etc/fail2ban/filter.d/ssh_tunnel-join.conf"
	f2bJailPath   = "/etc/fail2ban/jail.d/ssh_tunnel-join.local"
)

func joinFail2banFilter() string {
	return "# ssh_tunnel_panel: отказы входа по приглашению устройства (ТЗ 47)\n" +
		"[Definition]\n" +
		"failregex = ssh_tunnel join: refused \\(\\w+\\) from <HOST>\\s*$\n" +
		"journalmatch = _SYSTEMD_UNIT=ssh_tunnel_panel.service\n"
}

func joinFail2banJail(port int) string {
	if port <= 0 {
		port = 22
	}
	return "# ssh_tunnel_panel: отказы входа по приглашению устройства (ТЗ 47)\n" +
		"[ssh_tunnel-join]\n" +
		"enabled = true\n" +
		"backend = systemd\n" +
		"filter = ssh_tunnel-join\n" +
		fmt.Sprintf("port = %d\n", port) +
		"maxretry = 5\n" +
		"findtime = 10m\n" +
		"bantime = 1h\n"
}

// ensureJoinFail2ban — свой фильтр и тюрьма, если fail2ban стоит.
func ensureJoinFail2ban(port int) error {
	if _, err := os.Stat(filepath.Dir(f2bJailPath)); err != nil {
		return nil
	}
	changed := false
	for path, want := range map[string]string{f2bFilterPath: joinFail2banFilter(), f2bJailPath: joinFail2banJail(port)} {
		if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
			continue
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		if _, err := exec.LookPath("fail2ban-client"); err == nil {
			exec.Command("fail2ban-client", "reload").Run()
		}
	}
	return nil
}

// ---------- команды для sshd ----------

// userAuthKey — ключ из файла $SSH_USER_AUTH (ExposeAuthInfo): строки
// «publickey <тип> <base64>»; берётся последняя (успешный способ — в конце).
func userAuthKey(content string) string {
	key := ""
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "publickey" {
			key = f[1] + " " + f[2]
		}
	}
	return key
}

// RunJoin — ForceCommand для tun_join: одна строка запроса → одна строка
// ответа (контракт раздел B). post отправляет тело панели и возвращает её
// ответ (в тестах — напрямую).
func RunJoin(in io.Reader, out io.Writer, getenv func(string) string, post func([]byte) ([]byte, error)) int {
	reply := func(r joinResp) int {
		data, _ := json.Marshal(r)
		out.Write(append(data, '\n'))
		if r.OK {
			return 0
		}
		return 1
	}
	if strings.TrimSpace(getenv("SSH_ORIGINAL_COMMAND")) != "join" {
		return reply(joinResp{Err: joinUnknown})
	}
	line, err := bufio.NewReader(io.LimitReader(in, 8<<10)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return reply(joinResp{Err: joinUnknown})
	}
	var req joinReq
	if json.Unmarshal(bytes.TrimSpace(line), &req) != nil {
		return reply(joinResp{Err: joinUnknown})
	}
	// Ключ и адрес — только от sshd, не со слов программы.
	req.Key, req.From = "", ""
	if path := getenv("SSH_USER_AUTH"); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			req.Key = userAuthKey(string(data))
		}
	}
	if f := strings.Fields(getenv("SSH_CONNECTION")); len(f) > 0 {
		req.From = f[0]
	}
	body, _ := json.Marshal(req)
	resp, err := post(body)
	if err != nil {
		return reply(joinResp{Err: joinUnknown})
	}
	var r joinResp
	if json.Unmarshal(resp, &r) != nil || (!r.OK && r.Err == "") {
		return reply(joinResp{Err: joinUnknown})
	}
	return reply(r)
}

// PostJoin — настоящая отправка: на вход самообслуживания панели.
func PostJoin(body []byte) ([]byte, error) {
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Post("http://"+SelfServiceAddr+"/v1/join", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 16<<10))
}
