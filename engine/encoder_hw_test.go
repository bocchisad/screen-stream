package engine

import (
	"context"
	"testing"
	"time"
)

func TestHardwareEncoderOpenOrSkip(t *testing.T) {
	enc := NewHardwareEncoder()
	t.Logf("backend=%s", enc.Backend())
	err := enc.Open(EncoderConfig{Width: 320, Height: 192, FPS: 30, Bitrate: 800_000, KeyframeEvery: 30})
	if err != nil {
		t.Skipf("HW encoder unavailable (expected on hosts without GPU encode): %v", err)
	}
	defer enc.Close()

	bgra := make([]byte, 320*192*4)
	for i := range bgra {
		bgra[i] = byte(i)
	}
	ctx := context.Background()
	var total int
	for i := 0; i < 10; i++ {
		aus, err := enc.Encode(ctx, bgra, 320*4, time.Duration(i)*33*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		total += len(aus)
		for _, au := range aus {
			if len(au.Data) == 0 {
				t.Fatal("empty AU")
			}
		}
	}
	if total == 0 {
		t.Fatal("expected at least one encoded AU")
	}
}
