//go:build windows

package engine

import (
	"context"
	"fmt"
	"time"
)

// windowsGrabber: CaptureCPU → GDI; CaptureGPUSurface → DXGI Desktop Duplication.
type windowsGrabber struct {
	cfg     CaptureConfig
	frameID uint64
	started bool
	backend string // "gdi" | "dxgi"

	w, h int

	gdi  *gdiSession
	dxgi *dxgiSession
}

func newPlatformGrabber() Grabber {
	return &windowsGrabber{}
}

func (g *windowsGrabber) Backend() string {
	if g.backend != "" {
		return g.backend
	}
	if g.cfg.Mode == CaptureGPUSurface {
		return "dxgi"
	}
	return "gdi"
}

func (g *windowsGrabber) Open(ctx context.Context, cfg CaptureConfig) error {
	_ = ctx
	g.cfg = cfg
	if cfg.FPS <= 0 {
		g.cfg.FPS = 60
	}

	if cfg.Mode == CaptureGPUSurface {
		sess, w, h, err := openDXGI(cfg)
		if err != nil {
			return fmt.Errorf("%w: DXGI Desktop Duplication (gpu-surface): %v — try -capture=cpu (GDI)", ErrBackendUnavailable, err)
		}
		g.dxgi = sess
		g.w, g.h = w, h
		g.backend = "dxgi"
		g.started = true
		return nil
	}

	sess, w, h, err := openGDI(cfg)
	if err != nil {
		return err
	}
	g.gdi = sess
	g.w, g.h = w, h
	g.backend = "gdi"
	g.started = true
	return nil
}

func (g *windowsGrabber) Capture(ctx context.Context, dst []byte) (FrameMeta, error) {
	if !g.started {
		return FrameMeta{}, ErrStopped
	}
	select {
	case <-ctx.Done():
		return FrameMeta{}, ctx.Err()
	default:
	}
	need := g.w * g.h * 4
	if len(dst) < need {
		return FrameMeta{}, ErrBufferTooSmall
	}

	timeoutMs := uint(100)
	if g.cfg.FPS > 0 {
		timeoutMs = uint(2000 / g.cfg.FPS)
		if timeoutMs < 16 {
			timeoutMs = 16
		}
		if timeoutMs > 250 {
			timeoutMs = 250
		}
	}

	var err error
	switch g.backend {
	case "dxgi":
		err = g.dxgi.capture(dst[:need], g.w, g.h, timeoutMs)
	default:
		err = g.gdi.capture(dst[:need], g.w, g.h)
	}
	if err != nil {
		return FrameMeta{}, err
	}

	g.frameID++
	return FrameMeta{
		FrameID:   g.frameID,
		PTS:       time.Duration(time.Now().UnixNano()),
		Width:     uint16(g.w),
		Height:    uint16(g.h),
		Stride:    g.w * 4,
		Format:    PixelBGRA,
		BlockSize: g.cfg.BlockSize,
	}, nil
}

func (g *windowsGrabber) OutputSize() (width, height, stride int, ok bool) {
	if !g.started {
		return 0, 0, 0, false
	}
	return g.w, g.h, g.w * 4, true
}

func (g *windowsGrabber) Close() error {
	if !g.started {
		return nil
	}
	if g.dxgi != nil {
		g.dxgi.close()
		g.dxgi = nil
	}
	if g.gdi != nil {
		g.gdi.close()
		g.gdi = nil
	}
	g.started = false
	g.backend = ""
	return nil
}

func outputGeometry(cfg CaptureConfig, screenW, screenH int) (w, h int) {
	w, h = screenW, screenH
	if cfg.ScalePercent > 0 && cfg.ScalePercent < 100 {
		w = w * cfg.ScalePercent / 100
		h = h * cfg.ScalePercent / 100
	}
	if cfg.MaxWidth > 0 && w > cfg.MaxWidth {
		w = cfg.MaxWidth
	}
	if cfg.MaxHeight > 0 && h > cfg.MaxHeight {
		h = cfg.MaxHeight
	}
	if w < 16 {
		w = 16
	}
	if h < 16 {
		h = 16
	}
	return w, h
}

var _ Grabber = (*windowsGrabber)(nil)
var _ OutputSizer = (*windowsGrabber)(nil)
