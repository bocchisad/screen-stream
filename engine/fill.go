package engine

func fillBlack(bgra []byte, stride, x, y, w, h int) {
	if w <= 0 || h <= 0 || stride <= 0 || x < 0 || y < 0 {
		return
	}
	for row := y; row < y+h; row++ {
		off := row*stride + x*4
		if off < 0 || off+w*4 > len(bgra) {
			return
		}
		for col := 0; col < w; col++ {
			p := off + col*4
			bgra[p+0] = 0
			bgra[p+1] = 0
			bgra[p+2] = 0
			bgra[p+3] = 255
		}
	}
}
