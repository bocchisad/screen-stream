//go:build linux && !cgo

package engine

import (
	"context"
	"fmt"
)

type linuxNoCGOGrabber struct{}

func newPlatformGrabber() Grabber { return &linuxNoCGOGrabber{} }

func (g *linuxNoCGOGrabber) Backend() string { return "linux-nocgo" }

func (g *linuxNoCGOGrabber) Open(ctx context.Context, cfg CaptureConfig) error {
	_ = ctx
	_ = cfg
	return fmt.Errorf("%w: rebuild with CGO_ENABLED=1 and libx11/libxext", ErrBackendUnavailable)
}

func (g *linuxNoCGOGrabber) Capture(ctx context.Context, dst []byte) (FrameMeta, error) {
	_ = ctx
	_ = dst
	return FrameMeta{}, ErrBackendUnavailable
}

func (g *linuxNoCGOGrabber) Close() error { return nil }
