package engine

import (
	"context"
	"testing"
	"time"
)

func TestPipelineZeroAlloc(t *testing.T) {
	const w, h, block = 1280, 720, 64
	p := NewPipeline(PipelineConfig{Width: w, Height: h, BlockSize: block})
	defer p.Close()
	buf := make([]byte, w*h*4)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		meta := FrameMeta{FrameID: uint64(i + 1), Width: w, Height: h, Stride: w * 4}
		fr := &Frame{Meta: meta, Pixels: buf}
		pkt, err := p.Process(ctx, fr)
		if err != nil {
			t.Fatal(err)
		}
		pkt.Release()
		for j := range buf {
			buf[j] = byte(i + j)
		}
	}
	allocs := testing.AllocsPerRun(50, func() {
		meta := FrameMeta{FrameID: 100, Width: w, Height: h, Stride: w * 4}
		fr := &Frame{Meta: meta, Pixels: buf}
		pkt, err := p.Process(ctx, fr)
		if err != nil {
			t.Fatal(err)
		}
		pkt.Release()
	})
	if allocs > 0 {
		t.Fatalf("hot Process allocs/op = %v want 0", allocs)
	}
}

func TestPipelineFHDLatency(t *testing.T) {
	const w, h, block = 1920, 1080, 64
	p := NewPipeline(PipelineConfig{Width: w, Height: h, BlockSize: block})
	defer p.Close()
	buf := make([]byte, w*h*4)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		fr := &Frame{Meta: FrameMeta{FrameID: uint64(i + 1), Width: w, Height: h, Stride: w * 4}, Pixels: buf}
		pkt, _ := p.Process(ctx, fr)
		if pkt != nil {
			pkt.Release()
		}
	}
	const n = 30
	samples := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		for j := 0; j < 1024; j++ {
			buf[j] = byte(i + j)
		}
		fr := &Frame{Meta: FrameMeta{FrameID: uint64(10 + i), Width: w, Height: h, Stride: w * 4}, Pixels: buf}
		t0 := time.Now()
		pkt, err := p.Process(ctx, fr)
		dt := time.Since(t0)
		if err != nil {
			t.Fatal(err)
		}
		pkt.Release()
		samples = append(samples, dt)
	}
	// p99 among 30 ≈ max of top 1
	var p99 time.Duration
	for _, s := range samples {
		if s > p99 {
			p99 = s
		}
	}
	if p99 > 10*time.Millisecond {
		t.Fatalf("FHD Process p99-ish %v exceeds 10ms (synthetic)", p99)
	}
}
