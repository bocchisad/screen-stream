//go:build !(darwin && cgo) && !(linux && cgo) && !(windows && cgo)

package engine

import (
	"context"
	"fmt"
	"time"
)

type unsupportedEncoder struct{}

func NewHardwareEncoder() VideoEncoder { return unsupportedEncoder{} }

func (unsupportedEncoder) Backend() string { return "none" }

func (unsupportedEncoder) Open(cfg EncoderConfig) error {
	_ = cfg
	return fmt.Errorf("%w: hardware H.264 encode requires platform HW (VideoToolbox/VAAPI/MediaFoundation; no software fallback)", ErrNotSupported)
}

func (unsupportedEncoder) Encode(ctx context.Context, bgra []byte, stride int, pts time.Duration) ([]EncodedFrame, error) {
	_ = ctx
	_ = bgra
	_ = stride
	_ = pts
	return nil, ErrNotSupported
}

func (unsupportedEncoder) Close() error { return nil }

type unsupportedDecoder struct{}

func NewHardwareDecoder() VideoDecoder { return unsupportedDecoder{} }

func (unsupportedDecoder) Backend() string { return "none" }

func (unsupportedDecoder) Open(maxW, maxH int) error {
	_ = maxW
	_ = maxH
	return fmt.Errorf("%w: hardware H.264 decode requires platform HW (VideoToolbox/VAAPI/MediaFoundation; no software fallback)", ErrNotSupported)
}

func (unsupportedDecoder) Decode(ctx context.Context, annexB []byte) (bgra []byte, width, height, stride int, err error) {
	_ = ctx
	_ = annexB
	return nil, 0, 0, 0, ErrNotSupported
}

func (unsupportedDecoder) Close() error { return nil }

func HardwareEncoderAvailable() bool { return false }
