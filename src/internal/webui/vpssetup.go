// Мастер настройки VPS: то же самое, что раньше делалось руками по
// docs/SERVER_SETUP.md — положить на сервер свой ключ, прогнать harden.sh
// (шаг 2) и tunnel-user.sh (шаг 3), — но по SSH с паролем root, одной
// кнопкой, с построчным выводом в интерфейс вместо копирования команд в
// терминал.
//
// Пароль root используется только внутри runVpsSetup и функций, которым он
// передан явным параметром: он не сохраняется на диск, не публикуется в
// events.Bus и не возвращается наружу HTTP-обработчиком — тот же приём, что и
// пароль sudo для автозапуска (boot.go).
//
// Порядок шагов — как в документе, а не упрощённый: сначала ключ, потом
// проверка, что вход по нему действительно работает, и только после этого
// harden.sh отключает пароли. Если проверка не прошла, скрипты не
// запускаются вовсе — сервер остаётся как был, со входом по паролю.
package webui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/hostkey"
	"sshtunnel/internal/meshsvc"
	"sshtunnel/internal/tunnel"
	"sshtunnel/internal/updater"
)

// vpsSetupParams — то, что вводится в мастере настройки VPS.
type vpsSetupParams struct {
	Host            string
	Port            int
	User            string // root или обычный пользователь с правом sudo
	Password        string
	KeyPath         string
	InstallPanel    bool
	InstallUDPRelay bool
	InstallMesh     bool
	// PanelPublicPort — 0: панель слушает только 127.0.0.1 сервера (по
	// умолчанию, открывается из программы через временный проброс). Иначе —
	// открыта в интернет на этом порту по обычному HTTP: только если человек
	// сам так выбрал.
	PanelPublicPort int
	// AddProfile — добавить настроенный сервер в список программы. Вызывается
	// только после удачной настройки, до итогового события, — чтобы экран
	// итога сразу показал добавленный сервер. Заполняет ProfileID/Name/Note.
	AddProfile func(res *events.VpsResult)
}

const vpsDialTimeout = 20 * time.Second

// panelLocalAddr — где панель слушает на самом сервере (ssh_tunnel_panel,
// defaultListen). К нему и ведёт проброс «Открыть панель сервера».
const panelLocalAddr = "127.0.0.1:47823"

// runVpsSetup выполняет всю настройку и публикует ход построчно в bus.
// Ошибка на любом шаге останавливает всё, что после него, — но если она
// случилась до успешной проверки входа по ключу, пароли на сервере остаются
// включёнными: полунастроенного состояния без доступа не бывает.
func runVpsSetup(bus *events.Bus, p vpsSetupParams) (err error) {
	res := &events.VpsResult{Host: p.Host, AdminUser: p.User}
	defer func() {
		if err != nil {
			bus.VpsSetupDone(err.Error())
			return
		}
		bus.VpsSetupOK(res)
	}()

	if strings.TrimSpace(p.Host) == "" || p.Port <= 0 || strings.TrimSpace(p.User) == "" {
		return errors.New("не заполнены адрес, порт или пользователь")
	}
	if !usernameRe.MatchString(p.User) {
		return errors.New("имя пользователя: только латиница, цифры, «-» и «_», с буквы")
	}
	if p.Password == "" {
		return errors.New("не введён пароль")
	}
	if p.InstallPanel && p.PanelPublicPort != 0 {
		if p.PanelPublicPort < 1 || p.PanelPublicPort > 65535 {
			return errors.New("порт панели: число от 1 до 65535")
		}
		if p.PanelPublicPort == p.Port {
			return errors.New("порт панели совпадает с SSH-портом — выбери другой")
		}
	}

	pub, _, err := ensureKey(p.KeyPath)
	if err != nil {
		return fmt.Errorf("не удалось подготовить свой SSH-ключ: %w", err)
	}
	signer, err := loadSigner(p.KeyPath)
	if err != nil {
		return fmt.Errorf("не удалось прочитать свой SSH-ключ: %w", err)
	}

	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))

	// Повторный запуск (прошлый оборвался на середине): ключ этого компьютера
	// уже лежит на сервере, а вход по паролю harden.sh мог уже выключить.
	// Тогда входим по ключу и продолжаем — шаги дальше можно повторять.
	if kc, kerr := vpsDial(p.User, addr, ssh.PublicKeys(signer), bus); kerr == nil {
		defer kc.Close()
		bus.VpsSetupLine("connect", "Этот компьютер уже входит на сервер по ключу — продолжаю настройку (повторный запуск)")
		kRoot := &rootRunner{client: kc}
		if p.User != "root" {
			bus.VpsSetupLine("connect", "Проверяю, что у "+p.User+" есть право sudo")
			if err := kRoot.detectSudo(p.User, p.Password); err != nil {
				return err
			}
			bus.VpsSetupLine("connect", "sudo работает")
		}
		return finishVpsSetup(bus, p, res, pub, kc, kRoot)
	}

	bus.VpsSetupLine("connect", fmt.Sprintf("Подключаюсь к %s под %s по паролю…", addr, p.User))
	pwClient, err := vpsDial(p.User, addr, ssh.Password(p.Password), bus)
	if err != nil {
		return fmt.Errorf("не удалось подключиться по паролю: %w", classifySetupErr(err, p.User))
	}
	defer pwClient.Close()
	bus.VpsSetupLine("connect", "Подключение по паролю установлено")

	// Права root нужны всем шагам дальше. Обычного пользователя проверяем
	// сразу, пока на сервере ещё ничего не тронуто: без sudo мастер не
	// сделает ничего, и лучше сказать это до того, как класть ключи.
	pwRoot := &rootRunner{client: pwClient}
	if p.User != "root" {
		bus.VpsSetupLine("connect", "Проверяю, что у "+p.User+" есть право sudo")
		if err := pwRoot.detectSudo(p.User, p.Password); err != nil {
			return err
		}
		bus.VpsSetupLine("connect", "sudo работает")
	}

	bus.VpsSetupLine("key", "Кладу на сервер открытый ключ этого компьютера")
	if err := installPubKey(pwClient, pub); err != nil {
		return fmt.Errorf("не удалось положить ключ на сервер: %w", err)
	}
	bus.VpsSetupLine("key", "Ключ записан в authorized_keys")

	bus.VpsSetupLine("key", "Проверяю вход по ключу — без этого отключать пароли нельзя")
	keyClient, err := vpsDial(p.User, addr, ssh.PublicKeys(signer), bus)
	if err != nil {
		return fmt.Errorf(
			"ключ положен, но вход по нему не удался — пароли на сервере НЕ отключены (%w)",
			classifySSHErr(err))
	}
	defer keyClient.Close()
	bus.VpsSetupLine("key", "Вход по ключу работает")

	// Дальше всё — под root: напрямую или через sudo под тем же
	// пользователем (пароль sudo — тот же, что для входа; после harden.sh
	// вход по паролю выключен, но sudo его по-прежнему спрашивает).
	return finishVpsSetup(bus, p, res, pub, keyClient, pwRoot)
}

// finishVpsSetup — всё, что после входа по ключу: защита, пользователь
// туннеля, панель и остальное. sr — как получать root (sudo и его пароль).
// Шаги повторяемы, поэтому повторный запуск мастера после обрыва идёт сюда
// же сразу по ключу.
func finishVpsSetup(bus *events.Bus, p vpsSetupParams, res *events.VpsResult, pub string,
	keyClient *ssh.Client, sr *rootRunner) error {
	root := &rootRunner{client: keyClient, sudo: sr.sudo, sudoPass: sr.sudoPass}

	if p.User != "root" {
		// harden.sh отказывается отключать пароли, пока у root нет ни одного
		// ключа: это его защита от потери входа. Кладём туда тот же ключ —
		// root по ключу останется (по паролю — нет), это и есть запасной вход.
		bus.VpsSetupLine("key", "Кладу тот же ключ для root — без него harden.sh не отключит пароли")
		if err := root.script(rootKeyScript(pub), func(line string) { bus.VpsSetupLine("key", line) }); err != nil {
			return fmt.Errorf("не удалось положить ключ для root: %w", err)
		}
	}

	bus.VpsSetupLine("harden", "Запускаю базовую защиту сервера (harden.sh)")
	// NewUser пустой: отдельный sudo-пользователь тут не нужен, вход и так
	// уже по ключу под администратором — заводить его просто ради заведения
	// было бы лишним шагом. Пользователь для самого туннеля появится
	// следующим шагом.
	hardenScript, err := renderHarden(hardenParams{NewUser: "", SSHPort: p.Port, Timezone: ""})
	if err != nil {
		return err
	}
	if err := root.script(hardenScript, func(line string) { bus.VpsSetupLine("harden", line) }); err != nil {
		return fmt.Errorf("harden.sh завершился с ошибкой: %w", err)
	}
	bus.VpsSetupLine("harden", "harden.sh готово")

	bus.VpsSetupLine("tunnel-user", "Завожу отдельного пользователя для туннеля (tunnel-user.sh)")
	tunnelScript, err := renderTunnelUser(tunnelUserParams{TunnelUser: "tunnel", TunnelKey: "", BlockInternals: 1})
	if err != nil {
		return err
	}
	if err := root.script(tunnelScript, func(line string) { bus.VpsSetupLine("tunnel-user", line) }); err != nil {
		return fmt.Errorf("tunnel-user.sh завершился с ошибкой: %w", err)
	}
	bus.VpsSetupLine("tunnel-user", "tunnel-user.sh готово")

	if p.InstallPanel {
		bus.VpsSetupLine("panel", "Ставлю веб-панель ssh_tunnel_panel")
		login, pass, note, perr := installPanel(root, p.Host, p.Port, p.PanelPublicPort,
			func(line string) { bus.VpsSetupLine("panel", line) })
		if perr != nil {
			bus.VpsSetupLine("panel", "Веб-панель не установлена: "+perr.Error())
			res.PanelNote = perr.Error()
		} else {
			res.Panel = true
			res.PanelPublic = p.PanelPublicPort
			res.PanelLogin, res.PanelPass, res.PanelNote = login, pass, note
			if p.PanelPublicPort != 0 {
				bus.VpsSetupLine("panel", fmt.Sprintf("Панель открыта в интернет: http://%s/ — без шифрования, "+
					"сразу смени пароль; надёжнее закрыть порт и открывать её из программы",
					net.JoinHostPort(p.Host, strconv.Itoa(p.PanelPublicPort))))
			} else {
				bus.VpsSetupLine("panel", "Панель слушает только сам сервер — открыть её: кнопка «Открыть панель сервера»")
			}
			if pass != "" {
				bus.VpsSetupLine("panel", "Логин: "+login+", одноразовый пароль: "+pass)
			} else if note != "" {
				bus.VpsSetupLine("panel", note)
			}
		}
	}

	if p.InstallUDPRelay {
		bus.VpsSetupLine("udprelay", "Ставлю ретранслятор UDP (звонки, игры, QUIC через туннель)")
		if err := installUDPRelay(root, func(line string) { bus.VpsSetupLine("udprelay", line) }); err != nil {
			bus.VpsSetupLine("udprelay", "Ретранслятор UDP не установлен: "+err.Error())
		} else {
			res.UDPRelay = true
			bus.VpsSetupLine("udprelay", "Готово — у этого сервера включено «Пробрасывать UDP через сервер»")
		}
	}

	if p.InstallMesh {
		bus.VpsSetupLine("meshd", "Ставлю сервис сети устройств (meshd)")
		if err := installMeshd(root, func(line string) { bus.VpsSetupLine("meshd", line) }); err != nil {
			bus.VpsSetupLine("meshd", "Сеть устройств не установлена: "+err.Error())
		} else {
			bus.VpsSetupLine("meshd", "Готово — включи «Сеть устройств» в настройках сервера на каждом устройстве")
		}
	}

	if p.AddProfile != nil {
		p.AddProfile(res)
		if res.ProfileNote != "" {
			bus.VpsSetupLine("profile", res.ProfileNote)
		} else if res.ProfileName != "" {
			bus.VpsSetupLine("profile", "Сервер добавлен в список: «"+res.ProfileName+"» (пользователь tunnel)")
		}
	}
	return nil
}

// rootKeyScript дописывает открытый ключ root'у, если его там ещё нет.
func rootKeyScript(pubLine string) string {
	q := shQuote(pubLine)
	return "set -e\n" +
		"install -d -m 700 /root/.ssh\n" +
		"touch /root/.ssh/authorized_keys\n" +
		"chmod 600 /root/.ssh/authorized_keys\n" +
		"grep -qxF " + q + " /root/.ssh/authorized_keys || printf '%s\\n' " + q + " >> /root/.ssh/authorized_keys\n" +
		"echo 'ключ для root записан'\n"
}

// rootRunner выполняет команды на сервере с правами root: напрямую, если
// вошли под root, иначе через sudo. Пароль sudo живёт только здесь, в памяти,
// на время настройки — как и сам пароль входа.
type rootRunner struct {
	client   *ssh.Client
	sudo     bool   // false — вошли под root
	sudoPass string // пусто — sudo без пароля (NOPASSWD)
}

// sudoNoRights — признаки отказа sudo «нет прав» (а не «не тот пароль»).
var sudoNoRights = []string{"not in the sudoers", "is not allowed to run sudo", "may not run sudo",
	"нет в файле sudoers"}

// detectSudo проверяет, что user может стать root: сначала без пароля
// (NOPASSWD), потом с паролем входа. Ничего на сервере не меняет.
func (r *rootRunner) detectSudo(user, password string) error {
	out, code, err := runWithStdin(r.client, "sudo -n true", "")
	if err != nil {
		return err
	}
	if code == 0 {
		r.sudo = true
		return nil
	}
	if code == 127 {
		return errors.New("на сервере нет sudo — войди под root или поставь sudo и дай пользователю права")
	}
	out, code, err = runWithStdin(r.client, "sudo -S -k -p '' true", password+"\n")
	if err != nil {
		return err
	}
	if code == 0 {
		r.sudo, r.sudoPass = true, password
		return nil
	}
	low := strings.ToLower(out)
	for _, s := range sudoNoRights {
		if strings.Contains(low, s) {
			return fmt.Errorf("у пользователя %s нет права sudo — войди под root или дай ему права "+
				"(под root: usermod -aG sudo %s)", user, user)
		}
	}
	return fmt.Errorf("sudo не принял пароль пользователя %s — нужен root или пользователь с правом sudo "+
		"(ответ sudo: %s)", user, strings.TrimSpace(out))
}

// wrap — команда с повышением прав и то, что отдать ей на стандартный вход
// перед остальными данными.
func (r *rootRunner) wrap(cmd string) (string, string) {
	switch {
	case !r.sudo:
		return cmd, ""
	case r.sudoPass == "":
		return "sudo -n " + cmd, ""
	default:
		// -k: всегда спрашивать пароль, даже если sudo его недавно запомнил, —
		// иначе первая строка стандартного входа (пароль) ушла бы в bash как
		// команда. sudo читает пароль по байту до перевода строки, остальное
		// достаётся bash.
		return "sudo -S -k -p '' " + cmd, r.sudoPass + "\n"
	}
}

// script запускает script как `bash -s` с правами root, построчно отдавая
// вывод в onLine.
func (r *rootRunner) script(script string, onLine func(string)) error {
	cmd, pre := r.wrap("bash -s")
	return runRemoteScriptCmd(r.client, cmd, pre+script, onLine)
}

// output выполняет одну команду оболочки с правами root и отдаёт её вывод.
func (r *rootRunner) output(command string) (string, error) {
	cmd, pre := command, ""
	if r.sudo {
		cmd, pre = r.wrap("bash -c " + shQuote(command))
	}
	out, code, err := runWithStdin(r.client, cmd, pre)
	if err != nil {
		return out, err
	}
	if code != 0 {
		return out, fmt.Errorf("код выхода %d", code)
	}
	return out, nil
}

// runWithStdin выполняет cmd, отдаёт ему stdin и возвращает общий вывод и код
// выхода. err — только если сессия не сложилась вовсе.
func runWithStdin(client *ssh.Client, cmd, stdin string) (string, int, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", 0, err
	}
	defer session.Close()
	if stdin != "" {
		session.Stdin = strings.NewReader(stdin)
	}
	out, err := session.CombinedOutput(cmd)
	if err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitStatus(), nil
		}
		return string(out), 0, err
	}
	return string(out), 0, nil
}

// installMeshd ставит сервис сети устройств (см. sshtunnel/cmd/meshd и
// meshsvc) — так же, как ретранслятор UDP: готовый файл со страницы релиза, а
// если его нет — сборка прямо на сервере из вшитого исходника. Слушает только
// 127.0.0.1: до него дотягиваются лишь через туннель.
func installMeshd(root *rootRunner, onLine func(string)) error {
	return root.script(meshsvc.InstallScript(updater.Version), onLine)
}

// vpsDial — то же самое, что ssh.Dial, но с ограничением по времени на само
// рукопожатие: сервер, который принял TCP-соединение и замолчал, не должен
// подвесить мастер навсегда (тот же приём, что tunnel.dialSSH).
func vpsDial(user, addr string, auth ssh.AuthMethod, bus *events.Bus) (*ssh.Client, error) {
	cb := hostkey.Callback(config.KnownHostsPath(), func(host, fp string) {
		bus.VpsSetupLine("connect", fmt.Sprintf("Ключ сервера %s запомнен: %s", host, fp))
	})
	cfg := &ssh.ClientConfig{
		User:              user,
		Auth:              []ssh.AuthMethod{auth},
		HostKeyCallback:   cb,
		HostKeyAlgorithms: hostkey.Algorithms(config.KnownHostsPath(), addr),
		Timeout:           vpsDialTimeout,
	}
	conn, err := net.DialTimeout("tcp", addr, vpsDialTimeout)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(vpsDialTimeout)); err != nil {
		conn.Close()
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		c.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

func loadSigner(keyPath string) (ssh.Signer, error) {
	path, err := expandHome(keyPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(data)
}

// installPubKey кладёт открытый ключ в authorized_keys, если его там ещё нет
// — повторный запуск мастера не плодит дубликаты.
func installPubKey(client *ssh.Client, pubLine string) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	cmd := fmt.Sprintf(
		`set -e; mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys `+
			`&& chmod 600 ~/.ssh/authorized_keys `+
			`&& grep -qxF %s ~/.ssh/authorized_keys || printf '%%s\n' %s >> ~/.ssh/authorized_keys`,
		shQuote(pubLine), shQuote(pubLine))
	out, err := session.CombinedOutput(cmd)
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// runRemoteScriptCmd запускает cmd (обычно `bash -s`, возможно через sudo) и
// отдаёт ему stdin целиком, а стандартный вывод и вывод ошибок — построчно в
// onLine по мере появления, а не одной простынёй после завершения.
func runRemoteScriptCmd(client *ssh.Client, cmd, stdinData string, onLine func(string)) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	lw := &lineWriter{emit: onLine}
	session.Stdout = lw
	session.Stderr = lw

	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}
	if err := session.Start(cmd); err != nil {
		return err
	}
	if _, err := io.WriteString(stdin, stdinData); err != nil {
		stdin.Close()
		return err
	}
	stdin.Close()
	err = session.Wait()
	lw.Flush()
	return err
}

// lineWriter режет поток байт на строки и отдаёт их по одной. stdout и stderr
// сессии пишут в один и тот же lineWriter из разных горутин — отсюда мьютекс.
type lineWriter struct {
	mu   sync.Mutex
	buf  []byte
	emit func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(bytes.TrimRight(w.buf[:i], "\r"))
		w.buf = w.buf[i+1:]
		if line != "" {
			w.emit(line)
		}
	}
	return len(p), nil
}

func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = nil
	}
}

// installUDPRelay ставит ретранслятор UDP (см. sshtunnel/cmd/udprelay,
// sshtunnel/internal/udprelay): SSH умеет пробрасывать только TCP, поэтому
// звонки, игры и QUIC без него не проходят через туннель. Ретранслятор
// компилируется прямо на сервере из исходника, который мастер сюда
// закладывает, — так не нужно ни кросс-компилировать бинарник на компьютере
// человека, ни подбирать архитектуру сервера. Слушает только 127.0.0.1:
// отдельно открывать порт в файрволе не нужно, достучаться до него можно
// только через уже прошедший SSH-аутентификацию туннель.
func installUDPRelay(root *rootRunner, onLine func(string)) error {
	script, err := udpRelayInstallScript()
	if err != nil {
		return err
	}
	return root.script(script, onLine)
}

func udpRelayInstallScript() (string, error) {
	src, err := udpRelayServerSource()
	if err != nil {
		return "", fmt.Errorf("не нашёл исходник ретранслятора: %w", err)
	}

	const marker = "EOF_UDPRELAY_SOURCE_39fa2c"
	script := fmt.Sprintf(`set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# Сначала пробуем готовый файл со страницы релиза: он собран под ту же пару
# систем, что и остальные файлы проекта, и не тянет на сервер ничего лишнего.
# Компиляция на месте осталась запасным путём — на случай архитектуры, для
# которой сборки нет, или сервера без доступа к GitHub.
BIN=""
case "$(uname -m)" in
  x86_64)        BIN=udprelay ;;
  aarch64|arm64) BIN=udprelay_arm64 ;;
esac
GOT=0
if [ -n "$BIN" ] && command -v curl >/dev/null; then
  if curl -fsSL -o /usr/local/bin/udprelay \
      "https://github.com/VITAZGIO/ssh_tunnel/releases/latest/download/$BIN"; then
    chmod +x /usr/local/bin/udprelay
    GOT=1
    echo "ретранслятор скачан"
  fi
fi

if [ "$GOT" != 1 ]; then
  echo "готового файла нет — собираю на сервере"
  command -v go >/dev/null || { echo "ставлю Go"; apt-get -y -qq install golang-go >/dev/null; }
  mkdir -p /root/udprelay
  cat > /root/udprelay/main.go <<'%s'
%s
%s
  cd /root/udprelay
  go build -o /usr/local/bin/udprelay main.go
  echo "ретранслятор собран"
fi

cat > /etc/systemd/system/udprelay.service <<'EOF_UNIT'
[Unit]
Description=ssh_tunnel - ретранслятор UDP (только 127.0.0.1)
After=network.target

[Service]
ExecStart=/usr/local/bin/udprelay
Restart=always
RestartSec=2
DynamicUser=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes

[Install]
WantedBy=multi-user.target
EOF_UNIT
systemctl daemon-reload
systemctl enable --now udprelay.service
echo "udprelay запущен"
`+meshsvc.FirewallExemption, marker, src, marker)
	return script, nil
}

// installPanel ставит НАШУ веб-панель (sshtunnel/cmd/ssh_tunnel_panel):
// заводит клиентов, показывает трафик, умеет замораживать и удалять
// устройства. Готовый бинарь скачивается со страницы релиза — собирать его
// на сервере (и тащить туда компилятор) незачем, сборка под linux/amd64 и
// linux/arm64 уже лежит в релизе (см. src/build.sh).
//
// По умолчанию (publicPort == 0) панель слушает только 127.0.0.1: открывают
// её из программы временным пробросом по SSH («Открыть панель сервера») или
// через nginx/Caddy со своим сертификатом (docs/PANEL_SETUP.md). Порт в
// файрволе мастер открывает только по явному выбору «открыта в интернет» —
// иначе получилась бы работающая от root панель, доступная из интернета по
// голому HTTP, ровно после того как harden.sh закрыл вход по паролю.
//
// login/password — одноразовые данные первого входа из журнала панели;
// пусто вместе с note — панель стояла и раньше, пароль выдан тогда.
func installPanel(root *rootRunner, host string, sshPort, publicPort int, onLine func(string)) (login, password, note string, err error) {
	script := panelInstallScript(host, sshPort, publicPort)
	if err := root.script(script, onLine); err != nil {
		return "", "", "", fmt.Errorf("установка панели: %w", err)
	}

	// Одноразовый пароль панель придумывает сама при первом запуске и пишет
	// в журнал — мастер его только достаёт и показывает. Придумывать пароль
	// здесь и передавать внутрь было бы лишним звеном: панель всё равно
	// потребует сменить его при первом входе.
	login, password, note = firstRunPassword(root)
	return login, password, note, nil
}

// panelInstallScript — скрипт установки панели (отдельно, чтобы тест
// проверил его bash -n).
func panelInstallScript(host string, sshPort, publicPort int) string {
	firewall := ""
	if publicPort != 0 {
		firewall = fmt.Sprintf(`if command -v ufw >/dev/null; then
  ufw allow %d/tcp comment 'ssh_tunnel_panel' >/dev/null
  echo "порт %d открыт в firewall"
fi
`, publicPort, publicPort)
	}
	return fmt.Sprintf(`set -euo pipefail
command -v curl >/dev/null || { echo "нужен curl" >&2; exit 1; }
case "$(uname -m)" in
  x86_64)          BIN=ssh_tunnel_panel ;;
  aarch64|arm64)   BIN=ssh_tunnel_panel_arm64 ;;
  *) echo "нет сборки панели для $(uname -m)" >&2; exit 1 ;;
esac
echo "скачиваю панель ($BIN)"
curl -fsSL -o /usr/local/bin/ssh_tunnel_panel \
  "https://github.com/VITAZGIO/ssh_tunnel/releases/latest/download/$BIN"
chmod +x /usr/local/bin/ssh_tunnel_panel
install -d -m 700 /etc/ssh_tunnel_panel

cat > /etc/systemd/system/ssh_tunnel_panel.service <<'EOF_UNIT'
%s
EOF_UNIT
systemctl daemon-reload
systemctl enable ssh_tunnel_panel
systemctl restart ssh_tunnel_panel
%secho "панель запущена"
`, panelUnit(host, sshPort, publicPort), firewall)
}

// panelUnit — тот же юнит, что в packaging/panel/ssh_tunnel_panel.service, с
// подставленным адресом сервера: без -ssh-host панель не сможет собрать
// конфиг ни одному клиенту, потому что не знает, куда им подключаться.
func panelUnit(host string, sshPort, publicPort int) string {
	extra := ""
	if publicPort != 0 {
		extra = " -listen 0.0.0.0:" + strconv.Itoa(publicPort) +
			" -public-url " + shQuote("http://"+net.JoinHostPort(host, strconv.Itoa(publicPort))+"/")
	}
	return `[Unit]
Description=ssh_tunnel_panel — веб-панель управления сервером и клиентами
Documentation=https://github.com/VITAZGIO/ssh_tunnel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/ssh_tunnel_panel -ssh-host ` + shQuote(host) +
		` -ssh-port ` + strconv.Itoa(sshPort) + extra + `
Restart=always
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=15

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=true
ProtectHome=false
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true

[Install]
WantedBy=multi-user.target
`
}

// panelLoginRe/panelPassRe — строки, которые панель печатает при первом
// запуске (createFirstUser): «Логин:  admin», «Пароль: …».
var (
	panelLoginRe = regexp.MustCompile(`Логин:\s+(\S+)`)
	panelPassRe  = regexp.MustCompile(`Пароль:\s+(\S+)`)
)

// firstRunPassword достаёт из журнала одноразовые логин и пароль, которые
// панель печатает при первом запуске. Служба только что стартовала, поэтому
// пару секунд ждём: иначе журнал успевает оказаться пустым. Пароля нет —
// note объясняет почему и что делать.
func firstRunPassword(root *rootRunner) (login, password, note string) {
	out, err := root.output("sleep 2; journalctl -u ssh_tunnel_panel --no-pager -o cat -n 200 | " +
		"grep -E 'Логин:|Пароль:|одноразов' | tail -4")
	if err != nil && strings.TrimSpace(out) == "" {
		return "", "", "пароль панели из журнала не достать — посмотри на сервере: journalctl -u ssh_tunnel_panel"
	}
	return parsePanelPassword(out)
}

func parsePanelPassword(out string) (login, password, note string) {
	if m := panelPassRe.FindAllStringSubmatch(out, -1); len(m) > 0 {
		password = m[len(m)-1][1]
	}
	if m := panelLoginRe.FindAllStringSubmatch(out, -1); len(m) > 0 {
		login = m[len(m)-1][1]
	}
	if password == "" {
		// Старый формат одной строкой: «…с одноразовым паролем: X».
		if m := regexp.MustCompile(`паролем:\s+(\S+)`).FindAllStringSubmatch(out, -1); len(m) > 0 {
			password = m[len(m)-1][1]
		}
	}
	if password == "" {
		return "", "", "одноразового пароля в журнале нет — панель стояла и раньше, пароль выдан тогда. " +
			"Потерян — смотри «Забыл пароль» в docs/PANEL_SETUP.md"
	}
	if login == "" {
		login = "admin"
	}
	return login, password, ""
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// classifySetupErr — то же, что classifySSHErr, но для входа по паролю в
// мастере: подсказка зависит от того, под кем входили.
func classifySetupErr(err error, user string) error {
	if tunnel.IsAuthError(err) {
		if user == "root" {
			return errors.New("сервер не принял пароль root — проверь пароль; у некоторых хостеров вход root " +
				"по паролю выключен, тогда впиши своего пользователя с правом sudo")
		}
		return fmt.Errorf("сервер не принял пароль пользователя %s — проверь имя и пароль", user)
	}
	return classifySSHErr(err)
}

// classifySSHErr переводит частые ошибки golang.org/x/crypto/ssh в понятный
// текст — вместо "ssh: handshake failed: ssh: unable to authenticate...".
func classifySSHErr(err error) error {
	msg := err.Error()
	switch {
	case tunnel.IsAuthError(err):
		return errors.New("неверный пароль (или пользователь)")
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "actively refused"):
		return errors.New("порт закрыт: сервер отказал в соединении — проверь адрес и SSH-порт")
	case strings.Contains(msg, "no such host"):
		return errors.New("такого адреса нет — проверь, как написан адрес сервера")
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "timed out"):
		return errors.New("сервер не ответил вовремя — проверь адрес, порт и что сервер включён")
	case strings.Contains(msg, "no route to host"):
		return errors.New("сервер недоступен по сети")
	default:
		var changed *hostkey.ErrChanged
		if errors.As(err, &changed) {
			return changed
		}
		return err
	}
}
