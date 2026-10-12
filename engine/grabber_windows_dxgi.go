//go:build windows

package engine

import (
	"errors"
	"fmt"
	"image"
	"runtime"

	"github.com/kirides/go-d3d/d3d11"
	"github.com/kirides/go-d3d/outputduplication"
	"github.com/kirides/go-d3d/win"
)

type dxgiSession struct {
	device    *d3d11.ID3D11Device
	deviceCtx *d3d11.ID3D11DeviceContext
	ddup      *outputduplication.OutputDuplicator

	nativeW, nativeH int
	outW, outH       int
	rgba             *image.RGBA
	lastBGRA         []byte
	haveLast         bool
	locked           bool
}

func openDXGI(cfg CaptureConfig) (*dxgiSession, int, int, error) {
	runtime.LockOSThread()

	if win.IsValidDpiAwarenessContext(win.DpiAwarenessContextPerMonitorAwareV2) {
		_, _ = win.SetThreadDpiAwarenessContext(win.DpiAwarenessContextPerMonitorAwareV2)
	}

	device, deviceCtx, err := d3d11.NewD3D11Device()
	if err != nil {
		runtime.UnlockOSThread()
		return nil, 0, 0, fmt.Errorf("D3D11 device: %w", err)
	}

	output := uint(cfg.DisplayIndex)
	ddup, err := outputduplication.NewIDXGIOutputDuplication(device, deviceCtx, output)
	if err != nil {
		deviceCtx.Release()
		device.Release()
		runtime.UnlockOSThread()
		return nil, 0, 0, fmt.Errorf("DuplicateOutput: %w", err)
	}

	bounds, err := ddup.GetBounds()
	if err != nil {
		ddup.Release()
		deviceCtx.Release()
		device.Release()
		runtime.UnlockOSThread()
		return nil, 0, 0, fmt.Errorf("GetBounds: %w", err)
	}
	nw, nh := bounds.Dx(), bounds.Dy()
	if nw <= 0 || nh <= 0 {
		ddup.Release()
		deviceCtx.Release()
		device.Release()
		runtime.UnlockOSThread()
		return nil, 0, 0, fmt.Errorf("invalid desktop bounds %dx%d", nw, nh)
	}
	ow, oh := outputGeometry(cfg, nw, nh)

	s := &dxgiSession{
		device:    device,
		deviceCtx: deviceCtx,
		ddup:      ddup,
		nativeW:   nw,
		nativeH:   nh,
		outW:      ow,
		outH:      oh,
		rgba:      image.NewRGBA(image.Rect(0, 0, nw, nh)),
		lastBGRA:  make([]byte, ow*oh*4),
		locked:    true,
	}
	return s, ow, oh, nil
}

func (s *dxgiSession) capture(dst []byte, w, h int, timeoutMs uint) error {
	if s == nil || s.ddup == nil {
		return ErrStopped
	}
	err := s.ddup.GetImage(s.rgba, timeoutMs)
	if err != nil {
		if errors.Is(err, outputduplication.ErrNoImageYet) {
			if s.haveLast && len(dst) >= len(s.lastBGRA) {
				copy(dst, s.lastBGRA)
				return nil
			}
			// One more brief wait — first frame after Open can be slow.
			err = s.ddup.GetImage(s.rgba, timeoutMs*2+50)
			if err != nil {
				if errors.Is(err, outputduplication.ErrNoImageYet) && s.haveLast {
					copy(dst, s.lastBGRA)
					return nil
				}
				return fmt.Errorf("%w: DXGI no frame yet: %v", ErrBackendUnavailable, err)
			}
		} else {
			return fmt.Errorf("%w: DXGI GetImage: %v", ErrBackendUnavailable, err)
		}
	}

	// GetImage normalizes to RGBA; our pipeline expects BGRA.
	if w == s.nativeW && h == s.nativeH {
		rgbaToBGRA(dst, s.rgba.Pix, w, h, s.rgba.Stride)
	} else {
		scaleRGBAToBGRA(dst, s.rgba.Pix, s.nativeW, s.nativeH, s.rgba.Stride, w, h)
	}
	copy(s.lastBGRA, dst[:w*h*4])
	s.haveLast = true
	return nil
}

func rgbaToBGRA(dst, src []byte, w, h, srcStride int) {
	rowBytes := w * 4
	for y := 0; y < h; y++ {
		si := y * srcStride
		di := y * rowBytes
		for x := 0; x < w; x++ {
			i, j := si+x*4, di+x*4
			dst[j+0] = src[i+2] // B
			dst[j+1] = src[i+1] // G
			dst[j+2] = src[i+0] // R
			dst[j+3] = src[i+3] // A
		}
	}
}

func scaleRGBAToBGRA(dst, src []byte, sw, sh, srcStride, dw, dh int) {
	for y := 0; y < dh; y++ {
		sy := y * sh / dh
		for x := 0; x < dw; x++ {
			sx := x * sw / dw
			si := sy*srcStride + sx*4
			di := (y*dw + x) * 4
			dst[di+0] = src[si+2]
			dst[di+1] = src[si+1]
			dst[di+2] = src[si+0]
			dst[di+3] = src[si+3]
		}
	}
}

func (s *dxgiSession) close() {
	if s == nil {
		return
	}
	if s.ddup != nil {
		s.ddup.Release()
		s.ddup = nil
	}
	if s.deviceCtx != nil {
		s.deviceCtx.Release()
		s.deviceCtx = nil
	}
	if s.device != nil {
		s.device.Release()
		s.device = nil
	}
	s.rgba = nil
	s.lastBGRA = nil
	s.haveLast = false
	if s.locked {
		runtime.UnlockOSThread()
		s.locked = false
	}
}
