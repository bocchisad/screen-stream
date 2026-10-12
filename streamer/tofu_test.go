package streamer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTOFUCheckOrPin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tofu.json")
	s, err := LoadTOFUStore(path)
	if err != nil {
		t.Fatal(err)
	}
	key := TOFUPinKey("http://127.0.0.1:8787", "alice")
	if err := s.CheckOrPin(key, "aabbccddeeff0011"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s2, err := LoadTOFUStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.CheckOrPin(key, "aabbccddeeff0011"); err != nil {
		t.Fatal(err)
	}
	if err := s2.CheckOrPin(key, "deadbeefdeadbeef"); err == nil {
		t.Fatal("expected mismatch")
	}
}

func TestLoadOrCreateIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.key")
	priv, pub, err := LoadOrCreateIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(priv) != 32 || len(pub) != 32 {
		t.Fatal("bad lengths")
	}
	priv2, pub2, err := LoadOrCreateIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(priv) != string(priv2) || string(pub) != string(pub2) {
		t.Fatal("identity not stable")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("identity key too open: %o", fi.Mode().Perm())
	}
}

func TestDeriveBoundDiffersFromEphemeralOnly(t *testing.T) {
	aPriv, aPub, _ := GenerateKXKeypair()
	bPriv, bPub, _ := GenerateKXKeypair()
	_, aID, _ := GenerateKXKeypair()
	_, bID, _ := GenerateKXKeypair()
	k1, _, err := DeriveSessionSecrets(aPriv, bPub)
	if err != nil {
		t.Fatal(err)
	}
	k2, _, err := DeriveSessionSecretsBound(aPriv, bPub, aID, bID)
	if err != nil {
		t.Fatal(err)
	}
	if string(k1) == string(k2) {
		t.Fatal("identity binding should change HKDF output")
	}
	k3, _, err := DeriveSessionSecretsBound(bPriv, aPub, bID, aID)
	if err != nil {
		t.Fatal(err)
	}
	if string(k2) != string(k3) {
		t.Fatal("bound secrets not symmetric")
	}
}
