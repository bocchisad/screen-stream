package engine

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// VideoCodec selects the wire media path.
type VideoCodec int

const (
	// CodecDirtyPacket is the original tile/diff DataChannel path.
	CodecDirtyPacket VideoCodec = iota
	// CodecH264 is hardware H.264 over a WebRTC video track (SRTP).
	CodecH264
)

// ParseVideoCodec maps "dirty"|"h264".
func ParseVideoCodec(s string) (VideoCodec, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "dirty", "dirtypacket", "tiles":
		return CodecDirtyPacket, nil
	case "h264", "avc", "video":
		return CodecH264, nil
	default:
		return CodecDirtyPacket, fmt.Errorf("unknown codec %q (dirty|h264)", s)
	}
}

func (c VideoCodec) String() string {
	switch c {
	case CodecH264:
		return "h264"
	default:
		return "dirty"
	}
}

// EncodedFrame is one Annex-B H.264 access unit (AUD optional; SPS/PPS before IDR).
type EncodedFrame struct {
	Data      []byte
	PTS       time.Duration
	Duration  time.Duration
	Keyframe  bool
	Width     int
	Height    int
}

// EncoderConfig configures a hardware video encoder.
type EncoderConfig struct {
	Width   int
	Height  int
	FPS     int
	Bitrate int // bits/sec; 0 → derive from resolution
	// KeyframeEvery forces IDR every N frames (0 → encoder default ~2s).
	KeyframeEvery int
}

// VideoEncoder compresses BGRA frames to Annex-B H.264 using platform HW.
// No software fallback — Open fails with ErrNotSupported when HW is unavailable.
type VideoEncoder interface {
	Open(cfg EncoderConfig) error
	// Encode copies/uploads BGRA (stride bytes/row) and returns 0..N encoded AUs.
	Encode(ctx context.Context, bgra []byte, stride int, pts time.Duration) ([]EncodedFrame, error)
	Close() error
	Backend() string
}

// VideoDecoder expands Annex-B H.264 to BGRA using platform HW.
type VideoDecoder interface {
	Open(maxW, maxH int) error
	Decode(ctx context.Context, annexB []byte) (bgra []byte, width, height, stride int, err error)
	Close() error
	Backend() string
}

// DefaultH264Bitrate picks a sane real-time bitrate for screen content.
func DefaultH264Bitrate(w, h, fps int) int {
	if fps <= 0 {
		fps = 30
	}
	pixels := w * h
	// ~0.1 bit/pixel/frame for screen, clamp.
	br := pixels * fps / 10
	if br < 1_500_000 {
		br = 1_500_000
	}
	if br > 20_000_000 {
		br = 20_000_000
	}
	return br
}
