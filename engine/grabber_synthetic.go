package engine

import (
	"context"
	"time"
)

// SyntheticGrabber generates animated BGRA frames without OS capture (bench / CI).
type SyntheticGrabber struct {
	Width, Height int
	FPS           int
	frameID       uint64
	t0            time.Time
}

func NewSyntheticGrabber(w, h, fps int) *SyntheticGrabber {
	if w <= 0 {
		w = 1280
	}
	if h <= 0 {
		h = 720
	}
	if fps <= 0 {
		fps = 60
	}
	return &SyntheticGrabber{Width: w, Height: h, FPS: fps, t0: time.Now()}
}

func (g *SyntheticGrabber) Backend() string { return "synthetic" }

func (g *SyntheticGrabber) Open(ctx context.Context, cfg CaptureConfig) error {
	_ = ctx
	if cfg.FPS > 0 {
		g.FPS = cfg.FPS
	}
	return nil
}

func (g *SyntheticGrabber) Capture(ctx context.Context, dst []byte) (FrameMeta, error) {
	select {
	case <-ctx.Done():
		return FrameMeta{}, ctx.Err()
	default:
	}
	stride := g.Width * 4
	need := stride * g.Height
	if len(dst) < need {
		return FrameMeta{}, ErrBufferTooSmall
	}
	g.frameID++
	// Moving bar + noise-ish pattern (deterministic).
	phase := int(g.frameID * 3)
	for y := 0; y < g.Height; y++ {
		for x := 0; x < g.Width; x++ {
			off := y*stride + x*4
			v := uint8((x + phase) ^ (y << 1))
			bar := 0
			if (x+phase)%g.Width < 40 {
				bar = 200
			}
			dst[off+0] = v           // B
			dst[off+1] = uint8(bar)  // G
			dst[off+2] = uint8(255 - v) // R
			dst[off+3] = 255
		}
	}
	// Pace toward target FPS (Sleep is fine outside zero-alloc claims for OS path).
	target := time.Second / time.Duration(g.FPS)
	elapsed := time.Since(g.t0)
	ideal := time.Duration(g.frameID) * target
	if ideal > elapsed {
		select {
		case <-ctx.Done():
			return FrameMeta{}, ctx.Err()
		default:
			time.Sleep(ideal - elapsed)
		}
	}
	return FrameMeta{
		FrameID: g.frameID,
		PTS:     time.Since(g.t0),
		Width:   uint16(g.Width),
		Height:  uint16(g.Height),
		Stride:  stride,
		Format:  PixelBGRA,
	}, nil
}

func (g *SyntheticGrabber) Close() error { return nil }

var _ Grabber = (*SyntheticGrabber)(nil)
