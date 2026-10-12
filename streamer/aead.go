package streamer

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	AEADMagic     uint32 = 0x53534145 // "SSAE"
	AEADHeaderSize       = 4 + chacha20poly1305.NonceSize
)

// DeriveWireKey turns the shared seed into a 32-byte ChaCha20-Poly1305 key (HKDF-SHA256).
func DeriveWireKey(seed uint64) []byte {
	var ikm [8]byte
	binary.LittleEndian.PutUint64(ikm[:], seed)
	r := hkdf.New(sha256.New, ikm[:], []byte("screen-stream-v1"), []byte("wire-aead-v1"))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(r, key); err != nil {
		panic("hkdf: " + err.Error())
	}
	return key
}

// NewWireAEAD builds an AEAD from a shared seed (legacy / tests).
func NewWireAEAD(seed uint64) (cipher.AEAD, error) {
	return chacha20poly1305.New(DeriveWireKey(seed))
}

// NewWireAEADFromKey builds AEAD from a raw 32-byte key (ECDH-derived).
func NewWireAEADFromKey(key []byte) (cipher.AEAD, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("aead key: want %d bytes", chacha20poly1305.KeySize)
	}
	return chacha20poly1305.New(key)
}

// SealWire wraps a packed DirtyPacket: magic | nonce | ciphertext+tag.
func SealWire(aead cipher.AEAD, plain []byte, dst []byte) ([]byte, error) {
	if aead == nil {
		return plain, nil
	}
	need := AEADHeaderSize + len(plain) + aead.Overhead()
	if cap(dst) < need {
		dst = make([]byte, need)
	} else {
		dst = dst[:need]
	}
	binary.LittleEndian.PutUint32(dst[0:], AEADMagic)
	nonce := dst[4 : 4+chacha20poly1305.NonceSize]
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := aead.Seal(dst[AEADHeaderSize:AEADHeaderSize], nonce, plain, nil)
	return dst[:AEADHeaderSize+len(out)], nil
}

// OpenWire decrypts an SSAE envelope; passthrough if not sealed.
func OpenWire(aead cipher.AEAD, wire []byte) ([]byte, error) {
	if len(wire) < 4 || binary.LittleEndian.Uint32(wire) != AEADMagic {
		return wire, nil // legacy plaintext DirtyPacket / SSCH already peeled
	}
	if aead == nil {
		return nil, fmt.Errorf("AEAD envelope but no key configured")
	}
	if len(wire) < AEADHeaderSize+aead.Overhead() {
		return nil, ErrTruncated
	}
	nonce := wire[4:AEADHeaderSize]
	ct := wire[AEADHeaderSize:]
	plain, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("AEAD open: %w", err)
	}
	if len(plain) > MaxWireMessage {
		return nil, ErrTruncated
	}
	return plain, nil
}

func IsAEADWire(wire []byte) bool {
	return len(wire) >= 4 && binary.LittleEndian.Uint32(wire) == AEADMagic
}
