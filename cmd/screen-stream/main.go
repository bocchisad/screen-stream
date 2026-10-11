package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync/atomic"
	"time"

	"github.com/salfetka/screen-stream/engine"
	"github.com/salfetka/screen-stream/streamer"
)

func main() {
	seconds := flag.Float64("seconds", 3, "benchmark duration")
	fps := flag.Int("fps", 60, "target FPS")
	width := flag.Int("width", 1920, "synthetic width (default FHD)")
	height := flag.Int("height", 1080, "synthetic height (default FHD)")
	block := flag.Int("block", 64, "diff block size")
	synthetic := flag.Bool("synthetic", true, "use synthetic frames (no OS capture)")
	pii := flag.Bool("pii", true, "enable pattern redaction detector")
	useWebRTC := flag.Bool("webrtc", false, "pion DataChannel loopback + reconstruct")
	compress := flag.Bool("compress", true, "zlib-compress DirtyPacket payloads on WebRTC")
	scale := flag.Int("scale", 100, "capture scale percent (100 = native Retina pixels)")
	maxW := flag.Int("max-width", 0, "cap capture width (0 = unlimited)")
	maxH := flag.Int("max-height", 0, "cap capture height (0 = unlimited)")
	snapshot := flag.String("snapshot", "", "write reconstructed PNG (webrtc mode)")
	exclude := flag.String("exclude", "", "comma-separated bundle IDs to exclude (macOS)")
	flag.Parse()

	fmt.Printf("screen-stream  go=%s  os=%s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*seconds*float64(time.Second)))
	defer cancel()

	var grabber engine.Grabber
	if *synthetic {
		grabber = engine.NewSyntheticGrabber(*width, *height, *fps)
	} else {
		grabber = engine.NewGrabber()
	}

	cfg := engine.CaptureConfig{
		FPS:          *fps,
		BlockSize:    uint16(*block),
		ScalePercent: *scale,
		MaxWidth:     *maxW,
		MaxHeight:    *maxH,
	}
	if *exclude != "" {
		cfg.ExcludeBundleIDs = splitCSV(*exclude)
	}

	if err := grabber.Open(ctx, cfg); err != nil {
		var pe *engine.PermissionError
		if errors.As(err, &pe) || errors.Is(err, engine.ErrPermissionDenied) {
			fmt.Fprintf(os.Stderr, "permission denied: %v\n", err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "open grabber (%s): %v\n", grabber.Backend(), err)
		os.Exit(1)
	}
	defer grabber.Close()

	// Probe geometry — allow up to 8K for live.
	maxProbeW, maxProbeH := *width, *height
	if !*synthetic {
		maxProbeW, maxProbeH = 7680, 4320
	}
	probe := make([]byte, maxProbeW*maxProbeH*4)
	meta, err := grabber.Capture(ctx, probe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe capture: %v\n", err)
		os.Exit(1)
	}
	*width, *height = int(meta.Width), int(meta.Height)
	stride := meta.Stride
	if stride <= 0 {
		stride = (*width) * 4
	}
	fmt.Printf("backend=%s  geometry=%dx%d  stride=%d  scale=%d%%\n",
		grabber.Backend(), *width, *height, stride, *scale)

	var detector engine.PIIDetector
	if *pii {
		pd := engine.NewPatternDetector()
		pd.SetHits([]engine.Rect{{X: 0, Y: 0, W: uint16(*block), H: uint16(*block)}})
		detector = pd
	}

	pipe := engine.NewPipeline(engine.PipelineConfig{
		Width:     *width,
		Height:    *height,
		Stride:    stride,
		BlockSize: *block,
		Detector:  detector,
		XORSeed:   engine.DefaultXORSeed,
	})
	defer pipe.Close()

	var (
		sink        streamer.Sink
		recvCount   atomic.Uint64
		reconFrames atomic.Uint64
		senderRTC   *streamer.WebRTCSession
		receiverRTC *streamer.WebRTCSession
		rec         *streamer.FrameReconstructor
	)

	if *useWebRTC {
		rec = streamer.NewFrameReconstructor(engine.DefaultXORSeed)
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 45*time.Second)
		senderRTC, receiverRTC, err = streamer.ConnectLoopbackOpts(dialCtx, streamer.SessionConfig{
			RingDepth: 4,
			Compress:  *compress,
			OnPacket: func(b []byte) {
				recvCount.Add(1)
				if err := rec.Apply(b); err == nil {
					reconFrames.Add(1)
				}
			},
		})
		dialCancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "webrtc: %v\n", err)
			os.Exit(1)
		}
		defer senderRTC.Stop()
		defer receiverRTC.Stop()
		sink = senderRTC
		fmt.Printf("webrtc: loopback open (compress=%v chunk=%dKiB)\n", *compress, streamer.DefaultChunkSize/1024)
	} else {
		local := streamer.NewLocalSink(4, nil)
		_ = local.Start(ctx)
		defer local.Stop()
		sink = local
	}

	pool := engine.NewBufferPool(stride * (*height))
	var fr engine.Frame

	warmBuf := pool.Acquire()
	for i := 0; i < 5; i++ {
		m, err := grabber.Capture(ctx, warmBuf)
		if err != nil {
			break
		}
		fr = engine.Frame{Meta: m, Pixels: warmBuf}
		pkt, err := pipe.Process(ctx, &fr)
		if err == nil {
			sink.TryEnqueue(pkt)
		}
	}
	pool.Release(warmBuf)

	samples := make([]time.Duration, 0, int(*seconds*float64(*fps))+16)
	var frames int
	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	buf := pool.Acquire()
	defer pool.Release(buf)

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		default:
		}
		m, err := grabber.Capture(ctx, buf)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				break
			}
			// Transient "no frame" under high-res load — skip, don't abort the run.
			if errors.Is(err, engine.ErrBackendUnavailable) {
				continue
			}
			fmt.Fprintf(os.Stderr, "capture: %v\n", err)
			break
		}
		fr = engine.Frame{Meta: m, Pixels: buf}
		t0 := time.Now()
		pkt, err := pipe.Process(ctx, &fr)
		dt := time.Since(t0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pipeline: %v\n", err)
			break
		}
		sink.TryEnqueue(pkt)
		samples = append(samples, dt)
		frames++
	}

	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	runtime.ReadMemStats(&memAfter)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })

	fmt.Printf("frames=%d  dropped=%d  sent=%d\n", frames, sink.Dropped(), sink.Sent())
	if *useWebRTC {
		fmt.Printf("webrtc_recv=%d  reconstructed=%d\n", recvCount.Load(), reconFrames.Load())
		if *snapshot != "" && rec != nil {
			if err := rec.WritePNG(*snapshot); err != nil {
				fmt.Fprintf(os.Stderr, "snapshot: %v\n", err)
			} else {
				cw, ch := rec.Size()
				fmt.Printf("snapshot=%s  %dx%d\n", *snapshot, cw, ch)
			}
		}
	}
	if len(samples) > 0 {
		p50 := samples[len(samples)*50/100]
		p99 := samples[len(samples)*99/100]
		fmt.Printf("latency_us  p50=%d  p99=%d  max=%d\n",
			p50.Microseconds(), p99.Microseconds(), samples[len(samples)-1].Microseconds())
		if p99 > 10*time.Millisecond {
			fmt.Printf("warn: p99 %v exceeds 10ms target (high-res load)\n", p99)
		} else {
			fmt.Printf("ok: p99 under 10ms target\n")
		}
	}
	mallocs := memAfter.Mallocs - memBefore.Mallocs
	bytes := memAfter.TotalAlloc - memBefore.TotalAlloc
	var perFrame float64
	if frames > 0 {
		perFrame = float64(mallocs) / float64(frames)
	}
	fmt.Printf("heap_mallocs=%d  (~%.2f/frame)  total_alloc_bytes=%d\n", mallocs, perFrame, bytes)
	fmt.Println(streamer.DescribeHandoff())
}

func splitCSV(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := s[start:i]
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}
