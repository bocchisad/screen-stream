package engine_test

import (
	"context"
	"testing"

	"github.com/salfetka/screen-stream/engine"
)

func BenchmarkPipelineAllocs(b *testing.B) {
	const w, h, block = 1280, 720, 64
	p := engine.NewPipeline(engine.PipelineConfig{Width: w, Height: h, BlockSize: block})
	defer p.Close()
	buf := make([]byte, w*h*4)
	for i := range buf {
		buf[i] = byte(i)
	}
	ctx := context.Background()
	// warmup
	for i := 0; i < 5; i++ {
		meta := engine.FrameMeta{FrameID: uint64(i + 1), Width: w, Height: h, Stride: w * 4}
		fr := &engine.Frame{Meta: meta, Pixels: buf}
		pkt, _ := p.Process(ctx, fr)
		if pkt != nil {
			pkt.Release()
		}
		for j := range buf {
			buf[j] = byte(i + j)
		}
	}
	allocs := testing.AllocsPerRun(50, func() {
		meta := engine.FrameMeta{FrameID: 100, Width: w, Height: h, Stride: w * 4}
		fr := &engine.Frame{Meta: meta, Pixels: buf}
		pkt, err := p.Process(ctx, fr)
		if err != nil {
			b.Fatal(err)
		}
		pkt.Release()
	})
	b.ReportMetric(allocs, "allocs/op")
	b.Logf("allocs/op = %v", allocs)
}
