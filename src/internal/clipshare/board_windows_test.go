package clipshare

import (
	"os"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Тесты настоящего буфера Windows. Читают его и на миг открывают; писать в
// буфер (затрёт то, что человек скопировал) — только с
// CLIPSHARE_STRESS_WRITE=1. Длительность стресса — CLIPSHARE_STRESS (по
// умолчанию 3 с), например CLIPSHARE_STRESS=1h.

// onThread выполняет f на отдельном закреплённом потоке ОС и ждёт.
func onThread(f func()) {
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		f()
		close(done)
	}()
	<-done
}

// canOpenElsewhere — может ли ДРУГАЯ программа открыть буфер. Другой поток
// нашего же процесса не годится: Windows пускает его и при открытом буфере.
// Поэтому — дочерний процесс (этот же тест-бинарник, TestПомощникОткрыть).
func canOpenElsewhere(t *testing.T) bool {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestПомощникОткрыть$")
	cmd.Env = append(os.Environ(), "CLIPSHARE_HELPER=1")
	err := cmd.Run()
	if err == nil {
		return true
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 3 {
		return false
	}
	t.Fatalf("помощник: %v", err)
	return false
}

// TestПомощникОткрыть — не тест, а «другая программа» для canOpenElsewhere:
// код 0 — буфер открылся, 3 — нет (за полсекунды).
func TestПомощникОткрыть(t *testing.T) {
	if os.Getenv("CLIPSHARE_HELPER") != "1" {
		t.Skip("вызывается из других тестов")
	}
	runtime.LockOSThread()
	// Как настоящая программа — со своим окном: с hWnd = 0 Windows пускает
	// в буфер, открытый другим с тем же нулём.
	cls, _ := windows.UTF16PtrFromString("STATIC")
	hwnd, _, _ := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0,
		^uintptr(2), 0, 0, 0) // HWND_MESSAGE = -3
	if hwnd == 0 {
		os.Exit(4)
	}
	for i := 0; i < 20; i++ {
		if r, _, _ := pOpenClipboard.Call(hwnd); r != 0 {
			pCloseClipboard.Call()
			os.Exit(0)
		}
		time.Sleep(25 * time.Millisecond)
	}
	os.Exit(3)
}

// Как было до ТЗ 32: закрыть буфер с чужого потока нельзя — он остаётся
// открытым, и никто другой его не откроет. Это и есть «буфер перестал
// работать». Детерминированно: потоки задаём сами, а не ждём, пока
// планировщик Go перенесёт горутину.
func TestЗакрытиеСЧужогоПотокаДержитБуфер(t *testing.T) {
	opened := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() { // поток A — открыл
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(finished)
		if !open() {
			close(opened)
			return
		}
		close(opened)
		<-release
		pCloseClipboard.Call() // уборка: закрывает владелец
	}()
	<-opened
	select {
	case <-finished:
		t.Skip("буфер занят другой программой")
	default:
	}
	heldBefore := !canOpenElsewhere(t)
	t.Logf("держится до закрытия с чужого потока: %v", heldBefore)
	var closedElsewhere uintptr
	onThread(func() { closedElsewhere, _, _ = pCloseClipboard.Call() }) // поток B
	stuck := !canOpenElsewhere(t)
	close(release)
	<-finished
	if closedElsewhere != 0 {
		t.Fatal("CloseClipboard с чужого потока сработал — механизм зависания другой")
	}
	if !stuck {
		t.Fatal("после неудачного закрытия буфер всё равно открывается — механизм зависания другой")
	}
	if !canOpenElsewhere(t) {
		t.Fatal("буфер не освободился и после закрытия владельцем")
	}
}

func stressFor() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("CLIPSHARE_STRESS")); err == nil {
		return d
	}
	return 3 * time.Second
}

// Много горутин читают (и с CLIPSHARE_STRESS_WRITE=1 пишут) буфер, планировщик
// и сборщик мусора их всё время двигают по потокам. После — другой поток
// обязан открыть буфер.
func TestБуферСвободенПослеНагрузки(t *testing.T) {
	write := os.Getenv("CLIPSHARE_STRESS_WRITE") == "1"
	b := winBoard{}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				if write && i == 0 && n%5 == 0 {
					_ = b.Write(Clip{Kind: KindText, Data: []byte("ssh_tunnel stress " + time.Now().Format(time.RFC3339Nano))})
				} else {
					b.Read()
				}
				runtime.Gosched()
			}
		}(i)
	}
	// Мусор и сборки — чтобы горутины переезжали между потоками.
	wg.Add(1)
	go func() {
		defer wg.Done()
		var keep [][]byte
		for {
			select {
			case <-stop:
				return
			default:
			}
			keep = append(keep, make([]byte, 1<<20))
			if len(keep) > 64 {
				keep = nil
				runtime.GC()
			}
		}
	}()
	end := time.Now().Add(stressFor())
	for time.Now().Before(end) {
		time.Sleep(time.Second)
		if !canOpenElsewhere(t) {
			close(stop)
			wg.Wait()
			t.Fatalf("буфер не открывается другим потоком во время нагрузки; держит: %s", clipboardHolder())
		}
	}
	close(stop)
	wg.Wait()
	if !canOpenElsewhere(t) {
		t.Fatalf("буфер не открывается после нагрузки; держит: %s", clipboardHolder())
	}
}

func TestСторожБуфераПишетОдинРаз(t *testing.T) {
	var got []string
	old := Logf
	Logf = func(f string, a ...any) { got = append(got, f) }
	defer func() { Logf = old }()
	b := &busyWatch{}
	b.fail()
	b.since = time.Now().Add(-busyLimit - time.Second)
	b.fail()
	b.fail()
	if len(got) != 1 {
		t.Fatalf("записей %d, ждали 1", len(got))
	}
	b.ok()
	if !b.since.IsZero() {
		t.Fatal("ok не сбросил отсчёт")
	}
}
