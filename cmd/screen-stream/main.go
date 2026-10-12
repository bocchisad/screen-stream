package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/salfetka/screen-stream/engine"
	"github.com/salfetka/screen-stream/streamer"
)

func main() {
	mode := flag.String("mode", "bench", "bench | hub | send | recv | loopback | doctor")
	signalAddr := flag.String("signal", "127.0.0.1:8787", "signaling listen/dial host:port")
	seconds := flag.Float64("seconds", 3, "run duration (bench/send/recv/loopback)")
	fps := flag.Int("fps", 60, "target FPS")
	width := flag.Int("width", 1920, "synthetic width")
	height := flag.Int("height", 1080, "synthetic height")
	block := flag.Int("block", 64, "diff block size")
	synthetic := flag.Bool("synthetic", true, "synthetic frames (send/bench/loopback)")
	pii := flag.Bool("pii", false, "enable PII pattern redaction (demo corner hit when no OCR)")
	ocr := flag.Bool("ocr", false, "use OCRDetector (Vision on macOS)")
	compress := flag.Bool("compress", true, "zlib payloads on WebRTC")
	scale := flag.Int("scale", 100, "capture scale percent (100 = Retina pixels)")
	maxW := flag.Int("max-width", 0, "cap capture width")
	maxH := flag.Int("max-height", 0, "cap capture height")
	snapshot := flag.String("snapshot", "", "write reconstructed PNG (recv/loopback)")
	preview := flag.String("preview", "", "MJPEG preview listen addr for recv/loopback (e.g. 127.0.0.1:8090)")
	exclude := flag.String("exclude", "", "macOS bundle IDs to exclude")
	xorSeedFlag := flag.String("xor-seed", "", "protect seed (decimal or 0xHEX); else env SCREEN_STREAM_XOR_SEED")
	// legacy alias
	webrtcAlias := flag.Bool("webrtc", false, "alias for -mode=loopback")
	flag.Parse()

	if *webrtcAlias && *mode == "bench" {
		*mode = "loopback"
	}

	seed := engine.DefaultXORSeed
	if *xorSeedFlag != "" {
		n, err := engine.ParseXORSeed(*xorSeedFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		seed = n
	} else {
		seed = engine.ResolveXORSeed(0)
	}

	fmt.Printf("screen-stream  go=%s  os=%s/%s  mode=%s  xor_seed=0x%X\n",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, *mode, seed)

	switch *mode {
	case "hub":
		runHub(*signalAddr)
	case "doctor":
		runDoctor(*signalAddr)
	case "bench":
		runSession(sessionOpts{
			seconds: *seconds, fps: *fps, width: *width, height: *height, block: *block,
			synthetic: *synthetic, pii: *pii, ocr: *ocr, compress: *compress,
			scale: *scale, maxW: *maxW, maxH: *maxH, exclude: *exclude, seed: seed,
			transport: "local",
		})
	case "loopback":
		runSession(sessionOpts{
			seconds: *seconds, fps: *fps, width: *width, height: *height, block: *block,
			synthetic: *synthetic, pii: *pii, ocr: *ocr, compress: *compress,
			scale: *scale, maxW: *maxW, maxH: *maxH, exclude: *exclude, seed: seed,
			snapshot: *snapshot, preview: *preview, transport: "loopback",
		})
	case "send":
		runSession(sessionOpts{
			seconds: *seconds, fps: *fps, width: *width, height: *height, block: *block,
			synthetic: *synthetic, pii: *pii, ocr: *ocr, compress: *compress,
			scale: *scale, maxW: *maxW, maxH: *maxH, exclude: *exclude, seed: seed,
			transport: "send", signalURL: "http://" + *signalAddr,
		})
	case "recv":
		runSession(sessionOpts{
			seconds: *seconds, fps: *fps, width: *width, height: *height, block: *block,
			synthetic: true, // recv does not capture
			compress: *compress, seed: seed, snapshot: *snapshot, preview: *preview,
			transport: "recv", signalURL: "http://" + *signalAddr,
		})
	default:
		fmt.Fprintf(os.Stderr, "unknown -mode %q\n", *mode)
		os.Exit(1)
	}
}

func runHub(addr string) {
	hub := streamer.NewHTTPSignalingHub()
	url, err := hub.ListenAndServe(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hub: %v\n", err)
		os.Exit(1)
	}
	clientAddr := "127.0.0.1:8787"
	if host, port, err := splitHostPort(addr); err == nil {
		if host == "0.0.0.0" || host == "::" || host == "" {
			clientAddr = "127.0.0.1:" + port
		} else {
			clientAddr = host + ":" + port
		}
	}
	fmt.Printf("signaling hub listening on %s\n", url)
	fmt.Println("keep this terminal open, then in TWO other terminals:")
	fmt.Printf("  send: go run ./cmd/screen-stream -mode=send -signal=%s -synthetic=false -scale=75 -seconds=30\n", clientAddr)
	fmt.Printf("  recv: go run ./cmd/screen-stream -mode=recv -signal=%s -seconds=30 -snapshot=/tmp/recv.png\n", clientAddr)
	select {}
}

func runDoctor(signalAddr string) {
	fmt.Println("screen-stream doctor")
	fmt.Printf("  go=%s  os=%s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  xor_seed default=0x%X  env %s=%q\n", engine.DefaultXORSeed, engine.EnvXORSeedKey, os.Getenv(engine.EnvXORSeedKey))

	g := engine.NewGrabber()
	fmt.Printf("  grabber backend=%s\n", g.Backend())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := g.Open(ctx, engine.CaptureConfig{FPS: 30, ScalePercent: 50, BlockSize: 64})
	if err != nil {
		fmt.Printf("  capture open: FAIL %v\n", err)
	} else {
		buf := make([]byte, 4096*2304*4)
		meta, cerr := g.Capture(ctx, buf)
		_ = g.Close()
		if cerr != nil {
			fmt.Printf("  capture frame: FAIL %v\n", cerr)
		} else {
			fmt.Printf("  capture frame: OK %dx%d stride=%d\n", meta.Width, meta.Height, meta.Stride)
		}
	}

	base := "http://" + signalAddr
	res, err := httpGetStatus(base)
	if err != nil {
		fmt.Printf("  hub %s: DOWN (%v)\n", base, err)
		fmt.Println("  hint: go run ./cmd/screen-stream -mode=hub -signal=127.0.0.1:8787")
	} else {
		fmt.Printf("  hub %s: UP %s\n", base, res)
	}
	fmt.Println("  preview URL (while recv runs): http://127.0.0.1:8090/")
}

func httpGetStatus(base string) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	res, err := client.Get(base + "/status")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return strings.TrimSpace(string(b)), nil
}

func splitHostPort(addr string) (host, port string, err error) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i], addr[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("no port")
}

type sessionOpts struct {
	seconds                 float64
	fps, width, height      int
	block                   int
	synthetic, pii, ocr     bool
	compress                bool
	scale, maxW, maxH       int
	exclude                 string
	seed                    uint64
	snapshot                string
	preview                 string // MJPEG listen addr
	transport               string // local | loopback | send | recv
	signalURL               string
}

func runSession(o sessionOpts) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(o.seconds*float64(time.Second)))
	defer cancel()

	var (
		sink        streamer.Sink
		recvCount   atomic.Uint64
		reconFrames atomic.Uint64
		rec         *streamer.FrameReconstructor
		rtc         *streamer.WebRTCSession
		rtcPeer     *streamer.WebRTCSession
	)

	switch o.transport {
	case "local":
		local := streamer.NewLocalSink(4, nil)
		_ = local.Start(ctx)
		defer local.Stop()
		sink = local

	case "loopback":
		rec = streamer.NewFrameReconstructor(o.seed)
		prev := startPreview(o.preview)
		if prev != nil {
			defer prev.Close()
		}
		var lastPrev time.Time
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 45*time.Second)
		var err error
		rtc, rtcPeer, err = streamer.ConnectLoopbackOpts(dialCtx, streamer.SessionConfig{
			RingDepth: 4,
			Compress:  o.compress,
			OnPacket: func(b []byte) {
				recvCount.Add(1)
				if err := rec.Apply(b); err == nil {
					reconFrames.Add(1)
					maybePreview(prev, rec, &lastPrev)
				}
			},
		})
		dialCancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "webrtc loopback: %v\n", err)
			os.Exit(1)
		}
		defer rtc.Stop()
		defer rtcPeer.Stop()
		sink = rtc
		fmt.Printf("webrtc: loopback (compress=%v)\n", o.compress)

	case "send":
		sig := streamer.NewHTTPSignalingClient(o.signalURL)
		rtc = streamer.NewWebRTCSession(streamer.SessionConfig{RingDepth: 4, Compress: o.compress})
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := rtc.DialOfferer(dialCtx, sig); err != nil {
			dialCancel()
			fmt.Fprintf(os.Stderr, "dial offer: %v\n", err)
			fmt.Fprintf(os.Stderr, "hint: hub must be running, and recv should be started around the same time:\n")
			fmt.Fprintf(os.Stderr, "  go run ./cmd/screen-stream -mode=hub -signal=127.0.0.1:8787\n")
			fmt.Fprintf(os.Stderr, "  go run ./cmd/screen-stream -mode=recv -signal=127.0.0.1:8787 -preview=127.0.0.1:8090 -xor-seed=...\n")
			os.Exit(1)
		}
		dialCancel()
		_ = rtc.StartSendLoop(context.Background())
		defer rtc.Stop()
		sink = rtc
		fmt.Printf("webrtc: send → %s\n", o.signalURL)

	case "recv":
		rec = streamer.NewFrameReconstructor(o.seed)
		prev := startPreview(o.preview)
		if prev != nil {
			defer prev.Close()
		}
		var lastPrev time.Time
		sig := streamer.NewHTTPSignalingClient(o.signalURL)
		rtc = streamer.NewWebRTCSession(streamer.SessionConfig{
			RingDepth: 4,
			Compress:  o.compress,
			OnPacket: func(b []byte) {
				recvCount.Add(1)
				if err := rec.Apply(b); err == nil {
					reconFrames.Add(1)
					maybePreview(prev, rec, &lastPrev)
				}
			},
		})
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := rtc.DialAnswerer(dialCtx, sig); err != nil {
			dialCancel()
			fmt.Fprintf(os.Stderr, "dial answer: %v\n", err)
			fmt.Fprintf(os.Stderr, "hint: need THREE terminals, same xor-seed:\n")
			fmt.Fprintf(os.Stderr, "  1) go run ./cmd/screen-stream -mode=hub -signal=127.0.0.1:8787\n")
			fmt.Fprintf(os.Stderr, "  2) go run ./cmd/screen-stream -mode=send -signal=127.0.0.1:8787 -synthetic=false -seconds=30 -xor-seed=0xC0FFEE42D00D\n")
			fmt.Fprintf(os.Stderr, "  3) go run ./cmd/screen-stream -mode=recv -signal=127.0.0.1:8787 -seconds=30 -preview=127.0.0.1:8090 -xor-seed=0xC0FFEE42D00D\n")
			fmt.Fprintf(os.Stderr, "start send+recv while hub is up; if stuck, restart hub.\n")
			os.Exit(1)
		}
		dialCancel()
		defer rtc.Stop()
		fmt.Printf("webrtc: recv ← %s\n", o.signalURL)
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond)
		fmt.Printf("webrtc_recv=%d  reconstructed=%d\n", recvCount.Load(), reconFrames.Load())
		mbps := float64(rtc.BytesRecv()) * 8 / maxFloat(o.seconds, 1) / 1e6
		fmt.Printf("wire_bytes_recv=%d  (~%.2f Mbit/s avg)\n", rtc.BytesRecv(), mbps)
		if o.snapshot != "" && rec != nil {
			if err := rec.WritePNG(o.snapshot); err != nil {
				fmt.Fprintf(os.Stderr, "snapshot: %v\n", err)
			} else {
				w, h := rec.Size()
				fmt.Printf("snapshot=%s  %dx%d\n", o.snapshot, w, h)
			}
		}
		return

	default:
		fmt.Fprintf(os.Stderr, "bad transport %q\n", o.transport)
		os.Exit(1)
	}

	var grabber engine.Grabber
	if o.synthetic {
		grabber = engine.NewSyntheticGrabber(o.width, o.height, o.fps)
	} else {
		grabber = engine.NewGrabber()
	}
	cfg := engine.CaptureConfig{
		FPS: o.fps, BlockSize: uint16(o.block),
		ScalePercent: o.scale, MaxWidth: o.maxW, MaxHeight: o.maxH,
	}
	if o.exclude != "" {
		cfg.ExcludeBundleIDs = splitCSV(o.exclude)
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

	w, h, stride := 0, 0, 0
	if sz, ok := grabber.(engine.OutputSizer); ok {
		if ww, hh, ss, ok := sz.OutputSize(); ok {
			w, h, stride = ww, hh, ss
		}
	}
	if w <= 0 || h <= 0 {
		// Fallback: one probe frame (sized conservatively).
		maxW, maxH := o.width, o.height
		if !o.synthetic {
			maxW, maxH = 3840, 2160
		}
		probe := make([]byte, maxW*maxH*4)
		meta, err := grabber.Capture(ctx, probe)
		if err != nil {
			fmt.Fprintf(os.Stderr, "probe: %v\n", err)
			os.Exit(1)
		}
		w, h, stride = int(meta.Width), int(meta.Height), meta.Stride
	}
	if stride <= 0 {
		stride = w * 4
	}
	fmt.Printf("backend=%s  geometry=%dx%d  stride=%d  scale=%d%%\n",
		grabber.Backend(), w, h, stride, o.scale)

	// High-res: larger blocks cut hash/diff work (TZ p99 target is FHD@64).
	block := o.block
	if block == 64 && w*h > 1920*1080 {
		block = 128
		fmt.Printf("auto block=%d (high-res; override with -block)\n", block)
	}

	var detector engine.PIIDetector
	if o.ocr {
		detector = engine.NewOCRDetector(engine.NewVisionOCRBackend())
	} else if o.pii {
		pd := engine.NewPatternDetector()
		// Explicit demo mask only when -pii is on (top-left block).
		pd.SetHits([]engine.Rect{{X: 0, Y: 0, W: uint16(block), H: uint16(block)}})
		detector = pd
	}

	pipe := engine.NewPipeline(engine.PipelineConfig{
		Width: w, Height: h, Stride: stride, BlockSize: block,
		Detector: detector, XORSeed: o.seed,
	})
	defer pipe.Close()

	pool := engine.NewBufferPool(stride * h)
	var fr engine.Frame
	warm := pool.Acquire()
	for i := 0; i < 5; i++ {
		m, err := grabber.Capture(ctx, warm)
		if err != nil {
			break
		}
		fr = engine.Frame{Meta: m, Pixels: warm}
		if pkt, err := pipe.Process(ctx, &fr); err == nil {
			sink.TryEnqueue(pkt)
		}
	}
	pool.Release(warm)

	samples := make([]time.Duration, 0, int(o.seconds*float64(o.fps))+16)
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
	if bs, ok := sink.(interface{ BytesSent() uint64 }); ok {
		elapsed := o.seconds
		if elapsed <= 0 {
			elapsed = 1
		}
		mbps := float64(bs.BytesSent()) * 8 / elapsed / 1e6
		fmt.Printf("wire_bytes_sent=%d  (~%.2f Mbit/s avg)\n", bs.BytesSent(), mbps)
	}
	if o.transport == "loopback" {
		fmt.Printf("webrtc_recv=%d  reconstructed=%d\n", recvCount.Load(), reconFrames.Load())
		if rtcPeer != nil {
			mbps := float64(rtcPeer.BytesRecv()) * 8 / maxFloat(o.seconds, 1) / 1e6
			fmt.Printf("wire_bytes_recv=%d  (~%.2f Mbit/s avg)\n", rtcPeer.BytesRecv(), mbps)
		}
		if o.snapshot != "" && rec != nil {
			if err := rec.WritePNG(o.snapshot); err != nil {
				fmt.Fprintf(os.Stderr, "snapshot: %v\n", err)
			} else {
				cw, ch := rec.Size()
				fmt.Printf("snapshot=%s  %dx%d\n", o.snapshot, cw, ch)
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
	per := 0.0
	if frames > 0 {
		per = float64(mallocs) / float64(frames)
	}
	fmt.Printf("heap_mallocs=%d  (~%.2f/frame)  total_alloc_bytes=%d\n", mallocs, per, bytes)
	fmt.Println(streamer.DescribeHandoff())
}

func startPreview(addr string) *streamer.PreviewServer {
	if addr == "" {
		return nil
	}
	p := streamer.NewPreviewServer(70)
	url, err := p.ListenAndServe(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "preview: %v\n", err)
		return nil
	}
	fmt.Printf("preview: %s  (open in browser)\n", url)
	return p
}

func maybePreview(p *streamer.PreviewServer, rec *streamer.FrameReconstructor, last *time.Time) {
	if p == nil || rec == nil {
		return
	}
	now := time.Now()
	if !last.IsZero() && now.Sub(*last) < 100*time.Millisecond {
		return // ~10 FPS preview encode cap
	}
	*last = now
	rec.WithCanvas(func(bgra []byte, w, h, stride int) {
		_ = p.UpdateFromBGRA(bgra, w, h, stride)
	})
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func splitCSV(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if part := s[start:i]; part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}
