package engine

import (
	"context"
	"testing"
)

func TestPipelineDirtyAndProtect(t *testing.T) {
	const w, h, block = 128, 128, 32
	p := NewPipeline(PipelineConfig{Width: w, Height: h, BlockSize: block})
	defer p.Close()
	buf := make([]byte, w*h*4)
	for i := range buf {
		buf[i] = byte(i)
	}
	meta := FrameMeta{FrameID: 1, Width: w, Height: h, Stride: w * 4}
	fr := &Frame{Meta: meta, Pixels: buf}
	pkt, err := p.Process(context.Background(), fr)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Flags&FlagKeyframe == 0 {
		t.Fatal("first frame should be keyframe")
	}
	if !fr.Protected {
		t.Fatal("frame buffer should be protected")
	}
	if len(pkt.Regions) == 0 {
		t.Fatal("expected dirty regions")
	}
	pkt.Release()
}

func TestPatternScan(t *testing.T) {
	d := NewPatternDetector()
	hits := d.ScanText("token ghp_ABCDEFGHIJKLMNOPQRSTUV", Rect{X: 10, Y: 10, W: 100, H: 20}, 64)
	if len(hits) == 0 {
		t.Fatal("expected hit for ghp_ token")
	}
}

func TestLuhnCard(t *testing.T) {
	d := NewPatternDetector()
	// Visa test PAN (passes Luhn)
	ok := d.ScanText("card 4111111111111111", Rect{X: 0, Y: 0, W: 200, H: 20}, 64)
	if len(ok) == 0 {
		t.Fatal("expected Luhn-valid PAN hit")
	}
	bad := d.ScanText("card 4111111111111112", Rect{X: 0, Y: 0, W: 200, H: 20}, 64)
	if len(bad) != 0 {
		t.Fatal("Luhn-invalid PAN must not hit")
	}
}
