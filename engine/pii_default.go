package engine

// NewDefaultPIIDetector picks the best on-device redaction stack:
// Vision OCR + pattern rules (+ ASCII pixel scan) on darwin/cgo;
// PatternDetector pixel scan elsewhere.
func NewDefaultPIIDetector() PIIDetector {
	if OCRAvailable() {
		d := NewOCRDetector(NewVisionOCRBackend())
		d.Every = 3 // glyphs refresh often enough for secrets
		d.Patterns.PixelScan = true
		return d
	}
	return NewPatternDetector()
}
