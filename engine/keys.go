package engine

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DefaultXORSeed is a public demo seed (NOT a secret). Override via env / CLI.
// XOR protect is obfuscation against casual dumps — not cryptographic confidentiality.
const DefaultXORSeed uint64 = 0xC0FFEE42D00D

// EnvXORSeedKey is read by ResolveXORSeed when CLI seed is 0.
const EnvXORSeedKey = "SCREEN_STREAM_XOR_SEED"

// ResolveXORSeed picks CLI seed, else env SCREEN_STREAM_XOR_SEED, else DefaultXORSeed.
func ResolveXORSeed(cli uint64) uint64 {
	if cli != 0 {
		return cli
	}
	if v := strings.TrimSpace(os.Getenv(EnvXORSeedKey)); v != "" {
		if n, err := ParseXORSeed(v); err == nil {
			return n
		}
	}
	return DefaultXORSeed
}

// ParseXORSeed accepts decimal or 0x-hex.
func ParseXORSeed(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	base := 10
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		base = 16
		s = s[2:]
	}
	n, err := strconv.ParseUint(s, base, 64)
	if err != nil {
		return 0, fmt.Errorf("xor seed: %w", err)
	}
	return n, nil
}

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
