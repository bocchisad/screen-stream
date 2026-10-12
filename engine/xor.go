package engine

// xorBuffer applies rolling XOR in pure Go (no cgo).
// Hot path uses this for full-frame and per-tile protect to avoid cgo stub allocs.
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
