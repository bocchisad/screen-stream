package streamer

import (
	"bytes"
	"testing"
)

func TestChunkRoundTrip(t *testing.T) {
	const msgID = 42
	payload := bytes.Repeat([]byte{0xAB, 0xCD}, 80<<10) // > DefaultChunkSize
	var got []byte
	r := NewChunkReassembler(func(full []byte) {
		got = append([]byte(nil), full...)
	})
	err := SplitWire(msgID, payload, DefaultChunkSize, nil, func(chunk []byte) error {
		cp := append([]byte(nil), chunk...)
		r.Push(cp)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("reassembled len=%d want=%d mismatch", len(got), len(payload))
	}
}

func TestChunkLegacyPassthrough(t *testing.T) {
	// SSR1 magic (DirtyPacket) without SSCH header
	wire := make([]byte, 64)
	wire[0], wire[1], wire[2], wire[3] = 'S', 'S', 'R', '1'
	var got []byte
	r := NewChunkReassembler(func(full []byte) {
		got = append([]byte(nil), full...)
	})
	r.Push(wire)
	if !bytes.Equal(got, wire) {
		t.Fatal("legacy packet not forwarded")
	}
}

func TestChunkPoolReuse(t *testing.T) {
	payload := bytes.Repeat([]byte{1}, DefaultChunkSize+100)
	var n int
	r := NewChunkReassembler(func([]byte) { n++ })
	for i := 0; i < 8; i++ {
		_ = SplitWire(uint32(i+1), payload, DefaultChunkSize, nil, func(chunk []byte) error {
			r.Push(append([]byte(nil), chunk...))
			return nil
		})
	}
	if n != 8 {
		t.Fatalf("got %d full messages", n)
	}
}
