package streamer

import (
	"testing"

	"github.com/salfetka/screen-stream/engine"
)

func TestCompressRoundTrip(t *testing.T) {
	raw := make([]byte, 8<<10)
	for i := range raw {
		raw[i] = byte(i % 17)
	}
	pkt := &engine.DirtyPacket{
		Payload: append([]byte(nil), raw...),
		Flags:   0,
	}
	if err := CompressPayload(pkt); err != nil {
		t.Fatal(err)
	}
	if pkt.Flags&engine.FlagCompressed == 0 {
		t.Fatal("expected compressed flag on repetitive payload")
	}
	out, err := DecompressPayload(pkt.Flags, pkt.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(raw) {
		t.Fatalf("len %d want %d", len(out), len(raw))
	}
	for i := range raw {
		if out[i] != raw[i] {
			t.Fatalf("byte %d", i)
		}
	}
}

func TestCompressSkipsTiny(t *testing.T) {
	pkt := &engine.DirtyPacket{Payload: make([]byte, 100)}
	_ = CompressPayload(pkt)
	if pkt.Flags&engine.FlagCompressed != 0 {
		t.Fatal("tiny payload should skip compress")
	}
}
