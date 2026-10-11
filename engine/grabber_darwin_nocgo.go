//go:build darwin && !cgo

package engine

import (
	"context"
	"fmt"
)

type darwinNoCGOGrabber struct{}

func newPlatformGrabber() Grabber { return &darwinNoCGOGrabber{} }

func (g *darwinNoCGOGrabber) Backend() string { return "darwin-nocgo" }

func (g *darwinNoCGOGrabber) Open(ctx context.Context, cfg CaptureConfig) error {
	_ = ctx
	_ = cfg
	return fmt.Errorf("%w: rebuild with CGO_ENABLED=1 (ScreenCaptureKit)", ErrBackendUnavailable)
}

func (g *darwinNoCGOGrabber) Capture(ctx context.Context, dst []byte) (FrameMeta, error) {
	_ = ctx
	_ = dst
	return FrameMeta{}, ErrBackendUnavailable
}

func (g *darwinNoCGOGrabber) Close() error { return nil }
