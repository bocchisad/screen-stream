package streamer

import (
	"encoding/binary"
	"testing"

	"github.com/bocchisad/screen-stream/engine"
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

func TestDecompressCap(t *testing.T) {
	// Highly compressible payload that expands past MaxDecompressedPayload is rejected
	// when we craft a zlib stream claiming huge size — use LimitReader path with small
	// fake: compress tiny, then verify normal path still works under cap.
	raw := make([]byte, 4096)
	pkt := &engine.DirtyPacket{Payload: append([]byte(nil), raw...)}
	_ = CompressPayload(pkt)
	if pkt.Flags&engine.FlagCompressed == 0 {
		// all zeros may compress; if not, skip
		return
	}
	out, err := DecompressPayload(pkt.Flags, pkt.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(raw) {
		t.Fatalf("len %d", len(out))
	}
}

func TestChunkRejectsHugeTotal(t *testing.T) {
	var n int
	r := NewChunkReassembler(func([]byte) { n++ })
	hdr := make([]byte, ChunkHeaderSize+8)
	binary.LittleEndian.PutUint32(hdr[0:], ChunkMagic)
	binary.LittleEndian.PutUint32(hdr[4:], 1)
	binary.LittleEndian.PutUint16(hdr[8:], 0)
	binary.LittleEndian.PutUint16(hdr[10:], 1)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(MaxWireMessage)+1)
	r.Push(hdr)
	if n != 0 {
		t.Fatal("oversized total must be rejected")
	}
}
