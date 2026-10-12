package streamer

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// SessionDesc is SDP plus ephemeral X25519 for ECDH and optional static identity pub.
type SessionDesc struct {
	SDP         string
	PubKey      []byte // ephemeral, 32 bytes
	IdentityPub []byte // static identity, 32 bytes (optional; enables stable TOFU)
}

// GenerateKXKeypair returns X25519 private+public (32 bytes each).
func GenerateKXKeypair() (priv, pub []byte, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return k.Bytes(), k.PublicKey().Bytes(), nil
}

// DeriveSessionSecrets ECDH(ephPriv, peerEph) → AEAD key + XOR seed via HKDF.
func DeriveSessionSecrets(priv, peerPub []byte) (aeadKey []byte, xorSeed uint64, err error) {
	return DeriveSessionSecretsBound(priv, peerPub, nil, nil)
}

// DeriveSessionSecretsBound is ECDH on ephemeral keys; when both identity pubs are
// present they are mixed into HKDF info so the session is bound to TOFU identities.
func DeriveSessionSecretsBound(ephPriv, peerEph, localID, peerID []byte) (aeadKey []byte, xorSeed uint64, err error) {
	if len(ephPriv) != 32 || len(peerEph) != 32 {
		return nil, 0, fmt.Errorf("kx: want 32-byte ephemeral keys")
	}
	curve := ecdh.X25519()
	privK, err := curve.NewPrivateKey(ephPriv)
	if err != nil {
		return nil, 0, err
	}
	pubK, err := curve.NewPublicKey(peerEph)
	if err != nil {
		return nil, 0, err
	}
	shared, err := privK.ECDH(pubK)
	if err != nil {
		return nil, 0, err
	}
	info := []byte("session-v1")
	if len(localID) == 32 && len(peerID) == 32 {
		fp := TranscriptHash(localID, peerID)
		info = append([]byte("session-v1|id="), fp...)
	}
	r := hkdf.New(sha256.New, shared, []byte("screen-stream-v1"), info)
	aeadKey = make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(r, aeadKey); err != nil {
		return nil, 0, err
	}
	var seedBuf [8]byte
	if _, err := io.ReadFull(r, seedBuf[:]); err != nil {
		return nil, 0, err
	}
	xorSeed = binary.LittleEndian.Uint64(seedBuf[:])
	if xorSeed == 0 {
		xorSeed = 1 // never use 0 (ResolveXORSeed sentinel)
	}
	return aeadKey, xorSeed, nil
}

// TranscriptHash is an order-independent SHA-256 fingerprint of the two X25519 pubs.
// Display form: first 16 hex chars (64-bit prefix) for TOFU compare on both peers.
func TranscriptHash(localPub, remotePub []byte) string {
	if len(localPub) != 32 || len(remotePub) != 32 {
		return ""
	}
	a, b := localPub, remotePub
	if bytes.Compare(a, b) > 0 {
		a, b = b, a
	}
	h := sha256.New()
	_, _ = h.Write(a)
	_, _ = h.Write(b)
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8]) // 16 hex chars
}
