package updater

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// autoFake — Auto с подделанным запуском обновления и часами.
type autoFake struct {
	mu   sync.Mutex
	runs []string
	on   bool
	err  error
	path string
	now  time.Time
	done chan struct{}
}

func newAutoFake(t *testing.T, installed string, on bool) (*Auto, *autoFake) {
	t.Helper()
	f := &autoFake{on: on, now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC), done: make(chan struct{}, 16)}
	a := NewAuto(func() (bool, int64) { f.mu.Lock(); defer f.mu.Unlock(); return f.on, 1 },
		func(ctx context.Context, ver string) (string, error) {
			f.mu.Lock()
			f.runs = append(f.runs, ver)
			err, path := f.err, f.path
			f.mu.Unlock()
			f.done <- struct{}{}
			return path, err
		}, nil)
	a.installed = installed
	a.now = func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
	return a, f
}

func (f *autoFake) wait(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("обновление не запустилось")
	}
	time.Sleep(20 * time.Millisecond) // дать горутине записать итог
}

func (f *autoFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs)
}

func TestAutoОбновляетсяСАвтообновлением(t *testing.T) {
	a, f := newAutoFake(t, "v1.10.6", true)
	a.Net("1.10.7", true, false, false)
	f.wait(t)
	if f.runs[0] != "1.10.7" || a.State().Stage != "install" {
		t.Fatalf("runs %v, state %+v", f.runs, a.State())
	}
}

func TestAutoБезАвтообновленияПредлагает(t *testing.T) {
	a, f := newAutoFake(t, "1.10.6", false)
	a.Net("1.10.7", true, false, false)
	time.Sleep(50 * time.Millisecond)
	if f.count() != 0 {
		t.Fatal("обновилось при выключенном автообновлении")
	}
	if st := a.State(); st.Offer != "1.10.7" || st.NetVersion != "1.10.7" {
		t.Fatalf("предложение: %+v", st)
	}
	// Кнопка в панели — обновиться всё равно.
	a.Net("1.10.7", true, true, false)
	f.wait(t)
	if f.count() != 1 {
		t.Fatal("кнопка из панели не сработала")
	}
}

func TestAutoНеВниз(t *testing.T) {
	a, f := newAutoFake(t, "1.10.8", true)
	a.Net("1.10.7", true, true, false)
	time.Sleep(50 * time.Millisecond)
	if f.count() != 0 {
		t.Fatal("обновление вниз")
	}
	if err := a.UpdateNow("1.10.7"); err == nil {
		t.Fatal("UpdateNow вниз")
	}
	if _, _, err := PrepareVersion(context.Background(), "0.0.1"); err == nil {
		t.Fatal("PrepareVersion вниз")
	}
}

func TestAutoРазВЧасНаВерсию(t *testing.T) {
	a, f := newAutoFake(t, "1.10.6", true)
	f.err = errors.New("сломанный релиз")
	a.Net("1.10.7", true, false, false)
	f.wait(t)
	if st := a.State(); st.Stage != "error" || !strings.Contains(a.StatusText(), "сломанный") {
		t.Fatalf("ошибка не видна: %+v %q", st, a.StatusText())
	}
	for i := 0; i < 3; i++ {
		a.Net("1.10.7", true, false, false) // переподключения
	}
	time.Sleep(50 * time.Millisecond)
	if f.count() != 1 {
		t.Fatalf("повтор раньше часа: %d", f.count())
	}
	// Другая версия — можно сразу.
	a.Net("1.10.8", true, false, false)
	f.wait(t)
	f.mu.Lock()
	f.now = f.now.Add(AutoRetry + time.Minute)
	f.mu.Unlock()
	a.Net("1.10.7", true, false, false)
	f.wait(t)
	if f.count() != 3 {
		t.Fatalf("через час не повторилось: %v", f.runs)
	}
	// Человек нажал сам — без ограничения.
	if err := a.UpdateNow("1.10.7"); err != nil {
		t.Fatal(err)
	}
	f.wait(t)
}

func TestAutoСкачаноСтавитЧеловек(t *testing.T) {
	a, f := newAutoFake(t, "1.10.6", true)
	f.path = "/sdcard/ssh_tunnel.apk"
	a.Net("1.10.7", true, false, false)
	f.wait(t)
	if st := a.State(); st.Stage != "ready" || st.Path != f.path {
		t.Fatalf("%+v", st)
	}
}

func TestNetDecision(t *testing.T) {
	cases := []struct {
		inst, latest, net string
		mine              bool
		want              string
	}{
		{"1.10.6", "1.10.7", "1.10.7", false, "self"},   // устройство пропустило
		{"1.10.6", "1.10.8", "1.10.7", true, "network"}, // GitHub новее серверов, моё
		{"1.10.7", "1.10.8", "1.10.7", false, ""},       // не моё — как раньше
		{"1.10.6", "1.10.8", "1.10.7", false, "self"},   // хотя бы до версии сети
		{"1.10.7", "1.10.7", "1.10.7", true, ""},        // всё последнее
		{"1.10.7", "1.10.8", "", true, ""},              // старый meshd
	}
	for _, c := range cases {
		if got := NetDecision(c.inst, c.latest, c.net, c.mine); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

func TestReleaseByVersionТолькоОфициальный(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.10.7":
			w.Write([]byte(`{"tag_name":"v1.10.7","assets":[{"name":"SHA256SUMS.txt","browser_download_url":"x"}]}`))
		case "/v1.10.8":
			w.Write([]byte(`{"tag_name":"v1.10.8","prerelease":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := releasesTagURL
	releasesTagURL = srv.URL + "/"
	defer func() { releasesTagURL = old }()
	if rel, err := ReleaseByVersion(context.Background(), "v1.10.7"); err != nil || rel.Tag != "v1.10.7" {
		t.Fatalf("релиз: %v %v", rel, err)
	}
	if _, err := ReleaseByVersion(context.Background(), "1.10.8"); err == nil {
		t.Fatal("предрелиз принят")
	}
	if _, err := ReleaseByVersion(context.Background(), "1.10.9"); err == nil {
		t.Fatal("несуществующий принят")
	}
	if _, err := ReleaseByVersion(context.Background(), "../x"); err == nil {
		t.Fatal("кривой номер принят")
	}
}
