//go:build darwin && cgo

package engine

import (
	"context"
	"testing"
	"time"
)

func TestVideoToolboxEncodeDecodeRoundTrip(t *testing.T) {
	const w, h = 320, 192 // 16-aligned (180 breaks H.264 MB grid)
	enc := NewHardwareEncoder()
	if err := enc.Open(EncoderConfig{Width: w, Height: h, FPS: 30, Bitrate: 800_000, KeyframeEvery: 30}); err != nil {
		t.Skipf("VT HW encoder unavailable: %v", err)
	}
	defer enc.Close()

	dec := NewHardwareDecoder()
	if err := dec.Open(w, h); err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	bgra := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			bgra[i+0] = byte(x)
			bgra[i+1] = byte(y)
			bgra[i+2] = 40
			bgra[i+3] = 255
		}
	}

	ctx := context.Background()
	var gotKey bool
	var decoded bool
	var ausTotal, decodeAttempts int
	for i := 0; i < 60; i++ {
		// Change pixels so encoder emits dirty frames.
		bgra[0] = byte(i)
		bgra[4] = byte(i * 3)
		aus, err := enc.Encode(ctx, bgra, w*4, time.Duration(i)*time.Millisecond*33)
		if err != nil {
			t.Fatal(err)
		}
		ausTotal += len(aus)
		for _, au := range aus {
			if au.Keyframe {
				gotKey = true
			}
			decodeAttempts++
			out, ow, oh, _, err := dec.Decode(ctx, au.Data)
			if err != nil {
				t.Fatalf("decode: %v (au_len=%d key=%v)", err, len(au.Data), au.Keyframe)
			}
			if out != nil && ow > 0 && oh > 0 {
				decoded = true
			}
		}
	}
	if !gotKey {
		t.Fatalf("expected IDR; aus_total=%d", ausTotal)
	}
	if !decoded {
		t.Fatalf("expected decoded frame; aus_total=%d decode_attempts=%d", ausTotal, decodeAttempts)
	}
}

func TestParseVideoCodec(t *testing.T) {
	c, err := ParseVideoCodec("h264")
	if err != nil || c != CodecH264 {
		t.Fatal(c, err)
	}
	c, err = ParseVideoCodec("dirty")
	if err != nil || c != CodecDirtyPacket {
		t.Fatal(c, err)
	}
}
