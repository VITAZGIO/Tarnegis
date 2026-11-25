package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ключ пришёл с профилем без .pub рядом — открытая часть берётся из
// приватного, а не ошибка «проверь вручную».
func TestEnsureKeyWithoutPub(t *testing.T) {
	line, priv, err := generateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(p, []byte(priv), 0o600); err != nil {
		t.Fatal(err)
	}
	got, created, err := ensureKey(p)
	if err != nil || created {
		t.Fatalf("ensureKey: %q %v %v", got, created, err)
	}
	if got != line {
		t.Fatalf("pub: got %q, want %q", got, line)
	}
	if b, _ := os.ReadFile(p + ".pub"); strings.TrimSpace(string(b)) != line {
		t.Fatalf(".pub not written: %q", b)
	}
}
