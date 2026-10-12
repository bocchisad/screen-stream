//go:build darwin && cgo

package engine

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -I${SRCDIR} -I${SRCDIR}/native
#cgo LDFLAGS: -framework ScreenCaptureKit -framework CoreMedia -framework CoreVideo -framework Foundation -framework ApplicationServices -framework AVFoundation -framework CoreGraphics -framework AppKit
#include "helper.h"
#include <stdlib.h>
*/
import "C"
import (
	"context"
	"fmt"
	"time"
	"unsafe"
)

type macGrabber struct {
	cfg     CaptureConfig
	frameID uint64
	started bool
	w, h    int
}

func newPlatformGrabber() Grabber {
	return &macGrabber{}
}

func (g *macGrabber) Backend() string { return "screencapturekit" }

func (g *macGrabber) Open(ctx context.Context, cfg CaptureConfig) error {
	_ = ctx
	g.cfg = cfg
	if cfg.FPS <= 0 {
		cfg.FPS = 60
		g.cfg.FPS = 60
	}
	if cfg.ScalePercent <= 0 {
		cfg.ScalePercent = 100
		g.cfg.ScalePercent = 100
	}
	C.ss_mac_clear_excludes()
	for _, bid := range cfg.ExcludeBundleIDs {
		cBid := C.CString(bid)
		C.ss_mac_set_exclude_bundle(cBid)
		C.free(unsafe.Pointer(cBid))
	}
	for _, wid := range cfg.ExcludeWindowIDs {
		C.ss_mac_set_exclude_window(C.uint32_t(wid))
	}
	rc := int(C.ss_mac_start_ex(
		C.int(cfg.DisplayIndex),
		C.int(cfg.FPS),
		C.int(cfg.ScalePercent),
		C.int(cfg.MaxWidth),
		C.int(cfg.MaxHeight),
	))
	switch rc {
	case 0:
		g.started = true
		return nil
	case 1:
		return &PermissionError{
			Platform: "darwin",
			Hint:     MacPermissionHint(),
			Err:      ErrPermissionDenied,
		}
	case 2:
		return fmt.Errorf("%w: ScreenCaptureKit", ErrBackendUnavailable)
	default:
		return fmt.Errorf("%w: start failed code=%d", ErrBackendUnavailable, rc)
	}
}

func (g *macGrabber) Capture(ctx context.Context, dst []byte) (FrameMeta, error) {
	if !g.started {
		return FrameMeta{}, ErrStopped
	}
	select {
	case <-ctx.Done():
		return FrameMeta{}, ctx.Err()
	default:
	}

	var info C.ss_frame_info
	// Wait up to ~2 frame intervals (high-res process can take >16ms; 32ms was too tight).
	wait := 100 * time.Millisecond
	if g.cfg.FPS > 0 {
		wait = time.Second / time.Duration(g.cfg.FPS) * 2
		if wait < 50*time.Millisecond {
			wait = 50 * time.Millisecond
		}
		if wait > 250*time.Millisecond {
			wait = 250 * time.Millisecond
		}
	}
	deadline := time.Now().Add(wait)
	for {
		rc := int(C.ss_mac_capture((*C.uint8_t)(unsafe.Pointer(&dst[0])), C.int(len(dst)), &info))
		if rc == 0 {
			break
		}
		if rc == 1 {
			return FrameMeta{}, &PermissionError{Platform: "darwin", Hint: MacPermissionHint(), Err: ErrPermissionDenied}
		}
		if rc == 4 {
			if time.Now().After(deadline) {
				return FrameMeta{}, fmt.Errorf("%w: no frame", ErrBackendUnavailable)
			}
			select {
			case <-ctx.Done():
				return FrameMeta{}, ctx.Err()
			default:
			}
			time.Sleep(500 * time.Microsecond) // no heap (unlike time.After)
			continue
		}
		return FrameMeta{}, fmt.Errorf("%w: capture code=%d", ErrBackendUnavailable, rc)
	}

	g.frameID++
	g.w, g.h = int(info.width), int(info.height)
	return FrameMeta{
		FrameID:   g.frameID,
		PTS:       time.Duration(time.Now().UnixNano()),
		Width:     uint16(info.width),
		Height:    uint16(info.height),
		Stride:    int(info.stride),
		Format:    PixelBGRA,
		BlockSize: g.cfg.BlockSize,
	}, nil
}

func (g *macGrabber) OutputSize() (width, height, stride int, ok bool) {
	if !g.started {
		return 0, 0, 0, false
	}
	var w, h, s C.int
	if int(C.ss_mac_output_size(&w, &h, &s)) != 0 {
		return 0, 0, 0, false
	}
	return int(w), int(h), int(s), true
}

func (g *macGrabber) Close() error {
	if !g.started {
		return nil
	}
	C.ss_mac_stop()
	g.started = false
	return nil
}

var _ OutputSizer = (*macGrabber)(nil)
