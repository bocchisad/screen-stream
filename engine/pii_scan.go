package engine

// scanBGRASecrets finds grayscale (B≈G≈R) printable ASCII runs and matches
// secret patterns without heap alloc (no string/regex on the hot path).
func scanBGRASecrets(bgra []byte, meta FrameMeta, dst []Rect, textBuf []byte) ([]Rect, []byte) {
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
				if secretHitBytes(textBuf) {
					dst = append(dst, alignRect(Rect{
						X: uint16(runStart), Y: uint16(y),
						W: uint16(x - runStart), H: 1,
					}, block))
				}
			}
			runStart = -1
			textBuf = textBuf[:0]
		}
		if runStart >= 0 && len(textBuf) >= 8 {
			if secretHitBytes(textBuf) {
				dst = append(dst, alignRect(Rect{
					X: uint16(runStart), Y: uint16(y),
					W: uint16(w - runStart), H: 1,
				}, block))
			}
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

// secretHitBytes matches ghp_ / Luhn PAN / password= / api_key= without alloc.
func secretHitBytes(text []byte) bool {
	if hasGHToken(text) || hasLuhnPAN(text) || hasKVSecret(text, []byte("password")) ||
		hasKVSecret(text, []byte("api_key")) || hasKVSecret(text, []byte("api-key")) {
		return true
	}
	return false
}

func hasGHToken(text []byte) bool {
	const prefix = "ghp_"
	for i := 0; i+4+20 <= len(text); i++ {
		if text[i] != 'g' || text[i+1] != 'h' || text[i+2] != 'p' || text[i+3] != '_' {
			continue
		}
		n := 0
		j := i + 4
		for j < len(text) && isAlnum(text[j]) {
			n++
			j++
		}
		if n >= 20 {
			return true
		}
	}
	return false
}

func isAlnum(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func hasLuhnPAN(text []byte) bool {
	i := 0
	for i < len(text) {
		if text[i] < '0' || text[i] > '9' {
			i++
			continue
		}
		start := i
		digits := 0
		var buf [19]byte
		for i < len(text) && digits < 19 {
			c := text[i]
			if c >= '0' && c <= '9' {
				buf[digits] = c
				digits++
				i++
				continue
			}
			if (c == ' ' || c == '-') && digits > 0 {
				i++
				continue
			}
			break
		}
		if digits >= 13 && digits <= 19 && luhnValidBytes(buf[:digits]) {
			_ = start
			return true
		}
	}
	return false
}

func luhnValidBytes(digits []byte) bool {
	n := len(digits)
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

func hasKVSecret(text, key []byte) bool {
	// case-insensitive key, then optional space, : or =, then non-space.
	for i := 0; i+len(key)+2 <= len(text); i++ {
		if !equalFoldASCII(text[i:i+len(key)], key) {
			continue
		}
		j := i + len(key)
		for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
			j++
		}
		if j >= len(text) || (text[j] != ':' && text[j] != '=') {
			continue
		}
		j++
		for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
			j++
		}
		if j < len(text) && text[j] > ' ' {
			return true
		}
	}
	return false
}

func equalFoldASCII(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// PaintASCIIGray writes each byte of s as a gray BGRA pixel (B=G=R=c) starting at (x,y).
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
