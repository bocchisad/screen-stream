package streamer

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salfetka/screen-stream/engine"
)

func TestWebRTCLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var got atomic.Uint64
	rec := NewFrameReconstructor(1)
	sender, receiver, err := ConnectLoopbackOpts(ctx, SessionConfig{
		Compress: true,
		OnSessionReady: func(seed uint64) { rec.SetSeed(seed) },
		OnPacket: func(b []byte) {
			_ = rec.Apply(b)
			got.Add(1)
		},
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sender.Stop()
	defer receiver.Stop()

	seed := sender.SessionSeed()
	if seed == 0 || seed != receiver.SessionSeed() {
		t.Fatalf("session seeds mismatch sender=%x recv=%x", seed, receiver.SessionSeed())
	}

	pipe := engine.NewPipeline(engine.PipelineConfig{
		Width: 128, Height: 128, BlockSize: 32,
		XORSeed: seed,
	})
	defer pipe.Close()

	buf := make([]byte, 128*128*4)
	for i := 0; i < 5; i++ {
		for j := range buf {
			buf[j] = byte(i + j)
		}
		fr := &engine.Frame{
			Meta:   engine.FrameMeta{FrameID: uint64(i + 1), Width: 128, Height: 128, Stride: 128 * 4},
			Pixels: buf,
		}
		pkt, err := pipe.Process(ctx, fr)
		if err != nil {
			t.Fatal(err)
		}
		sender.TryEnqueue(pkt)
	}

	deadline := time.Now().Add(10 * time.Second)
	for (got.Load() < 5 || sender.Sent() < 5) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got.Load() < 4 {
		t.Fatalf("received %d/5 packets (sent=%d)", got.Load(), sender.Sent())
	}
	if cw, ch := rec.Size(); cw != 128 || ch != 128 {
		t.Fatalf("canvas size %dx%d", cw, ch)
	}
}

func TestWebRTCHighResChunked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const w, h = 1920, 1080
	var got atomic.Uint64
	rec := NewFrameReconstructor(1)
	sender, receiver, err := ConnectLoopbackOpts(ctx, SessionConfig{
		Compress:  true,
		ChunkSize: DefaultChunkSize,
		OnSessionReady: func(seed uint64) { rec.SetSeed(seed) },
		OnPacket: func(b []byte) {
			if err := rec.Apply(b); err == nil {
				got.Add(1)
			}
		},
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sender.Stop()
	defer receiver.Stop()

	seed := sender.SessionSeed()

	pipe := engine.NewPipeline(engine.PipelineConfig{
		Width: w, Height: h, BlockSize: 64,
		XORSeed: seed,
	})
	defer pipe.Close()

	buf := make([]byte, w*h*4)
	for i := range buf {
		buf[i] = byte(i)
	}
	fr := &engine.Frame{
		Meta:   engine.FrameMeta{FrameID: 1, Width: w, Height: h, Stride: w * 4},
		Pixels: buf,
	}
	pkt, err := pipe.Process(ctx, fr)
	if err != nil {
		t.Fatal(err)
	}
	sender.TryEnqueue(pkt)

	deadline := time.Now().Add(15 * time.Second)
	for got.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got.Load() < 1 {
		t.Fatalf("no FHD packet received (sent=%d)", sender.Sent())
	}
	if cw, ch := rec.Size(); cw != w || ch != h {
		t.Fatalf("canvas %dx%d want %dx%d", cw, ch, w, h)
	}
}
