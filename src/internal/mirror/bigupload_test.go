package mirror

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Файл больше 50 кусков загружается целиком (раньше «загрузка не идёт»
// после 50 кусков — файлы больше 200 МБ не доходили никогда).
func TestBigFileManyChunks(t *testing.T) {
	old := chunk
	chunk = 1 << 10
	t.Cleanup(func() { chunk = old })
	e := newEnv(t)
	c := e.client("пк", "both")
	data := bytes.Repeat([]byte("0123456789abcdef"), 130*64) // 130 кусков по 1 КБ
	if err := os.WriteFile(filepath.Join(c.Path, "big.mp3"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.sync(c); err != nil {
		t.Fatalf("большой файл не загрузился: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(e.root, "big.mp3"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("на сервере не тот файл: %d байт из %d, %v", len(got), len(data), err)
	}
}
