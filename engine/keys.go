package engine

// DefaultXORSeed is the shared stream protect seed (negotiate out-of-band in production).
const DefaultXORSeed uint64 = 0xC0FFEE42D00D

// FrameKey derives the per-frame protect key.
func FrameKey(seed, frameID uint64) uint64 {
	return seed ^ frameID
}

// TileKey derives the per-tile protect key from frame key and tile origin.
func TileKey(frameKey uint64, x, y uint16) uint64 {
	return frameKey ^ (uint64(x)<<32 | uint64(y))
}

// XORBuffer applies the rolling XOR protect/unprotect in place (symmetric).
func XORBuffer(buf []byte, key *uint64) {
	xorBuffer(buf, key)
}

// XORSeed returns the pipeline seed (for receiver setup).
func (p *StreamPipeline) XORSeed() uint64 { return p.seed }
