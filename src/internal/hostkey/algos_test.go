package hostkey

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAlgorithms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "known_hosts")
	data := "example.org ssh-ed25519 AAAA\n" +
		"[example.org]:2222 ecdsa-sha2-nistp256 BBBB\n" +
		"other.net,1.2.3.4 ssh-rsa CCCC\n" +
		"# comment\n"
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		addr string
		want []string
	}{
		{"example.org:22", []string{"ssh-ed25519"}},
		{"example.org:2222", []string{"ecdsa-sha2-nistp256"}},
		{"1.2.3.4:22", []string{"rsa-sha2-512", "rsa-sha2-256", "ssh-rsa"}},
		{"unknown.host:22", nil},
	}
	for _, c := range cases {
		if got := Algorithms(p, c.addr); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.addr, got, c.want)
		}
	}
	if got := Algorithms(filepath.Join(t.TempDir(), "none"), "example.org:22"); got != nil {
		t.Errorf("missing file: got %v", got)
	}
}
