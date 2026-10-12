package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakeOCR struct {
	calls atomic.Int32
	lines []OCRLine
}

func (f *fakeOCR) ScanFrame(ctx context.Context, meta FrameMeta, bgra []byte) ([]OCRLine, error) {
	f.calls.Add(1)
	_ = ctx
	_ = meta
	_ = bgra
	return f.lines, nil
}

func TestOCRDetectorAsyncHits(t *testing.T) {
	fake := &fakeOCR{lines: []OCRLine{{
		Text: "password: hunter2",
		Rect: Rect{X: 0, Y: 0, W: 120, H: 24},
	}}}
	d := NewOCRDetector(fake)
	d.SyncFirst = false
	d.Every = 1
	bgra := make([]byte, 64*64*4)
	meta := FrameMeta{Width: 64, Height: 64, Stride: 256, BlockSize: 32}

	_, err := d.Detect(context.Background(), meta, bgra)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.calls.Load() >= 1 {
			hits := d.Patterns.DetectInto(nil)
			if len(hits) > 0 {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected async OCR hit; calls=%d", fake.calls.Load())
}

func TestOCRDetectorStubNoOp(t *testing.T) {
	d := NewOCRDetector(StubOCRBackend{})
	_, err := d.Detect(context.Background(), FrameMeta{Width: 8, Height: 8, Stride: 32}, make([]byte, 256))
	if err != nil {
		t.Fatal(err)
	}
}

func TestOCRAsyncSurvivesParentCancel(t *testing.T) {
	fake := &fakeOCR{lines: []OCRLine{{
		Text: "api_key=secretvalue123",
		Rect: Rect{X: 0, Y: 0, W: 80, H: 16},
	}}}
	d := NewOCRDetector(fake)
	d.SyncFirst = false
	d.Every = 1
	bgra := make([]byte, 16*16*4)
	meta := FrameMeta{Width: 16, Height: 16, Stride: 64, BlockSize: 16}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := d.Detect(ctx, meta, bgra)
	cancel() // parent returns; async must still finish via WithoutCancel
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.calls.Load() >= 1 && !d.busy.Load() {
			hits := d.Patterns.DetectInto(nil)
			if len(hits) == 0 {
				t.Fatal("expected async hits after parent cancel")
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("async OCR did not finish after parent cancel; calls=%d", fake.calls.Load())
}
