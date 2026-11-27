package mirror

// ТЗ 57: ход первой сверки — сколько файлов и байт из скольких, по
// настоящей передаче.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type slowTr struct{}

func (slowTr) RoundTrip(r *http.Request) (*http.Response, error) {
	time.Sleep(30 * time.Millisecond)
	return http.DefaultTransport.RoundTrip(r)
}

func TestХодПервойСверки(t *testing.T) {
	e := newEnv(t)
	const n, size = 4, 300 << 10
	for i := 0; i < n; i++ {
		e.write(e.root, "f"+string(rune('a'+i))+".bin", strings.Repeat(string(rune('a'+i)), size))
	}
	if err := e.hub.Scan(e.fid); err != nil {
		t.Fatal(err)
	}
	a := e.client("A", ModeBoth)
	a.R.HC = &http.Client{Transport: slowTr{}}

	var mu sync.Mutex
	var seen []Progress
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(3 * time.Millisecond):
			}
			if p := a.Status().Progress; p != nil {
				mu.Lock()
				seen = append(seen, *p)
				mu.Unlock()
			}
		}
	}()
	if _, err := a.Sync(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	close(stop)
	<-done
	if len(seen) == 0 {
		t.Fatal("хода сверки не видно")
	}
	prevF, prevB := 0, int64(0)
	for _, p := range seen {
		if p.Files != n || p.Bytes < n*size || p.Bytes > n*size+1024 {
			t.Fatalf("всего: %+v", p)
		}
		if p.FilesDone < prevF || p.BytesDone < prevB || p.BytesDone > p.Bytes {
			t.Fatalf("ход назад или больше всего: %+v после %d/%d", p, prevF, prevB)
		}
		prevF, prevB = p.FilesDone, p.BytesDone
	}
	if prevF == 0 {
		t.Fatal("ни одного готового файла по ходу")
	}
	if a.Status().Progress != nil {
		t.Fatal("ход остался после конца прохода")
	}
	// Второй проход: передавать нечего — хода нет.
	if _, err := a.Sync(context.Background(), 0, false); err != nil || a.Status().Progress != nil {
		t.Fatalf("второй проход: %v %+v", err, a.Status().Progress)
	}
}

func TestОценкаВремени(t *testing.T) {
	r := &Replica{}
	r.prog = &Progress{Files: 2, Bytes: 1000, BytesDone: 500, Started: time.Now().Add(-10 * time.Second).UnixMilli()}
	p := r.Status().Progress
	if p.ETA < 9 || p.ETA > 12 {
		t.Fatalf("осталось %d с, ждали ~10", p.ETA)
	}
	r.prog = &Progress{Files: 2, Bytes: 1000, BytesDone: 500, Started: time.Now().UnixMilli()}
	if p := r.Status().Progress; p.ETA != 0 {
		t.Fatalf("оценка в начале: %d", p.ETA)
	}
}
