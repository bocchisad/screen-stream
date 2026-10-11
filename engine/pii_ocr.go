package engine

import "context"

// OCRDetector is a pluggable on-device text detector adapter.
// Wire a real OCR engine (e.g. Apple Vision / Tesseract) behind ScanFrame;
// default is a no-op until SetBackend is called.
type OCRBackend interface {
	// ScanFrame returns text lines with screen-space bounds for the given BGRA frame.
	// May run off the hot path; results are consumed via PatternDetector.SetHits.
	ScanFrame(ctx context.Context, meta FrameMeta, bgra []byte) ([]OCRLine, error)
}

// OCRLine is one recognized text span.
type OCRLine struct {
	Text string
	Rect Rect
}

// OCRDetector implements PIIDetector by scanning OCR lines through PatternDetector rules.
type OCRDetector struct {
	Patterns *PatternDetector
	Backend  OCRBackend
	hits     []Rect
}

func NewOCRDetector(backend OCRBackend) *OCRDetector {
	return &OCRDetector{
		Patterns: NewPatternDetector(),
		Backend:  backend,
		hits:     make([]Rect, 0, 32),
	}
}

func (d *OCRDetector) Detect(ctx context.Context, meta FrameMeta, protectedBGRA []byte) ([]Rect, error) {
	if d.Backend == nil {
		return d.Patterns.DetectInto(nil), nil
	}
	// Caller typically feeds plaintext scratch; protected buffer is not decoded here.
	_ = protectedBGRA
	lines, err := d.Backend.ScanFrame(ctx, meta, nil)
	if err != nil {
		return nil, err
	}
	d.hits = d.hits[:0]
	for _, ln := range lines {
		d.hits = append(d.hits, d.Patterns.ScanText(ln.Text, ln.Rect, meta.BlockSize)...)
	}
	d.Patterns.SetHits(d.hits)
	return d.Patterns.DetectInto(nil), nil
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
