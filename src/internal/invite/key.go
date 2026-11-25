package invite

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// NewKey создаёт пару ключей устройства: закрытый — в path (0600, формат
// OpenSSH), открытый — рядом, path+".pub". Закрытый ключ никуда не
// отправляется: сервер получает только открытую часть при входе.
func NewKey(path string) (ssh.Signer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, err
	}
	_ = os.WriteFile(path+".pub", []byte(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))+" ssh_tunnel-device\n"), 0o644)
	return ssh.NewSignerFromKey(priv)
}

// LoadKey читает закрытый ключ устройства.
func LoadKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(data)
}
