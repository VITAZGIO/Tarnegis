package hostkey

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPinДописываетОдинРаз(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(path, []byte("old.example ssh-ed25519 AAAAold"), 0o600)
	Pin(path, "london.example", 22, "ssh-ed25519 AAAAnew comment")
	Pin(path, "london.example", 22, "ssh-ed25519 AAAAother")
	Pin(path, "amst.example", 2222, "ssh-ed25519 AAAAamst")
	Pin(path, "old.example", 22, "ssh-ed25519 AAAAchanged")
	Pin(path, "bad.example", 22, "мусор")
	got, _ := os.ReadFile(path)
	want := "old.example ssh-ed25519 AAAAold\nlondon.example ssh-ed25519 AAAAnew\n[amst.example]:2222 ssh-ed25519 AAAAamst\n"
	if string(got) != want {
		t.Fatalf("known_hosts:\n%s\nждал:\n%s", got, want)
	}
}
