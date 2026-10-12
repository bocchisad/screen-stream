package streamer

import (
	"crypto/ecdh"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultConfigDir returns ~/.config/screen-stream (or $SCREEN_STREAM_CONFIG).
func DefaultConfigDir() (string, error) {
	if d := os.Getenv("SCREEN_STREAM_CONFIG"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "screen-stream"), nil
}

// DefaultIdentityPath is <config>/identity.key (32-byte X25519 private key).
func DefaultIdentityPath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "identity.key"), nil
}

// DefaultTOFUPath is <config>/tofu.json.
func DefaultTOFUPath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tofu.json"), nil
}

// LoadOrCreateIdentity loads a 32-byte X25519 private key or creates one.
func LoadOrCreateIdentity(path string) (priv, pub []byte, err error) {
	if path == "" {
		path, err = DefaultIdentityPath()
		if err != nil {
			return nil, nil, err
		}
	}
	if b, err := os.ReadFile(path); err == nil {
		if len(b) != 32 {
			return nil, nil, fmt.Errorf("identity: %s: want 32-byte private key, got %d", path, len(b))
		}
		curve := ecdh.X25519()
		privK, err := curve.NewPrivateKey(b)
		if err != nil {
			return nil, nil, fmt.Errorf("identity: parse: %w", err)
		}
		return append([]byte(nil), b...), privK.PublicKey().Bytes(), nil
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	priv, pub, err = GenerateKXKeypair()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(path, priv, 0o600); err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
}
