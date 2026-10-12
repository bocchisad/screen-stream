package engine

import (
	"context"
	"sync"
	"sync/atomic"
)

// OCRBackend is a pluggable on-device text detector (Vision / Tesseract / stub).
type OCRBackend interface {
	// ScanFrame returns text lines with screen-space bounds for the given BGRA frame.
	// May be slow; OCRDetector calls it off the capture hot path.
	ScanFrame(ctx context.Context, meta FrameMeta, bgra []byte) ([]OCRLine, error)
}

// OCRLine is one recognized text span.
type OCRLine struct {
	Text string
	Rect Rect
}

// OCRDetector implements PIIDetector by scanning OCR lines through PatternDetector rules.
// ScanFrame runs asynchronously so Process stays on the hot path; Detect returns last hits.
type OCRDetector struct {
	Patterns *PatternDetector
	Backend  OCRBackend
	Every    uint64 // kick a scan every N frames (default 15)

	hits   []Rect
	frameN atomic.Uint64
	busy   atomic.Bool

	mu      sync.Mutex
	scratch []byte
}

func NewOCRDetector(backend OCRBackend) *OCRDetector {
	pd := NewPatternDetector()
	pd.PixelScan = false // OCR supplies layout hits
	return &OCRDetector{
		Patterns: pd,
		Backend:  backend,
		Every:    15,
		hits:     make([]Rect, 0, 32),
	}
}

func (d *OCRDetector) Detect(ctx context.Context, meta FrameMeta, plaintextBGRA []byte) ([]Rect, error) {
	if d.Backend == nil {
		return d.Patterns.DetectInto(nil), nil
	}
	every := d.Every
	if every == 0 {
		every = 15
	}
	n := d.frameN.Add(1)
	if (n == 1 || n%every == 0) && d.busy.CompareAndSwap(false, true) {
		d.kickAsync(ctx, meta, plaintextBGRA)
	}
	return d.Patterns.DetectInto(nil), nil
}

func (d *OCRDetector) kickAsync(ctx context.Context, meta FrameMeta, bgra []byte) {
	need := len(bgra)
	d.mu.Lock()
	if cap(d.scratch) < need {
		d.scratch = make([]byte, need)
	}
	copy(d.scratch[:need], bgra)
	snap := d.scratch[:need]
	metaCopy := meta
	d.mu.Unlock()

	go func() {
		defer d.busy.Store(false)
		select {
		case <-ctx.Done():
			return
		default:
		}
		lines, err := d.Backend.ScanFrame(ctx, metaCopy, snap)
		if err != nil {
			return
		}
		hits := d.hits[:0]
		for _, ln := range lines {
			hits = append(hits, d.Patterns.ScanText(ln.Text, ln.Rect, metaCopy.BlockSize)...)
		}
		d.hits = hits
		d.Patterns.SetHits(hits)
	}()
}

// StubOCRBackend returns no lines (keeps interface wired for integration tests).
type StubOCRBackend struct{}

func (StubOCRBackend) ScanFrame(ctx context.Context, meta FrameMeta, bgra []byte) ([]OCRLine, error) {
	_ = ctx
	_ = meta
	_ = bgra
	return nil, nil
}

var _ PIIDetector = (*OCRDetector)(nil)
var _ OCRBackend = StubOCRBackend{}
