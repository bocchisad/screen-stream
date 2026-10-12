//go:build linux && cgo

package engine

/*
#cgo CFLAGS: -I${SRCDIR}/native
#cgo LDFLAGS: -lX11 -lXext
#include "helper.h"
#include "xshm_linux.h"
#include "portal_pipewire_linux.h"
*/
import "C"
import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unsafe"
)

type linuxGrabber struct {
	cfg     CaptureConfig
	frameID uint64
	started bool
	backend string // "x11-shm" | "pipewire"
}

func newPlatformGrabber() Grabber {
	return &linuxGrabber{}
}

func (g *linuxGrabber) Backend() string {
	if g.backend == "" {
		return "linux"
	}
	return g.backend
}

func (g *linuxGrabber) Open(ctx context.Context, cfg CaptureConfig) error {
	_ = ctx
	g.cfg = cfg
	if cfg.FPS <= 0 {
		g.cfg.FPS = 60
	}

	wayland := int(C.ss_wayland_session()) == 1 ||
		strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "wayland")

	if wayland && int(C.ss_x11_available()) == 0 {
		rc := int(C.ss_pw_start(C.int(g.cfg.FPS)))
		if rc == 0 {
			g.backend = "pipewire"
			g.started = true
			return nil
		}
		return fmt.Errorf("%w: PipeWire/portal (install libpipewire, grant portal share): code=%d — %s",
			ErrBackendUnavailable, rc, LinuxPortalHint())
	}

	if int(C.ss_x11_available()) == 1 {
		rc := int(C.ss_x11_start(C.int(cfg.DisplayIndex)))
		if rc == 0 {
			g.backend = "x11-shm"
			g.started = true
			return nil
		}
		return fmt.Errorf("%w: X11/XShm code=%d", ErrBackendUnavailable, rc)
	}

	// Last resort: try PipeWire even if session detection is ambiguous.
	rc := int(C.ss_pw_start(C.int(g.cfg.FPS)))
	if rc == 0 {
		g.backend = "pipewire"
		g.started = true
		return nil
	}
	return fmt.Errorf("%w: no DISPLAY and PipeWire unavailable", ErrBackendUnavailable)
}

func (g *linuxGrabber) Capture(ctx context.Context, dst []byte) (FrameMeta, error) {
	if !g.started {
		return FrameMeta{}, ErrStopped
	}
	select {
	case <-ctx.Done():
		return FrameMeta{}, ctx.Err()
	default:
	}
	if len(dst) == 0 {
		return FrameMeta{}, ErrBufferTooSmall
	}

	var info C.ss_frame_info
	var rc int
	switch g.backend {
	case "pipewire":
		rc = int(C.ss_pw_capture((*C.uint8_t)(unsafe.Pointer(&dst[0])), C.int(len(dst)), &info))
	default:
		rc = int(C.ss_x11_capture((*C.uint8_t)(unsafe.Pointer(&dst[0])), C.int(len(dst)), &info))
	}
	if rc == 1 {
		return FrameMeta{}, &PermissionError{Platform: "linux", Hint: LinuxPortalHint(), Err: ErrPermissionDenied}
	}
	if rc != 0 {
		return FrameMeta{}, fmt.Errorf("%w: capture code=%d", ErrBackendUnavailable, rc)
	}

	g.frameID++
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

func (g *linuxGrabber) OutputSize() (width, height, stride int, ok bool) {
	if !g.started || g.backend == "pipewire" {
		return 0, 0, 0, false
	}
	var w, h, s C.int
	if int(C.ss_x11_output_size(&w, &h, &s)) != 0 {
		return 0, 0, 0, false
	}
	return int(w), int(h), int(s), true
}

func (g *linuxGrabber) Close() error {
	if !g.started {
		return nil
	}
	switch g.backend {
	case "pipewire":
		C.ss_pw_stop()
	default:
		C.ss_x11_stop()
	}
	g.started = false
	return nil
}

var _ OutputSizer = (*linuxGrabber)(nil)
