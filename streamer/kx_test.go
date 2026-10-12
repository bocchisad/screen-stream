package streamer

import (
	"bytes"
	"testing"
)

func TestDeriveSessionSecretsSymmetric(t *testing.T) {
	aPriv, aPub, err := GenerateKXKeypair()
	if err != nil {
		t.Fatal(err)
	}
	bPriv, bPub, err := GenerateKXKeypair()
	if err != nil {
		t.Fatal(err)
	}
	aKey, aSeed, err := DeriveSessionSecrets(aPriv, bPub)
	if err != nil {
		t.Fatal(err)
	}
	bKey, bSeed, err := DeriveSessionSecrets(bPriv, aPub)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(aKey, bKey) {
		t.Fatal("AEAD keys differ")
	}
	if aSeed != bSeed || aSeed == 0 {
		t.Fatalf("seeds a=%x b=%x", aSeed, bSeed)
	}
}

func TestDeriveSessionSecretsBadLen(t *testing.T) {
	if _, _, err := DeriveSessionSecrets([]byte{1}, make([]byte, 32)); err == nil {
		t.Fatal("expected error")
	}
}

func TestTranscriptHashSymmetric(t *testing.T) {
	_, aPub, err := GenerateKXKeypair()
	if err != nil {
		t.Fatal(err)
	}
	_, bPub, err := GenerateKXKeypair()
	if err != nil {
		t.Fatal(err)
	}
	h1 := TranscriptHash(aPub, bPub)
	h2 := TranscriptHash(bPub, aPub)
	if h1 != h2 || len(h1) != 16 {
		t.Fatalf("hash mismatch %q vs %q", h1, h2)
	}
	_, cPub, _ := GenerateKXKeypair()
	if TranscriptHash(aPub, cPub) == h1 {
		t.Fatal("different peer should differ")
	}
}
