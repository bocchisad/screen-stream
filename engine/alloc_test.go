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

func TestPipelineZeroAllocWithPixelScan(t *testing.T) {
	const w, h, block = 640, 360, 64
	pd := NewPatternDetector()
	p := NewPipeline(PipelineConfig{Width: w, Height: h, BlockSize: block, Detector: pd})
	defer p.Close()
	buf := make([]byte, w*h*4)
	// Non-gray noise → PixelScan finds no runs (still walks frame).
	for i := range buf {
		buf[i] = byte(i*3 + 7)
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		fr := &Frame{Meta: FrameMeta{FrameID: uint64(i + 1), Width: w, Height: h, Stride: w * 4, BlockSize: block}, Pixels: buf}
		pkt, err := p.Process(ctx, fr)
		if err != nil {
			t.Fatal(err)
		}
		pkt.Release()
	}
	allocs := testing.AllocsPerRun(30, func() {
		fr := &Frame{Meta: FrameMeta{FrameID: 50, Width: w, Height: h, Stride: w * 4, BlockSize: block}, Pixels: buf}
		pkt, err := p.Process(ctx, fr)
		if err != nil {
			t.Fatal(err)
		}
		pkt.Release()
	})
	if allocs > 0 {
		t.Fatalf("Process+PixelScan allocs/op = %v want 0", allocs)
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

func TestCaptureProcessFHDLatency(t *testing.T) {
	const w, h, fps, block = 1920, 1080, 60, 64
	g := NewSyntheticGrabber(w, h, fps)
	g.NoPace = true // measure work, not FPS limiter
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.Open(ctx, CaptureConfig{FPS: fps, BlockSize: block}); err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	p := NewPipeline(PipelineConfig{Width: w, Height: h, BlockSize: block})
	defer p.Close()
	buf := make([]byte, w*h*4)

	for i := 0; i < 5; i++ {
		meta, err := g.Capture(ctx, buf)
		if err != nil {
			t.Fatal(err)
		}
		fr := &Frame{Meta: meta, Pixels: buf}
		pkt, err := p.Process(ctx, fr)
		if err != nil {
			t.Fatal(err)
		}
		pkt.Release()
	}

	const n = 40
	samples := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		t0 := time.Now()
		meta, err := g.Capture(ctx, buf)
		if err != nil {
			t.Fatal(err)
		}
		fr := &Frame{Meta: meta, Pixels: buf}
		pkt, err := p.Process(ctx, fr)
		dt := time.Since(t0)
		if err != nil {
			t.Fatal(err)
		}
		pkt.Release()
		samples = append(samples, dt)
	}
	var p99 time.Duration
	for _, s := range samples {
		if s > p99 {
			p99 = s
		}
	}
	// Synthetic fill is O(W×H); gate Process-dominated path loosely (TZ p99 is Process-focused).
	if p99 > 50*time.Millisecond {
		t.Fatalf("FHD Capture+Process p99-ish %v exceeds 50ms", p99)
	}
}

func TestOCRScratchWiped(t *testing.T) {
	fake := &fakeOCR{lines: []OCRLine{{
		Text: "ghp_ABCDEFGHIJKLMNOPQRSTUV",
		Rect: Rect{X: 0, Y: 0, W: 100, H: 20},
	}}}
	d := NewOCRDetector(fake)
	d.SyncFirst = false
	d.Every = 1
	bgra := make([]byte, 32*32*4)
	for i := range bgra {
		bgra[i] = 0x41
	}
	meta := FrameMeta{Width: 32, Height: 32, Stride: 128, BlockSize: 16}
	_, _ = d.Detect(context.Background(), meta, bgra)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !d.busy.Load() && fake.calls.Load() >= 1 {
			d.mu.Lock()
			for _, b := range d.scratch {
				if b != 0 {
					d.mu.Unlock()
					t.Fatal("OCR scratch not wiped")
				}
			}
			d.mu.Unlock()
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("async OCR did not finish")
}

func TestOCRSyncFirstSameFrame(t *testing.T) {
	fake := &fakeOCR{lines: []OCRLine{{
		Text: "password: hunter2",
		Rect: Rect{X: 0, Y: 0, W: 120, H: 24},
	}}}
	d := NewOCRDetector(fake)
	d.SyncFirst = true
	bgra := make([]byte, 64*64*4)
	meta := FrameMeta{Width: 64, Height: 64, Stride: 256, BlockSize: 32}
	hits, err := d.Detect(context.Background(), meta, bgra)
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls.Load() < 1 {
		t.Fatal("expected sync OCR on frame 1")
	}
	if len(hits) == 0 {
		t.Fatal("expected same-frame hits from SyncFirst OCR")
	}
}
