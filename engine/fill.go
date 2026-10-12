package engine

func fillBlack(bgra []byte, stride, x, y, w, h int) {
	for row := y; row < y+h; row++ {
		off := row*stride + x*4
		for col := 0; col < w; col++ {
			p := off + col*4
			bgra[p+0] = 0
			bgra[p+1] = 0
			bgra[p+2] = 0
			bgra[p+3] = 255
		}
	}
}
