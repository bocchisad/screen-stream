package streamer

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/salfetka/screen-stream/engine"
)

func TestAEADRoundTrip(t *testing.T) {
	aead, err := NewWireAEAD(engine.DefaultXORSeed)
	if err != nil {
		t.Fatal(err)
	}
	plain := bytes.Repeat([]byte{0x42}, 4096)
	sealed, err := SealWire(aead, plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !IsAEADWire(sealed) {
		t.Fatal("missing SSAE magic")
	}
	out, err := OpenWire(aead, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatal("mismatch")
	}
}

func TestAEADWrongKey(t *testing.T) {
	a, _ := NewWireAEAD(1)
	b, _ := NewWireAEAD(2)
	sealed, _ := SealWire(a, []byte("hello-world-payload!!!!"), nil)
	if _, err := OpenWire(b, sealed); err == nil {
		t.Fatal("expected auth failure")
	}
}

func TestAuthToken(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://x/stream?token=secret", nil)
	if !CheckRequestToken(req, "secret") {
		t.Fatal("query token")
	}
	req2, _ := http.NewRequest("GET", "http://x/", nil)
	req2.Header.Set("Authorization", "Bearer secret")
	if !CheckRequestToken(req2, "secret") {
		t.Fatal("bearer")
	}
	if CheckRequestToken(req2, "other") {
		t.Fatal("should reject")
	}
	if !CheckRequestToken(req2, "") {
		t.Fatal("empty expected allows")
	}
}
