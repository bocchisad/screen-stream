package engine

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Shared contracts for capture, pipeline, and packet handoff.

var (
	ErrPermissionDenied    = errors.New("screen capture permission denied")
	ErrBackendUnavailable  = errors.New("capture backend unavailable")
	ErrNotSupported        = errors.New("platform not supported")
	ErrBufferTooSmall      = errors.New("frame buffer too small")
	ErrStopped             = errors.New("grabber stopped")
	ErrRingFull            = errors.New("delivery ring full")
)

// PixelFormat is always BGRA for v0.
type PixelFormat uint8

const (
	PixelBGRA PixelFormat = iota
)

// Rect is an inclusive-exclusive screen-space rectangle.
type Rect struct {
	X, Y, W, H uint16
}

// FrameMeta describes geometry and timing for one captured frame.
type FrameMeta struct {
	FrameID   uint64
	PTS       time.Duration
	Width     uint16
	Height    uint16
	Stride    int // bytes per row
	Format    PixelFormat
	BlockSize uint16
}

// Frame holds a pool-owned BGRA buffer. Bytes may be XOR-protected in place.
type Frame struct {
	Meta    FrameMeta
	Pixels  []byte // len == Stride * Height; pool-backed
	Protected bool
	pool    *BufferPool
}

// Release returns the frame buffer to the pool. Safe to call once.
func (f *Frame) Release() {
	if f == nil || f.pool == nil {
		return
	}
	f.pool.Release(f.Pixels)
	f.Pixels = nil
	f.pool = nil
}

// DirtyRegion is one changed tile/AABB with payload slice into DirtyPacket.Payload.
type DirtyRegion struct {
	Rect
	PayloadOffset uint32
	PayloadLen    uint32
}

// Packet flags.
const (
	FlagKeyframe   uint16 = 1 << 0
	FlagCompressed uint16 = 1 << 1 // zlib over Payload blob
)

// DirtyPacket is the only type crossing the engine→streamer boundary.
type DirtyPacket struct {
	Magic       uint32
	Version     uint16
	Flags       uint16
	FrameID     uint64
	PTSUs       uint64
	Width       uint16
	Height      uint16
	BlockSize   uint16
	Regions     []DirtyRegion
	Payload     []byte // protected BGRA tiles, pool-backed
	raw         []byte // full serialized buffer when packed
	pool        *PacketPool
}

const PacketMagic uint32 = 0x53535231 // "SSR1"

// Release returns packet backing store to the pool.
// Must not nil Payload/Regions after enqueue — that would force realloc on next Acquire.
func (p *DirtyPacket) Release() {
	if p == nil || p.pool == nil {
		return
	}
	p.pool.Release(p)
}

// Grabber captures screen frames into caller-provided / pool buffers.
type Grabber interface {
	Open(ctx context.Context, cfg CaptureConfig) error
	// Capture writes one BGRA frame into dst (must be large enough).
	// Zero heap alloc in steady state.
	Capture(ctx context.Context, dst []byte) (FrameMeta, error)
	Close() error
	Backend() string
}

// OutputSizer is optional: report configured geometry after Open (no probe frame).
type OutputSizer interface {
	OutputSize() (width, height, stride int, ok bool)
}

// CaptureConfig controls capture session.
type CaptureConfig struct {
	DisplayIndex int
	FPS          int
	// ExcludeBundleIDs / ExcludeWindowIDs: apps/windows omitted from the share set.
	ExcludeBundleIDs []string
	ExcludeWindowIDs []uint32
	BlockSize        uint16 // default 64
	// ScalePercent: 100 = native Retina pixels, 50 = half. 0 → 100.
	ScalePercent int
	// MaxWidth / MaxHeight cap output (0 = unlimited). Applied after scale.
	MaxWidth  int
	MaxHeight int
}

// Pipeline turns successive frames into DirtyPackets (diff, protect, redact).
type Pipeline interface {
	Process(ctx context.Context, frame *Frame) (*DirtyPacket, error)
	Close() error
}

// PIIDetector returns screen-space rects to redact before send.
// Pipeline passes plaintext BGRA scratch (not XOR-protected).
type PIIDetector interface {
	Detect(ctx context.Context, meta FrameMeta, plaintextBGRA []byte) ([]Rect, error)
}

// PermissionError wraps ErrPermissionDenied with OS-specific guidance.
type PermissionError struct {
	Platform string
	Hint     string
	Err      error
}

func (e *PermissionError) Error() string {
	return fmt.Sprintf("%s: %v (%s)", e.Platform, e.Err, e.Hint)
}

func (e *PermissionError) Unwrap() error { return e.Err }

func MacPermissionHint() string {
	return "System Settings → Privacy & Security → Screen Recording — enable this app, then restart"
}

func LinuxPortalHint() string {
	return "grant screen share in the desktop portal dialog, or check XDG_SESSION_TYPE / PipeWire"
}
