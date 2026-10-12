//go:build windows

package engine

import (
	"fmt"
	"syscall"
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
	haloftone    = 4
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

type gdiSession struct {
	screenW, screenH int
	hdcScreen        uintptr
	hdcMem           uintptr
	hbm              uintptr
	oldObj           uintptr
	bgra             []byte
}

func openGDI(cfg CaptureConfig) (*gdiSession, int, int, error) {
	sw, _, _ := procGetSystemMetrics.Call(smCXScreen)
	sh, _, _ := procGetSystemMetrics.Call(smCYScreen)
	if sw == 0 || sh == 0 {
		return nil, 0, 0, fmt.Errorf("%w: GetSystemMetrics", ErrBackendUnavailable)
	}
	w, h := outputGeometry(cfg, int(sw), int(sh))

	hdc, _, err := procGetDC.Call(0)
	if hdc == 0 {
		return nil, 0, 0, fmt.Errorf("%w: GetDC: %v", ErrBackendUnavailable, err)
	}
	mem, _, err := procCreateCompatibleDC.Call(hdc)
	if mem == 0 {
		procReleaseDC.Call(0, hdc)
		return nil, 0, 0, fmt.Errorf("%w: CreateCompatibleDC: %v", ErrBackendUnavailable, err)
	}
	hbm, _, err := procCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
	if hbm == 0 {
		procDeleteDC.Call(mem)
		procReleaseDC.Call(0, hdc)
		return nil, 0, 0, fmt.Errorf("%w: CreateCompatibleBitmap: %v", ErrBackendUnavailable, err)
	}
	old, _, _ := procSelectObject.Call(mem, hbm)
	_, _, _ = procSetStretchBltMode.Call(mem, haloftone)

	return &gdiSession{
		screenW:   int(sw),
		screenH:   int(sh),
		hdcScreen: hdc,
		hdcMem:    mem,
		hbm:       hbm,
		oldObj:    old,
		bgra:      make([]byte, w*h*4),
	}, w, h, nil
}

func (s *gdiSession) capture(dst []byte, w, h int) error {
	var r uintptr
	var err error
	if w == s.screenW && h == s.screenH {
		r, _, err = procBitBlt.Call(s.hdcMem, 0, 0, uintptr(w), uintptr(h), s.hdcScreen, 0, 0, srcCopy)
	} else {
		r, _, err = procStretchBlt.Call(
			s.hdcMem, 0, 0, uintptr(w), uintptr(h),
			s.hdcScreen, 0, 0, uintptr(s.screenW), uintptr(s.screenH),
			srcCopy,
		)
	}
	if r == 0 {
		return fmt.Errorf("%w: blit: %v", ErrBackendUnavailable, err)
	}

	var bi bitmapInfoHeader
	bi.Size = uint32(unsafe.Sizeof(bi))
	bi.Width = int32(w)
	bi.Height = -int32(h)
	bi.Planes = 1
	bi.BitCount = 32
	bi.Compression = biRGB

	ret, _, err := procGetDIBits.Call(
		s.hdcMem, s.hbm, 0, uintptr(h),
		uintptr(unsafe.Pointer(&s.bgra[0])),
		uintptr(unsafe.Pointer(&bi)),
		dibRGBColors,
	)
	if ret == 0 {
		return fmt.Errorf("%w: GetDIBits: %v", ErrBackendUnavailable, err)
	}
	copy(dst, s.bgra[:w*h*4])
	return nil
}

func (s *gdiSession) close() {
	if s == nil {
		return
	}
	if s.oldObj != 0 {
		procSelectObject.Call(s.hdcMem, s.oldObj)
	}
	if s.hbm != 0 {
		procDeleteObject.Call(s.hbm)
	}
	if s.hdcMem != 0 {
		procDeleteDC.Call(s.hdcMem)
	}
	if s.hdcScreen != 0 {
		procReleaseDC.Call(0, s.hdcScreen)
	}
	*s = gdiSession{}
}
