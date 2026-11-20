package clipshare

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"

	"sshtunnel/internal/devnet"
)

// Буфер Windows. Смену видно по GetClipboardSequenceNumber — без окна и без
// чтения содержимого. Служба (SYSTEM, сеанс 0) буфера пользователя не
// видит — там localBoard == nil, наблюдает окно (WatchRemote).
//
// Главное правило (ТЗ 32): буфер в Win32 открывает ПОТОК, и закрыть его
// может только тот же поток. Горутина между OpenClipboard и CloseClipboard
// может переехать на другой поток ОС (чтение файла, GlobalAlloc, сборка
// мусора) — тогда CloseClipboard молча не срабатывает, буфер остаётся
// открытым нашим процессом навсегда, и ни одна программа больше не может ни
// копировать, ни вставлять, пока процесс жив. Так и было: «буфер перестаёт
// работать, лечится перезапуском приложения». Поэтому всё от открытия до
// закрытия — только внутри withClipboard (LockOSThread), и внутри — ничего
// долгого: файлы, картинки, UTF-16 готовятся снаружи.
//
// OpenClipboard зовётся с hWnd = 0: окно программы живёт на потоке
// интерфейса, а буфер мы открываем с рабочего потока. С нулём EmptyClipboard
// делает владельцем «никого» — нам это не мешает: отложенной отрисовки
// форматов (WM_RENDERFORMAT) у нас нет, все данные кладутся сразу.

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	shell32           = windows.NewLazySystemDLL("shell32.dll")
	pOpenClipboard    = user32.NewProc("OpenClipboard")
	pCloseClipboard   = user32.NewProc("CloseClipboard")
	pEmptyClipboard   = user32.NewProc("EmptyClipboard")
	pGetClipboardData = user32.NewProc("GetClipboardData")
	pSetClipboardData = user32.NewProc("SetClipboardData")
	pIsFormat         = user32.NewProc("IsClipboardFormatAvailable")
	pSeq              = user32.NewProc("GetClipboardSequenceNumber")
	pRegisterFormat   = user32.NewProc("RegisterClipboardFormatW")
	pOpenClipWindow   = user32.NewProc("GetOpenClipboardWindow")
	pWindowThreadPID  = user32.NewProc("GetWindowThreadProcessId")
	pGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	pGlobalLock       = kernel32.NewProc("GlobalLock")
	pGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	pGlobalSize       = kernel32.NewProc("GlobalSize")
	pGlobalFree       = kernel32.NewProc("GlobalFree")
	pDragQueryFile    = shell32.NewProc("DragQueryFileW")
)

const (
	cfUnicodeText = 13
	cfDIB         = 8
	cfHDrop       = 15
	gmemMoveable  = 0x0002
)

// Logf — куда писать о неполадках буфера (не закрылся, долго занят). По
// умолчанию — в журнал программы; окно VPN-версии пишет в свой файл.
var Logf = func(format string, a ...any) { devnet.Warn(fmt.Sprintf(format, a...)) }

func localBoard() Board {
	if is, err := svc.IsWindowsService(); err != nil || is {
		return nil
	}
	return &winBoard{}
}

type winBoard struct{}

var cfPNG = func() uintptr {
	n, _ := windows.UTF16PtrFromString("PNG")
	r, _, _ := pRegisterFormat.Call(uintptr(unsafe.Pointer(n)))
	return r
}()

func (winBoard) Token() string {
	r, _, _ := pSeq.Call()
	return strconv.FormatUint(uint64(r), 10)
}

// ErrBusy — буфер не открылся: его держит другая программа.
var ErrBusy = errors.New("буфер занят")

// clipMu — в процессе буфер открывает только один поток за раз. Windows
// другие потоки того же процесса не останавливает (OpenClipboard(0) из
// второго потока удаётся, пока первый держит буфер), а одновременные
// GetClipboardData/CloseClipboard из двух потоков роняли процесс (проверено
// тестом: 2 горутины читают — процесс падает без трассы). Наблюдатель же
// читает и пишет из двух разных горутин.
var clipMu sync.Mutex

// WithClipboard — то же для других пакетов (phonelink): один замок на весь
// процесс. f выполняется с открытым буфером — только быстрые вызовы.
func WithClipboard(f func() error) error { return withClipboard(f) }

// withClipboard открывает буфер, выполняет f и закрывает — всё на одном
// потоке ОС. Только так: см. комментарий в начале файла.
func withClipboard(f func() error) error {
	clipMu.Lock()
	defer clipMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if !open() {
		busy.fail()
		return ErrBusy
	}
	busy.ok()
	err := f()
	if r, _, e := pCloseClipboard.Call(); r == 0 {
		Logf("Общий буфер: CloseClipboard не сработал: %v", e)
	}
	return err
}

// open — буфер бывает занят другой программой: несколько попыток. Поток уже
// закреплён (withClipboard), поэтому ждать здесь можно.
func open() bool {
	for i := 0; i < 10; i++ {
		if r, _, _ := pOpenClipboard.Call(0); r != 0 {
			return true
		}
		time.Sleep(30 * time.Millisecond)
	}
	return false
}

// busyWatch — сторож: буфер не открывается дольше busyLimit подряд — пишем,
// какая программа его держит (GetOpenClipboardWindow → процесс). Если
// виноват кто-то другой, это видно в журнале, а не «опять ssh_tunnel».
type busyWatch struct {
	mu     sync.Mutex
	since  time.Time // первая неудача подряд; ноль — буфер открывался
	logged time.Time
}

var busy busyWatch

const (
	busyLimit = 30 * time.Second
	busyEvery = 10 * time.Minute // не чаще одной записи за столько
)

func (b *busyWatch) ok() {
	b.mu.Lock()
	b.since = time.Time{}
	b.mu.Unlock()
}

func (b *busyWatch) fail() {
	now := time.Now()
	b.mu.Lock()
	if b.since.IsZero() {
		b.since = now
	}
	report := now.Sub(b.since) > busyLimit && now.Sub(b.logged) > busyEvery
	if report {
		b.logged = now
	}
	since := b.since
	b.mu.Unlock()
	if report {
		Logf("Общий буфер: буфер обмена занят уже %s — его держит %s", now.Sub(since).Round(time.Second), clipboardHolder())
	}
}

// clipboardHolder — кто сейчас держит буфер открытым.
func clipboardHolder() string {
	hwnd, _, _ := pOpenClipWindow.Call()
	if hwnd == 0 {
		return "неизвестная программа (без окна)"
	}
	var pid uint32
	pWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return "неизвестная программа"
	}
	name := processName(pid)
	if pid == uint32(os.Getpid()) {
		return fmt.Sprintf("сам ssh_tunnel (%s, PID %d) — это ошибка программы", name, pid)
	}
	return fmt.Sprintf("программа %s (PID %d)", name, pid)
}

func processName(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "?"
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return "?"
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}

func isFormat(f uintptr) bool {
	r, _, _ := pIsFormat.Call(f)
	return r != 0
}

// ptr — адрес памяти, которую отдала система (GlobalLock): она не Go и
// сборщиком не двигается.
func ptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// global — копия содержимого формата f (буфер открыт).
func global(f uintptr) []byte {
	h, _, _ := pGetClipboardData.Call(f)
	if h == 0 {
		return nil
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return nil
	}
	defer pGlobalUnlock.Call(h)
	n, _, _ := pGlobalSize.Call(h)
	return append([]byte{}, unsafe.Slice((*byte)(ptr(p)), n)...)
}

// rawClip — что взяли из открытого буфера: сырые байты или путь. Разбирают
// их уже после закрытия.
type rawClip struct {
	format uintptr
	data   []byte
	path   string
	files  int
}

// grab — под открытым буфером: только скопировать, ничего не читать с диска
// и не декодировать.
func grab() (rawClip, bool) {
	switch {
	case isFormat(cfHDrop):
		h, _, _ := pGetClipboardData.Call(cfHDrop)
		if h == 0 {
			return rawClip{}, false
		}
		n, _, _ := pDragQueryFile.Call(h, 0xFFFFFFFF, 0, 0)
		if n != 1 {
			return rawClip{format: cfHDrop, files: int(n)}, true
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		l, _, _ := pDragQueryFile.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		return rawClip{format: cfHDrop, path: windows.UTF16ToString(buf[:l]), files: 1}, true
	case isFormat(cfUnicodeText):
		return rawClip{format: cfUnicodeText, data: global(cfUnicodeText)}, true
	case cfPNG != 0 && isFormat(cfPNG):
		if b := global(cfPNG); len(b) > 0 {
			return rawClip{format: cfPNG, data: b}, true
		}
	case isFormat(cfDIB):
		return rawClip{format: cfDIB, data: global(cfDIB)}, true
	}
	return rawClip{}, false
}

func (winBoard) Read() (Clip, bool) {
	var raw rawClip
	var got bool
	if withClipboard(func() error { raw, got = grab(); return nil }) != nil || !got {
		return Clip{}, false
	}
	// Буфер уже закрыт: дальше — сколько угодно долго.
	switch raw.format {
	case cfHDrop:
		if raw.files != 1 {
			return Clip{}, false // несколько файлов — это «Файлы устройства»
		}
		st, err := os.Stat(raw.path)
		if err != nil || !st.Mode().IsRegular() {
			return Clip{}, false
		}
		c := Clip{Kind: KindFile, Name: filepath.Base(raw.path)}
		if st.Size() > MaxBytes {
			return c, true
		}
		if c.Data, err = os.ReadFile(raw.path); err != nil {
			return Clip{}, false
		}
		return c, true
	case cfUnicodeText:
		u := make([]uint16, len(raw.data)/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(raw.data[i*2:])
		}
		s := windows.UTF16ToString(u)
		if s == "" {
			return Clip{}, false
		}
		return Clip{Kind: KindText, Data: []byte(s)}, true
	case cfDIB:
		if p, err := dibToPNG(raw.data); err == nil {
			return Clip{Kind: KindImage, Mime: "image/png", Data: p}, true
		}
		return Clip{}, false
	default: // cfPNG
		return Clip{Kind: KindImage, Mime: "image/png", Data: raw.data}, true
	}
}

// put — положить байты b форматом f (буфер открыт и очищен). Система
// забирает память себе; не взяла — освобождаем.
func put(f uintptr, b []byte) error {
	h, _, _ := pGlobalAlloc.Call(gmemMoveable, uintptr(len(b)))
	if h == 0 {
		return errors.New("нет памяти")
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		pGlobalFree.Call(h)
		return errors.New("нет памяти")
	}
	copy(unsafe.Slice((*byte)(ptr(p)), len(b)), b)
	pGlobalUnlock.Call(h)
	if r, _, err := pSetClipboardData.Call(f, h); r == 0 {
		pGlobalFree.Call(h)
		return err
	}
	return nil
}

func utf16z(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, (len(u)+1)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[i*2:], c)
	}
	return b
}

// part — один формат, готовый к записи.
type part struct {
	f    uintptr
	data []byte
	must bool // ошибка записи этого формата — ошибка всей записи
}

// parts — всё, что положим в буфер, готовое заранее (до открытия буфера).
func parts(c Clip) ([]part, error) {
	switch c.Kind {
	case KindText:
		return []part{{cfUnicodeText, utf16z(string(c.Data)), true}}, nil
	case KindImage:
		dib, err := pngToDIB(c.Data)
		if err != nil {
			return nil, err
		}
		var ps []part
		if cfPNG != 0 {
			ps = append(ps, part{cfPNG, c.Data, false})
		}
		return append(ps, part{cfDIB, dib, true}), nil
	case KindFile:
		// Картинка-файл (фото с телефона: JPEG, PNG, GIF) кладётся сразу
		// тремя форматами в одном открытии буфера: PNG и DIB — чтобы
		// вставлялось картинкой (Paint, Word, поле в браузере), CF_HDROP —
		// чтобы вставлялось файлом (Проводник, почта). Самое «богатое» —
		// первым. Не картинка или слишком большая — только файл.
		var ps []part
		if dib, p, err := imageBitmaps(c.Data); err == nil {
			if cfPNG != 0 {
				ps = append(ps, part{cfPNG, p, false})
			}
			ps = append(ps, part{cfDIB, dib, false})
		}
		// DROPFILES: смещение списка (20), точка, «не клиентская», «UTF-16»;
		// потом пути, каждый с нулём, и ещё один ноль.
		// Read читает CF_HDROP раньше картинок и получает тот же файл, поэтому
		// отпечаток (lastApplied) совпадает и обратно в сеть он не уходит.
		hdr := make([]byte, 20)
		binary.LittleEndian.PutUint32(hdr[0:], 20)
		binary.LittleEndian.PutUint32(hdr[16:], 1)
		return append(ps, part{cfHDrop, append(append(hdr, utf16z(c.Path)...), 0, 0), true}), nil
	}
	return nil, nil
}

func (winBoard) Write(c Clip) error {
	ps, err := parts(c)
	if err != nil || len(ps) == 0 {
		return err
	}
	return withClipboard(func() error {
		pEmptyClipboard.Call()
		var err error
		for _, p := range ps {
			if e := put(p.f, p.data); e != nil && p.must {
				err = e
			}
		}
		return err
	})
}
