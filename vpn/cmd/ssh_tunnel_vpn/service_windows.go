package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"

	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/updater"
	"sshtunnel/internal/webui"
	"sshtunnel/vpn/internal/winsvc"
	"sshtunnel/vpn/vpnlayer"
)

// runService — ssh_tunnel_vpn.exe -service, его запускает сама Windows при
// включении компьютера. Окна и рабочего стола у службы нет: её интерфейс
// показывает обычная копия программы (см. client_windows.go).
func runService(profile, appdata string) {
	// Служба работает от имени SYSTEM, у которого свои пустые папки.
	// Настройки, ключи и известные серверы — пользователя, который ставил
	// службу: подставляем его папки, и весь остальной код (config.Dir, поиск
	// ключа, «Загрузки» для обновлений) находит их сам.
	if profile != "" {
		os.Setenv("USERPROFILE", profile)
	}
	if appdata != "" {
		os.Setenv("APPDATA", appdata)
	}
	vpnlayer.WintunDir = filepath.Join(winsvc.InstallDir(), "wintun")

	// «Обновить» в окне: служба (SYSTEM) сама меняет свой файл в Program
	// Files и перезапускается — без окна UAC. Окно увидит новую версию
	// службы и перезапустится следом (client.watchUpgrade).
	updater.VPNInstall = func(newFile, exe string, _ func()) error {
		if err := updater.Swap(newFile, exe); err != nil {
			return err
		}
		// net stop — штатная остановка (Execute гасит туннель), net start —
		// уже новый файл. cmd переживает выход службы. Если служба ещё не
		// успела остановиться, первый net start не проходит — и раньше служба
		// так и оставалась выключенной («служба ещё не записала свой адрес»
		// в окне): пробуем запустить ещё дважды.
		n := winsvc.Name
		cmd := exec.Command("cmd", "/c", "ping -n 3 127.0.0.1 >nul & net stop "+n+
			" & net start "+n+" || (ping -n 6 127.0.0.1 >nul & net start "+n+")"+
			" || (ping -n 16 127.0.0.1 >nul & net start "+n+")")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		return cmd.Start()
	}
	if err := svc.Run(winsvc.Name, &service{}); err != nil {
		fmt.Fprintln(os.Stderr, "служба:", err)
		os.Exit(1)
	}
}

type service struct{}

func (*service) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	t0 := time.Now()

	dir := config.Dir()
	// Журнал — с самого начала и с временем: окно пишет свою сторону в
	// window.log, по двум файлам видно, кто кого ждал (ТЗ 32).
	writeServiceLog(dir, fmt.Sprintf("старт службы, PID %d, версия %s", os.Getpid(), updater.Version))
	e, err := newEngine()
	if err != nil {
		writeServiceLog(dir, "не запустился интерфейс: "+err.Error())
		return true, 1
	}
	go logToFile(dir, e.a.Bus)
	// Галочку автозапуска из службы не снять: служба удаляет себя только из
	// окна, через окно UAC (см. clientBoot).
	webui.SetBootControl(serviceBoot{})

	if err := winsvc.WriteEndpoint(dir, winsvc.Endpoint{
		Addr: e.srv.Addr(), Token: e.srv.Token(), Version: updater.Version, PID: os.Getpid(),
	}); err != nil {
		writeServiceLog(dir, "не удалось записать service.json: "+err.Error())
	} else {
		writeServiceLog(dir, fmt.Sprintf("адрес записан (%s) через %d мс после старта", e.srv.Addr(), time.Since(t0).Milliseconds()))
	}
	// Удалённо включённый компьютер должен появиться в сети, когда бы ни
	// поднялась его сеть, — поэтому ждём её без срока.
	e.autoConnect(0, 0)

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range req {
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			why := "остановка"
			if c.Cmd == svc.Shutdown {
				why = "выключение компьютера"
			}
			t := time.Now()
			writeServiceLog(dir, why+": служба останавливается")
			status <- svc.Status{State: svc.StopPending}
			winsvc.RemoveEndpoint(dir)
			e.a.StopNow()
			e.srv.Close()
			writeServiceLog(dir, fmt.Sprintf("служба остановлена за %d мс", time.Since(t).Milliseconds()))
			return false, 0
		}
	}
	return false, 0
}

// serviceBoot — галочка автозапуска глазами самой службы: раз служба
// работает, автозапуск включён.
type serviceBoot struct{}

func (serviceBoot) Enabled() bool { return true }
func (serviceBoot) Set(enable bool) error {
	if enable {
		return nil
	}
	return fmt.Errorf("автозапуск выключается в окне программы")
}

// Журнал службы — файлом в папке настроек: окна у службы нет, а понять,
// почему VPN не поднялся при включении компьютера, иногда надо.
const serviceLogMax = 1 << 20

func serviceLogPath(dir string) string { return filepath.Join(dir, "service.log") }

func writeServiceLog(dir, line string) {
	f, err := os.OpenFile(serviceLogPath(dir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), line)
}

func logToFile(dir string, bus *events.Bus) {
	// Старый журнал не копим бесконечно: перевалил за мегабайт — начинаем
	// заново, прежний остаётся рядом одной копией.
	path := serviceLogPath(dir)
	if st, err := os.Stat(path); err == nil && st.Size() > serviceLogMax {
		os.Rename(path, path+".old")
	}
	writeServiceLog(dir, "служба запущена, версия "+updater.Version)
	ch, _ := bus.Subscribe()
	for e := range ch {
		switch e.Kind {
		case events.KindLog:
			writeServiceLog(dir, e.Level+": "+e.Text)
		case events.KindState:
			writeServiceLog(dir, "состояние: "+e.State+" "+e.Detail)
		}
	}
}
