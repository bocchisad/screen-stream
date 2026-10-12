//go:build darwin && cgo

package engine

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -I${SRCDIR}/native
#cgo LDFLAGS: -framework VideoToolbox -framework CoreMedia -framework CoreVideo -framework Foundation -framework CoreFoundation
#include "vt_encode.h"
#include <stdlib.h>
*/
import "C"
import (
	"context"
	"fmt"
	"time"
	"unsafe"
)

type vtEncoder struct {
	enc    *C.ss_vt_enc
	cfg    EncoderConfig
	frameN int
}

func NewHardwareEncoder() VideoEncoder { return &vtEncoder{} }

func (e *vtEncoder) Backend() string { return "videotoolbox-hw" }

func align16(n int) int {
	if n < 16 {
		return 16
	}
	return (n + 15) &^ 15
}

func (e *vtEncoder) Open(cfg EncoderConfig) error {
	if cfg.Width < 16 || cfg.Height < 16 {
		return fmt.Errorf("%w: encoder size", ErrNotSupported)
	}
	// H.264 macroblocks are 16×16; VT rejects/mis-decodes odd heights like 180.
	cfg.Width = align16(cfg.Width)
	cfg.Height = align16(cfg.Height)
	if cfg.FPS <= 0 {
		cfg.FPS = 30
	}
	if cfg.Bitrate <= 0 {
		cfg.Bitrate = DefaultH264Bitrate(cfg.Width, cfg.Height, cfg.FPS)
	}
	enc := C.ss_vt_enc_create(C.int(cfg.Width), C.int(cfg.Height), C.int(cfg.FPS), C.int(cfg.Bitrate), C.int(cfg.KeyframeEvery))
	if enc == nil {
		return fmt.Errorf("%w: VideoToolbox HW H.264 encoder unavailable (RequireHardwareAcceleratedVideoEncoder)", ErrBackendUnavailable)
	}
	e.enc = enc
	e.cfg = cfg
	return nil
}

func (e *vtEncoder) Encode(ctx context.Context, bgra []byte, stride int, pts time.Duration) ([]EncodedFrame, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if e.enc == nil {
		return nil, ErrStopped
	}
	need := e.cfg.Height * stride
	if stride <= 0 {
		stride = e.cfg.Width * 4
		need = e.cfg.Height * stride
	}
	if len(bgra) < need {
		return nil, ErrBufferTooSmall
	}
	e.frameN++
	var aus [4]C.ss_vt_au
	var n C.int
	rc := C.ss_vt_enc_encode(e.enc, (*C.uint8_t)(unsafe.Pointer(&bgra[0])), C.int(stride), C.int64_t(pts.Microseconds()), &aus[0], 4, &n)
	if rc < 0 {
		return nil, fmt.Errorf("%w: VT encode rc=%d", ErrBackendUnavailable, int(rc))
	}
	out := make([]EncodedFrame, 0, int(n))
	for i := 0; i < int(n); i++ {
		au := aus[i]
		b := C.GoBytes(unsafe.Pointer(au.data), au.len)
		C.ss_vt_au_free(&aus[i])
		dur := time.Second / time.Duration(e.cfg.FPS)
		out = append(out, EncodedFrame{
			Data:     b,
			PTS:      time.Duration(au.pts_us) * time.Microsecond,
			Duration: dur,
			Keyframe: au.keyframe != 0,
			Width:    e.cfg.Width,
			Height:   e.cfg.Height,
		})
	}
	return out, nil
}

func (e *vtEncoder) Close() error {
	if e.enc != nil {
		C.ss_vt_enc_destroy(e.enc)
		e.enc = nil
	}
	return nil
}

type vtDecoder struct {
	dec *C.ss_vt_dec
}

func NewHardwareDecoder() VideoDecoder { return &vtDecoder{} }

func (d *vtDecoder) Backend() string { return "videotoolbox-hw" }

func (d *vtDecoder) Open(maxW, maxH int) error {
	dec := C.ss_vt_dec_create(C.int(maxW), C.int(maxH))
	if dec == nil {
		return fmt.Errorf("%w: VideoToolbox decoder", ErrBackendUnavailable)
	}
	d.dec = dec
	return nil
}

func (d *vtDecoder) Decode(ctx context.Context, annexB []byte) (bgra []byte, width, height, stride int, err error) {
	select {
	case <-ctx.Done():
		return nil, 0, 0, 0, ctx.Err()
	default:
	}
	if d.dec == nil {
		return nil, 0, 0, 0, ErrStopped
	}
	if len(annexB) == 0 {
		return nil, 0, 0, 0, nil
	}
	var out *C.uint8_t
	var w, h, s C.int
	rc := C.ss_vt_dec_decode(d.dec, (*C.uint8_t)(unsafe.Pointer(&annexB[0])), C.int(len(annexB)), &out, &w, &h, &s)
	if rc < 0 {
		return nil, 0, 0, 0, fmt.Errorf("%w: VT decode rc=%d", ErrBackendUnavailable, int(rc))
	}
	if rc > 0 || out == nil {
		return nil, 0, 0, 0, nil
	}
	n := int(s) * int(h)
	bgra = C.GoBytes(unsafe.Pointer(out), C.int(n))
	C.ss_vt_buf_free(out)
	return bgra, int(w), int(h), int(s), nil
}

func (d *vtDecoder) Close() error {
	if d.dec != nil {
		C.ss_vt_dec_destroy(d.dec)
		d.dec = nil
	}
	return nil
}

// HardwareEncoderAvailable is true when VideoToolbox HW encode can be created.
func HardwareEncoderAvailable() bool {
	e := NewHardwareEncoder()
	err := e.Open(EncoderConfig{Width: 320, Height: 192, FPS: 30, Bitrate: 500_000})
	_ = e.Close()
	return err == nil
}
