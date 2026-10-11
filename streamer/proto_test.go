package streamer

import (
	"bytes"
	"context"
	"testing"

	"github.com/salfetka/screen-stream/engine"
)

func TestPackUnpackRoundTrip(t *testing.T) {
	pkt := &engine.DirtyPacket{
		Magic:     engine.PacketMagic,
		Version:   1,
		Flags:     engine.FlagKeyframe,
		FrameID:   42,
		PTSUs:     1000,
		Width:     64,
		Height:    64,
		BlockSize: 32,
		Regions: []engine.DirtyRegion{{
			Rect:          engine.Rect{X: 0, Y: 0, W: 32, H: 32},
			PayloadOffset: 0,
			PayloadLen:    32 * 32 * 4,
		}},
		Payload: bytes.Repeat([]byte{1, 2, 3, 4}, 32*32),
	}
	wire := Pack(pkt)
	u, err := Unpack(wire)
	if err != nil {
		t.Fatal(err)
	}
	if u.FrameID != 42 || u.Width != 64 || len(u.Regions) != 1 {
		t.Fatalf("bad unpack: %+v", u)
	}
	if len(u.Payload) != len(pkt.Payload) {
		t.Fatalf("payload len %d != %d", len(u.Payload), len(pkt.Payload))
	}
}

func TestReconstructDecrypt(t *testing.T) {
	const w, h, block = 64, 64, 32
	pipe := engine.NewPipeline(engine.PipelineConfig{
		Width: w, Height: h, BlockSize: block,
		XORSeed: engine.DefaultXORSeed,
	})
	defer pipe.Close()

	buf := make([]byte, w*h*4)
	for i := range buf {
		buf[i] = byte(i * 3)
	}
	want := make([]byte, len(buf))
	copy(want, buf)

	fr := &engine.Frame{Meta: engine.FrameMeta{FrameID: 1, Width: w, Height: h, Stride: w * 4}, Pixels: buf}
	pkt, err := pipe.Process(context.Background(), fr)
	if err != nil {
		t.Fatal(err)
	}
	wire := Pack(pkt)
	pkt.Release()

	rec := NewFrameReconstructor(engine.DefaultXORSeed)
	if err := rec.Apply(wire); err != nil {
		t.Fatal(err)
	}
	got := rec.Canvas()
	if !bytes.Equal(got, want) {
		// First keyframe should restore full frame (all blocks dirty).
		diff := 0
		for i := range want {
			if got[i] != want[i] {
				diff++
			}
		}
		t.Fatalf("canvas mismatch, differing bytes=%d/%d", diff, len(want))
	}
}
