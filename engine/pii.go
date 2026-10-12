package engine

import (
	"context"
	"regexp"
	"sync"
	"unicode"
)

// PatternDetector is the default on-device redaction helper.
// LayoutHits come from OCR; PixelScan finds ASCII-luminance overlays in BGRA.
type PatternDetector struct {
	mu         sync.Mutex
	patterns   []*regexp.Regexp
	LayoutHits []Rect
	// PixelScan enables grayscale ASCII-run regex scan of BGRA (default true).
	PixelScan bool

	outScratch  []Rect
	textScratch []byte
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
	return &PatternDetector{
		patterns:    compiled,
		PixelScan:   true,
		outScratch:  make([]Rect, 0, 32),
		textScratch: make([]byte, 0, 256),
	}
}

// Detect returns rects to black out (layout hits + optional pixel ASCII scan).
func (d *PatternDetector) Detect(ctx context.Context, meta FrameMeta, plaintextBGRA []byte) ([]Rect, error) {
	_ = ctx
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.outScratch[:0]
	if len(d.LayoutHits) > 0 {
		out = append(out, d.LayoutHits...)
	}
	if d.PixelScan && len(plaintextBGRA) > 0 {
		out, d.textScratch = scanBGRASecrets(plaintextBGRA, meta, d.patterns, out, d.textScratch)
	}
	d.outScratch = out
	return out, nil
}

// DetectInto appends layout hits into dst (legacy / OCR hot path).
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

// SetHits replaces layout redaction rects (called from OCR worker).
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
	for i, re := range d.patterns {
		for _, m := range re.FindAllString(text, -1) {
			if i == 1 && !luhnValid(digitsOnly(m)) {
				continue
			}
			hits = append(hits, alignRect(lineRect, blockSize))
			break
		}
	}
	return hits
}

func digitsOnly(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if unicode.IsDigit(r) {
			b = append(b, byte(r))
		}
	}
	return string(b)
}

func luhnValid(digits string) bool {
	n := len(digits)
	if n < 13 || n > 19 {
		return false
	}
	sum := 0
	alt := false
	for i := n - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

func alignRect(r Rect, block uint16) Rect {
	x0 := r.X / block * block
	y0 := r.Y / block * block
	x1 := (r.X + r.W + block - 1) / block * block
	y1 := (r.Y + r.H + block - 1) / block * block
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

var _ PIIDetector = (*PatternDetector)(nil)
