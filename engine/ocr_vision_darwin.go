//go:build darwin && cgo

package engine

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -I${SRCDIR}/native
#cgo LDFLAGS: -framework Foundation -framework Vision -framework CoreGraphics -framework AppKit
#include "vision_ocr.h"
#include <stdlib.h>
*/
import "C"
import (
	"context"
	"unsafe"
)

// VisionOCRBackend uses Apple Vision (VNRecognizeTextRequest) on-device.
type VisionOCRBackend struct {
	MaxLines int
}

func NewVisionOCRBackend() *VisionOCRBackend {
	return &VisionOCRBackend{MaxLines: 64}
}

func (v *VisionOCRBackend) ScanFrame(ctx context.Context, meta FrameMeta, bgra []byte) ([]OCRLine, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if len(bgra) == 0 || meta.Width == 0 || meta.Height == 0 {
		return nil, nil
	}
	stride := meta.Stride
	if stride == 0 {
		stride = int(meta.Width) * 4
	}
	need := stride * int(meta.Height)
	if need <= 0 || len(bgra) < need {
		return nil, nil
	}
	max := v.MaxLines
	if max <= 0 {
		max = 64
	}
	rects := make([]C.ss_ocr_rect, max)
	texts := make([]*C.char, max)
	n := int(C.ss_vision_ocr(
		(*C.uint8_t)(unsafe.Pointer(&bgra[0])),
		C.int(meta.Width), C.int(meta.Height), C.int(stride),
		&rects[0], &texts[0], C.int(max),
	))
	if n <= 0 {
		return nil, nil
	}
	out := make([]OCRLine, 0, n)
	for i := 0; i < n; i++ {
		text := ""
		if texts[i] != nil {
			text = C.GoString(texts[i])
			C.free(unsafe.Pointer(texts[i]))
		}
		out = append(out, OCRLine{
			Text: text,
			Rect: Rect{
				X: uint16(rects[i].x),
				Y: uint16(rects[i].y),
				W: uint16(rects[i].w),
				H: uint16(rects[i].h),
			},
		})
	}
	return out, nil
}

var _ OCRBackend = (*VisionOCRBackend)(nil)
