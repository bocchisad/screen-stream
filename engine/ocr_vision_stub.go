//go:build !darwin || !cgo

package engine

import (
	"context"
	"fmt"
)

// NewVisionOCRBackend is unavailable off darwin/cgo.
func NewVisionOCRBackend() OCRBackend {
	return unavailableVision{}
}

type unavailableVision struct{}

func (unavailableVision) ScanFrame(ctx context.Context, meta FrameMeta, bgra []byte) ([]OCRLine, error) {
	_ = ctx
	_ = meta
	_ = bgra
	return nil, fmt.Errorf("%w: Vision OCR requires darwin+cgo", ErrNotSupported)
}
