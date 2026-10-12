package engine

// Hot-path hash/diff in pure Go — zero cgo stub allocs (TZ acceptance).
// C implementations remain in native/protect.c for optional/tooling use.

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
				i := 0
				for ; i+8 <= len(row); i += 8 {
					w := uint64(row[i]) | uint64(row[i+1])<<8 | uint64(row[i+2])<<16 | uint64(row[i+3])<<24 |
						uint64(row[i+4])<<32 | uint64(row[i+5])<<40 | uint64(row[i+6])<<48 | uint64(row[i+7])<<56
					h ^= w
					h *= 1099511628211
				}
				for ; i < len(row); i++ {
					h ^= uint64(row[i])
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
	wcount := 0
	for i := 0; i < count; i++ {
		x, y, w, h := outRects[i*4+0], outRects[i*4+1], outRects[i*4+2], outRects[i*4+3]
		if wcount > 0 {
			p := outRects[(wcount-1)*4:]
			if p[0] == x && p[2] == w && p[1]+p[3] == y {
				p[3] += h
				continue
			}
		}
		outRects[wcount*4+0] = x
		outRects[wcount*4+1] = y
		outRects[wcount*4+2] = w
		outRects[wcount*4+3] = h
		wcount++
	}
	return wcount
}

func hashAndDiff(plain []byte, w, h, stride, blockSize int, curr, prev []uint64, havePrev, forceAll bool, outRects []uint16) int {
	blockHashes(plain, w, h, stride, blockSize, curr)
	bx := (w + blockSize - 1) / blockSize
	by := (h + blockSize - 1) / blockSize
	force := forceAll || !havePrev
	return diffBlocks(curr, prev, bx, by, blockSize, w, h, outRects, force)
}
