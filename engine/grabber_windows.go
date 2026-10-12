//go:build windows

package engine

import (
	"context"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                     = syscall.NewLazyDLL("user32.dll")
	gdi32                      = syscall.NewLazyDLL("gdi32.dll")
	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procStretchBlt             = gdi32.NewProc("StretchBlt")
	procSetStretchBltMode      = gdi32.NewProc("SetStretchBltMode")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
)

const (
	smCXScreen   = 0
	smCYScreen   = 1
	srcCopy      = 0x00CC0020
	biRGB        = 0
	dibRGBColors = 0
	haloftone    = 4 // COLORONCOLOR / HALFTONE
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type windowsGrabber struct {
	cfg       CaptureConfig
	frameID   uint64
	started   bool
	w, h      int
	screenW   int
	screenH   int
	hdcScreen uintptr
	hdcMem    uintptr
	hbm       uintptr
	oldObj    uintptr
	bgra      []byte
}

func newPlatformGrabber() Grabber {
	return &windowsGrabber{}
}

func (g *windowsGrabber) Backend() string {
	if g.cfg.Mode == CaptureGPUSurface {
		return "gdi+gpu-surface-pending"
	}
	return "gdi"
}

func (g *windowsGrabber) Open(ctx context.Context, cfg CaptureConfig) error {
	_ = ctx
	g.cfg = cfg
	if cfg.FPS <= 0 {
		g.cfg.FPS = 60
	}

	sw, _, _ := procGetSystemMetrics.Call(smCXScreen)
	sh, _, _ := procGetSystemMetrics.Call(smCYScreen)
	if sw == 0 || sh == 0 {
		return fmt.Errorf("%w: GetSystemMetrics", ErrBackendUnavailable)
	}
	g.screenW, g.screenH = int(sw), int(sh)
	g.w, g.h = g.screenW, g.screenH
	if cfg.ScalePercent > 0 && cfg.ScalePercent < 100 {
		g.w = g.w * cfg.ScalePercent / 100
		g.h = g.h * cfg.ScalePercent / 100
	}
	if cfg.MaxWidth > 0 && g.w > cfg.MaxWidth {
		g.w = cfg.MaxWidth
	}
	if cfg.MaxHeight > 0 && g.h > cfg.MaxHeight {
		g.h = cfg.MaxHeight
	}
	if g.w < 16 {
		g.w = 16
	}
	if g.h < 16 {
		g.h = 16
	}

	hdc, _, err := procGetDC.Call(0)
	if hdc == 0 {
		return fmt.Errorf("%w: GetDC: %v", ErrBackendUnavailable, err)
	}
	mem, _, err := procCreateCompatibleDC.Call(hdc)
	if mem == 0 {
		procReleaseDC.Call(0, hdc)
		return fmt.Errorf("%w: CreateCompatibleDC: %v", ErrBackendUnavailable, err)
	}
	hbm, _, err := procCreateCompatibleBitmap.Call(hdc, uintptr(g.w), uintptr(g.h))
	if hbm == 0 {
		procDeleteDC.Call(mem)
		procReleaseDC.Call(0, hdc)
		return fmt.Errorf("%w: CreateCompatibleBitmap: %v", ErrBackendUnavailable, err)
	}
	old, _, _ := procSelectObject.Call(mem, hbm)
	_, _, _ = procSetStretchBltMode.Call(mem, haloftone)

	g.hdcScreen = hdc
	g.hdcMem = mem
	g.hbm = hbm
	g.oldObj = old
	g.bgra = make([]byte, g.w*g.h*4)
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

	var r uintptr
	var err error
	if g.w == g.screenW && g.h == g.screenH {
		r, _, err = procBitBlt.Call(g.hdcMem, 0, 0, uintptr(g.w), uintptr(g.h), g.hdcScreen, 0, 0, srcCopy)
	} else {
		r, _, err = procStretchBlt.Call(
			g.hdcMem, 0, 0, uintptr(g.w), uintptr(g.h),
			g.hdcScreen, 0, 0, uintptr(g.screenW), uintptr(g.screenH),
			srcCopy,
		)
	}
	if r == 0 {
		return FrameMeta{}, fmt.Errorf("%w: blit: %v", ErrBackendUnavailable, err)
	}

	var bi bitmapInfoHeader
	bi.Size = uint32(unsafe.Sizeof(bi))
	bi.Width = int32(g.w)
	bi.Height = -int32(g.h) // top-down BGRA
	bi.Planes = 1
	bi.BitCount = 32
	bi.Compression = biRGB

	ret, _, err := procGetDIBits.Call(
		g.hdcMem, g.hbm, 0, uintptr(g.h),
		uintptr(unsafe.Pointer(&g.bgra[0])),
		uintptr(unsafe.Pointer(&bi)),
		dibRGBColors,
	)
	if ret == 0 {
		return FrameMeta{}, fmt.Errorf("%w: GetDIBits: %v", ErrBackendUnavailable, err)
	}
	copy(dst[:need], g.bgra)

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
	if g.oldObj != 0 {
		procSelectObject.Call(g.hdcMem, g.oldObj)
	}
	if g.hbm != 0 {
		procDeleteObject.Call(g.hbm)
	}
	if g.hdcMem != 0 {
		procDeleteDC.Call(g.hdcMem)
	}
	if g.hdcScreen != 0 {
		procReleaseDC.Call(0, g.hdcScreen)
	}
	g.hdcScreen, g.hdcMem, g.hbm, g.oldObj = 0, 0, 0, 0
	g.bgra = nil
	g.started = false
	return nil
}

var _ Grabber = (*windowsGrabber)(nil)
var _ OutputSizer = (*windowsGrabber)(nil)
