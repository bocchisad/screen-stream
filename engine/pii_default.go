package engine

// NewDefaultPIIDetector picks the best on-device redaction stack:
// Vision (darwin) → Tesseract (if on PATH) → PatternDetector ASCII scan.
func NewDefaultPIIDetector() PIIDetector {
	if OCRAvailable() {
		d := NewOCRDetector(NewVisionOCRBackend())
		d.Every = 3
		d.Patterns.PixelScan = true
		return d
	}
	if TesseractAvailable() {
		d := NewOCRDetector(NewTesseractOCRBackend())
		d.Every = 5 // slower than Vision
		d.Patterns.PixelScan = true
		return d
	}
	return NewPatternDetector()
}
