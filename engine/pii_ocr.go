package engine

import (
	"context"
	"sync"
	"sync/atomic"
)

// OCRBackend is a pluggable on-device text detector (Vision / Tesseract / stub).
type OCRBackend interface {
	ScanFrame(ctx context.Context, meta FrameMeta, bgra []byte) ([]OCRLine, error)
}

// OCRLine is one recognized text span.
type OCRLine struct {
	Text string
	Rect Rect
}

// OCRDetector: frame 1 sync (same-frame glyphs); later frames async. PixelScan always on.
type OCRDetector struct {
	Patterns  *PatternDetector
	Backend   OCRBackend
	Every     uint64
	SyncFirst bool

	frameN atomic.Uint64
	busy   atomic.Bool

	mu      sync.Mutex
	scratch []byte
	hits    []Rect
}

func NewOCRDetector(backend OCRBackend) *OCRDetector {
	pd := NewPatternDetector()
	pd.PixelScan = true
	return &OCRDetector{
		Patterns:  pd,
		Backend:   backend,
		Every:     3,
		SyncFirst: true,
		hits:      make([]Rect, 0, 32),
	}
}

func (d *OCRDetector) Detect(ctx context.Context, meta FrameMeta, plaintextBGRA []byte) ([]Rect, error) {
	if d.Backend != nil {
		every := d.Every
		if every == 0 {
			every = 3
		}
		n := d.frameN.Add(1)
		if n == 1 && d.SyncFirst {
			d.scanApply(ctx, meta, plaintextBGRA)
		} else if (n%every == 0) && d.busy.CompareAndSwap(false, true) {
			d.kickAsync(ctx, meta, plaintextBGRA)
		}
	}
	return d.Patterns.Detect(ctx, meta, plaintextBGRA)
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
		defer func() {
			clear(snap)
			d.busy.Store(false)
		}()
		select {
		case <-ctx.Done():
			return
		default:
		}
		d.scanApply(ctx, metaCopy, snap)
	}()
}

func (d *OCRDetector) scanApply(ctx context.Context, meta FrameMeta, bgra []byte) {
	lines, err := d.Backend.ScanFrame(ctx, meta, bgra)
	if err != nil {
		return
	}
	d.mu.Lock()
	hits := d.hits[:0]
	for _, ln := range lines {
		hits = append(hits, d.Patterns.ScanText(ln.Text, ln.Rect, meta.BlockSize)...)
	}
	d.hits = hits
	d.Patterns.SetHits(hits) // copies into LayoutHits
	d.mu.Unlock()
}

// StubOCRBackend returns no lines.
type StubOCRBackend struct{}

func (StubOCRBackend) ScanFrame(ctx context.Context, meta FrameMeta, bgra []byte) ([]OCRLine, error) {
	_ = ctx
	_ = meta
	_ = bgra
	return nil, nil
}

var _ PIIDetector = (*OCRDetector)(nil)
var _ OCRBackend = StubOCRBackend{}
