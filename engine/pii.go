package engine

import (
	"context"
	"regexp"
	"sync"
)

// PatternDetector is the default on-device redaction helper.
// Full OCR plugs in via PIIDetector; this impl uses injected "layout hits"
// plus optional synthetic scan of a plaintext overlay provided by tests.
type PatternDetector struct {
	mu       sync.Mutex
	patterns []*regexp.Regexp
	// LayoutHits supplies precomputed rects (e.g. from OCR goroutine).
	LayoutHits []Rect
}

func NewPatternDetector() *PatternDetector {
	pats := []string{
		`ghp_[A-Za-z0-9]{20,}`,
		`\b(?:\d[ -]*?){13,19}\b`,
		`(?i)password\s*[:=]\s*\S+`,
		`(?i)api[_-]?key\s*[:=]\s*\S+`,
	}
	compiled := make([]*regexp.Regexp, 0, len(pats))
	for _, p := range pats {
		compiled = append(compiled, regexp.MustCompile(p))
	}
	return &PatternDetector{patterns: compiled}
}

// Detect returns rects to black out. Prefer DetectInto on the hot path.
func (d *PatternDetector) Detect(ctx context.Context, meta FrameMeta, protectedBGRA []byte) ([]Rect, error) {
	_ = ctx
	_ = protectedBGRA
	_ = meta
	out := d.DetectInto(nil)
	return out, nil
}

// DetectInto appends layout hits into dst (reused buffer) — zero alloc when cap suffices.
func (d *PatternDetector) DetectInto(dst []Rect) []Rect {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.LayoutHits) == 0 {
		if dst == nil {
			return nil
		}
		return dst[:0]
	}
	return append(dst[:0], d.LayoutHits...)
}

// SetHits replaces layout redaction rects (called from OCR worker; not on hot path alloc-sensitive).
func (d *PatternDetector) SetHits(rects []Rect) {
	d.mu.Lock()
	d.LayoutHits = append(d.LayoutHits[:0], rects...)
	d.mu.Unlock()
}

// ScanText maps matching pattern spans to block-aligned rects (helper for OCR/text layer).
func (d *PatternDetector) ScanText(text string, lineRect Rect, blockSize uint16) []Rect {
	if blockSize == 0 {
		blockSize = 64
	}
	var hits []Rect
	for _, re := range d.patterns {
		if re.MatchString(text) {
			hits = append(hits, alignRect(lineRect, blockSize))
		}
	}
	return hits
}

func alignRect(r Rect, block uint16) Rect {
	x0 := r.X / block * block
	y0 := r.Y / block * block
	x1 := (r.X + r.W + block - 1) / block * block
	y1 := (r.Y + r.H + block - 1) / block * block
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

var _ PIIDetector = (*PatternDetector)(nil)
