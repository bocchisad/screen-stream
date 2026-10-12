package engine

import "regexp"

// scanBGRASecrets finds grayscale (B≈G≈R) printable ASCII runs and matches
// PatternDetector regexes. Real glyph OCR is OCRDetector; this catches
// literal ASCII overlays where luminance ≈ codepoint.
func scanBGRASecrets(bgra []byte, meta FrameMeta, patterns []*regexp.Regexp, dst []Rect, textBuf []byte) ([]Rect, []byte) {
	w, h, stride := int(meta.Width), int(meta.Height), meta.Stride
	if stride <= 0 {
		stride = w * 4
	}
	if w <= 0 || h <= 0 || len(bgra) < stride*h {
		return dst, textBuf
	}
	block := meta.BlockSize
	if block == 0 {
		block = 64
	}

	for y := 0; y < h; y++ {
		runStart := -1
		textBuf = textBuf[:0]
		row := y * stride
		for x := 0; x < w; x++ {
			p := row + x*4
			ch, ok := grayASCII(bgra[p], bgra[p+1], bgra[p+2])
			if ok {
				if runStart < 0 {
					runStart = x
				}
				textBuf = append(textBuf, ch)
				continue
			}
			if runStart >= 0 && len(textBuf) >= 8 {
				dst = appendPatternHits(patterns, string(textBuf), Rect{
					X: uint16(runStart), Y: uint16(y),
					W: uint16(x - runStart), H: 1,
				}, block, dst)
			}
			runStart = -1
			textBuf = textBuf[:0]
		}
		if runStart >= 0 && len(textBuf) >= 8 {
			dst = appendPatternHits(patterns, string(textBuf), Rect{
				X: uint16(runStart), Y: uint16(y),
				W: uint16(w - runStart), H: 1,
			}, block, dst)
		}
	}
	return dst, textBuf
}

func grayASCII(b, g, r byte) (byte, bool) {
	if abs8(int(b)-int(g)) > 8 || abs8(int(b)-int(r)) > 8 {
		return 0, false
	}
	if b < 0x20 || b > 0x7e {
		return 0, false
	}
	return b, true
}

func abs8(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func appendPatternHits(patterns []*regexp.Regexp, text string, line Rect, block uint16, dst []Rect) []Rect {
	for i, re := range patterns {
		for _, m := range re.FindAllString(text, -1) {
			if i == 1 && !luhnValid(digitsOnly(m)) {
				continue
			}
			dst = append(dst, alignRect(line, block))
			break
		}
	}
	return dst
}

// PaintASCIIGray writes each byte of s as a gray BGRA pixel (B=G=R=c) starting at (x,y).
// Used by tests / overlays so PixelScan can find secrets without OCR.
func PaintASCIIGray(bgra []byte, stride, x, y int, s string) {
	if stride <= 0 || y < 0 || x < 0 {
		return
	}
	row := y * stride
	for i := 0; i < len(s); i++ {
		p := row + (x+i)*4
		if p+3 >= len(bgra) {
			return
		}
		c := s[i]
		bgra[p+0] = c
		bgra[p+1] = c
		bgra[p+2] = c
		bgra[p+3] = 255
	}
}
