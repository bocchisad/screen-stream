package engine

// Portable implementations (always in the package so gopls cross-GOOS analysis works).
// On cgo builds these are still used; native C in protect.c remains available for future hot paths.

func xorBuffer(buf []byte, key *uint64) {
	k := *key
	i := 0
	for ; i+8 <= len(buf); i += 8 {
		k = k*6364136223846793005 + 1
		u := uint64(buf[i]) | uint64(buf[i+1])<<8 | uint64(buf[i+2])<<16 | uint64(buf[i+3])<<24 |
			uint64(buf[i+4])<<32 | uint64(buf[i+5])<<40 | uint64(buf[i+6])<<48 | uint64(buf[i+7])<<56
		u ^= k
		buf[i] = byte(u)
		buf[i+1] = byte(u >> 8)
		buf[i+2] = byte(u >> 16)
		buf[i+3] = byte(u >> 24)
		buf[i+4] = byte(u >> 32)
		buf[i+5] = byte(u >> 40)
		buf[i+6] = byte(u >> 48)
		buf[i+7] = byte(u >> 56)
	}
	for ; i < len(buf); i++ {
		k = k*6364136223846793005 + 1
		buf[i] ^= byte(k >> 56)
	}
	*key = k
}

func blockHashes(bgra []byte, width, height, stride, blockSize int, out []uint64) {
	bx := (width + blockSize - 1) / blockSize
	by := (height + blockSize - 1) / blockSize
	idx := 0
	for byi := 0; byi < by; byi++ {
		for bxi := 0; bxi < bx; bxi++ {
			x0 := bxi * blockSize
			y0 := byi * blockSize
			x1 := x0 + blockSize
			y1 := y0 + blockSize
			if x1 > width {
				x1 = width
			}
			if y1 > height {
				y1 = height
			}
			h := uint64(14695981039346656037)
			for y := y0; y < y1; y++ {
				row := bgra[y*stride+x0*4 : y*stride+x1*4]
				for _, b := range row {
					h ^= uint64(b)
					h *= 1099511628211
				}
			}
			if idx < len(out) {
				out[idx] = h
			}
			idx++
		}
	}
}

func diffBlocks(curr, prev []uint64, blocksX, blocksY, blockSize, frameW, frameH int, outRects []uint16, forceAll bool) int {
	maxOut := len(outRects) / 4
	count := 0
	for by := 0; by < blocksY; by++ {
		runX0 := -1
		for bx := 0; bx <= blocksX; bx++ {
			dirty := false
			if bx < blocksX {
				i := by*blocksX + bx
				dirty = forceAll || curr[i] != prev[i]
			}
			if dirty && runX0 < 0 {
				runX0 = bx
			} else if !dirty && runX0 >= 0 {
				if count >= maxOut {
					return count
				}
				x := runX0 * blockSize
				y := by * blockSize
				w := (bx - runX0) * blockSize
				h := blockSize
				if x+w > frameW {
					w = frameW - x
				}
				if y+h > frameH {
					h = frameH - y
				}
				outRects[count*4+0] = uint16(x)
				outRects[count*4+1] = uint16(y)
				outRects[count*4+2] = uint16(w)
				outRects[count*4+3] = uint16(h)
				count++
				runX0 = -1
			}
		}
	}
	return count
}

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
