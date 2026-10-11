//go:build !darwin && !linux

package engine

import (
	"context"
	"fmt"
)

type stubGrabber struct{}

func newPlatformGrabber() Grabber { return &stubGrabber{} }

func (g *stubGrabber) Backend() string { return "stub" }

func (g *stubGrabber) Open(ctx context.Context, cfg CaptureConfig) error {
	_ = ctx
	_ = cfg
	return fmt.Errorf("%w: only darwin and linux are implemented", ErrNotSupported)
}

func (g *stubGrabber) Capture(ctx context.Context, dst []byte) (FrameMeta, error) {
	_ = ctx
	_ = dst
	return FrameMeta{}, ErrNotSupported
}

func (g *stubGrabber) Close() error { return nil }
