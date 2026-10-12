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

	"github.com/bocchisad/screen-stream/engine"
	"github.com/bocchisad/screen-stream/streamer"
)

func main() {
	mode := flag.String("mode", "bench", "bench | hub | send | recv | loopback | doctor | cli")
	signalAddr := flag.String("signal", "127.0.0.1:8787", "signaling listen/dial host:port")
	seconds := flag.Float64("seconds", 3, "run duration (bench/send/recv/loopback)")
	fps := flag.Int("fps", 60, "target FPS")
	width := flag.Int("width", 1920, "synthetic width")
	height := flag.Int("height", 1080, "synthetic height")
	block := flag.Int("block", 64, "diff block size")
	synthetic := flag.Bool("synthetic", true, "synthetic frames (send/bench/loopback)")
	pii := flag.Bool("pii", false, "PII redaction (Vision OCR on macOS + pattern/ASCII scan)")
	ocr := flag.Bool("ocr", false, "same as -pii (Vision OCR when available)")
	compress := flag.Bool("compress", true, "zlib payloads on WebRTC")
	scale := flag.Int("scale", 100, "capture scale percent (100 = Retina pixels)")
	maxW := flag.Int("max-width", 0, "cap capture width")
	maxH := flag.Int("max-height", 0, "cap capture height")
	snapshot := flag.String("snapshot", "", "write reconstructed PNG (recv/loopback)")
	preview := flag.String("preview", "", "MJPEG preview listen addr for recv/loopback (e.g. 127.0.0.1:8090)")
	exclude := flag.String("exclude", "", "macOS bundle IDs to exclude (comma-separated)")
	excludeWin := flag.String("exclude-window", "", "macOS window IDs to exclude (comma-separated uint32)")
	xorSeedFlag := flag.String("xor-seed", "", "bench/local RAM XOR seed (decimal/0xHEX); P2P uses ECDH-derived seed")
	tokenFlag := flag.String("token", "", "required auth for hub/send/recv/preview; else env SCREEN_STREAM_TOKEN")
	stunFlag := flag.String("stun", "", "optional STUN URL (e.g. stun:stun.l.google.com:19302)")
	expectFP := flag.String("expect-fingerprint", "", "exit 1 if fingerprint mismatches (overrides TOFU pin)")
	tofuFlag := flag.String("tofu", "auto", "auto|off — pin stable identity fingerprint on send/recv")
	tofuFile := flag.String("tofu-file", "", "TOFU pins JSON (default ~/.config/screen-stream/tofu.json)")
	identityFlag := flag.String("identity", "", "static X25519 priv key path (default ~/.config/screen-stream/identity.key)")
	peerFlag := flag.String("peer", "default", "hub peer slot name (recv / single send)")
	peersFlag := flag.String("peers", "", "send fanout: comma-separated peer names (overrides -peer)")
	captureFlag := flag.String("capture", "cpu", "capture staging: cpu | gpu-surface (experimental)")
	codecFlag := flag.String("codec", "dirty", "wire codec: dirty (DataChannel tiles) | h264 (HW track: VT/VAAPI/MF)")
	// legacy alias
	webrtcAlias := flag.Bool("webrtc", false, "alias for -mode=loopback")
	flag.Parse()

	if *webrtcAlias && *mode == "bench" {
		*mode = "loopback"
	}

	captureMode, err := engine.ParseCaptureMode(*captureFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if captureMode == engine.CaptureGPUSurface {
		fmt.Fprintln(os.Stderr, "WARN: -capture=gpu-surface is EXPERIMENTAL — still ScreenCaptureKit/PipeWire (same OS visibility); at your own risk")
	}
	videoCodec, err := engine.ParseVideoCodec(*codecFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if videoCodec == engine.CodecH264 {
		switch *mode {
		case "bench", "hub", "doctor", "cli":
			if *mode == "bench" {
				fmt.Fprintln(os.Stderr, "h264: use -mode=send|recv|loopback (not bench)")
				os.Exit(1)
			}
		case "send", "loopback":
			if !engine.HardwareEncoderAvailable() {
				fmt.Fprintf(os.Stderr, "h264 encode: %v — need platform HW encoder (VideoToolbox/VAAPI/MediaFoundation; no software fallback)\n", engine.ErrNotSupported)
				os.Exit(1)
			}
		case "recv":
			// decoder opened later; still HW-only on darwin
		}
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
	token := streamer.ResolveAuthToken(*tokenFlag)

	var cliOpts sessionOpts
	if *mode == "cli" {
		var addr string
		*mode, cliOpts, addr = runCLI()
		*signalAddr = addr
		token = cliOpts.token
		seed = cliOpts.seed
		if seed == 0 {
			seed = engine.ResolveXORSeed(0)
		}
		captureMode = cliOpts.captureMode
		*preview = cliOpts.preview
		*seconds = cliOpts.seconds
		*fps = cliOpts.fps
		*scale = cliOpts.scale
		*synthetic = cliOpts.synthetic
		*compress = cliOpts.compress
		*pii = cliOpts.pii
		*ocr = cliOpts.ocr
		*stunFlag = cliOpts.stun
		*peerFlag = cliOpts.peer
		if cliOpts.tofu != "" {
			*tofuFlag = cliOpts.tofu
		}
		if len(cliOpts.peers) > 0 {
			*peersFlag = strings.Join(cliOpts.peers, ",")
		}
	}

	needToken := *mode == "hub" || *mode == "send" || *mode == "recv" || *preview != ""
	if needToken {
		if err := streamer.RequireAuthToken(token); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("screen-stream  go=%s  os=%s/%s  mode=%s  capture=%v  codec=%s  ecdh=x25519  aead=chacha20poly1305  auth=%v\n",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, *mode, captureMode, videoCodec, token != "")

	tofuMode := strings.ToLower(strings.TrimSpace(*tofuFlag))
	if tofuMode != "auto" && tofuMode != "off" {
		fmt.Fprintf(os.Stderr, "-tofu must be auto|off\n")
		os.Exit(1)
	}
	base := sessionOpts{
		seconds: *seconds, fps: *fps, width: *width, height: *height, block: *block,
		synthetic: *synthetic, pii: *pii, ocr: *ocr, compress: *compress,
		scale: *scale, maxW: *maxW, maxH: *maxH, exclude: *exclude, excludeWin: *excludeWin,
		seed: seed, token: token, stun: *stunFlag, expectFP: *expectFP, captureMode: captureMode,
		tofu: tofuMode, tofuFile: *tofuFile, identityPath: *identityFlag, codec: videoCodec,
	}

	switch *mode {
	case "hub":
		runHub(*signalAddr, token)
	case "doctor":
		runDoctor(*signalAddr, token)
	case "bench":
		base.transport = "local"
		runSession(base)
	case "loopback":
		base.snapshot = *snapshot
		base.preview = *preview
		base.transport = "loopback"
		runSession(base)
	case "send":
		peers := splitCSV(*peersFlag)
		if len(peers) == 0 {
			peers = []string{*peerFlag}
		}
		base.peers = peers
		base.transport = "send"
		base.signalURL = "http://" + *signalAddr
		runSession(base)
	case "recv":
		base.synthetic = true
		base.peer = *peerFlag
		base.snapshot = *snapshot
		base.preview = *preview
		base.transport = "recv"
		base.signalURL = "http://" + *signalAddr
		runSession(base)
	default:
		fmt.Fprintf(os.Stderr, "unknown -mode %q\n", *mode)
		os.Exit(1)
	}
}

func runHub(addr, token string) {
	hub := streamer.NewHTTPSignalingHub()
	hub.Token = token
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
	if !streamer.IsLoopbackAddr(addr) {
		fmt.Fprintf(os.Stderr, "WARN: hub is NOT on loopback (%s) — prefer 127.0.0.1\n", addr)
	}
	fmt.Println("auth: token required on /offer /answer /ice /status")
	fmt.Println("keys: X25519 ECDH on offer/answer (no shared -xor-seed)")
	fmt.Println("keep this terminal open, then in TWO other terminals:")
	fmt.Printf("  send: go run ./cmd/screen-stream -mode=send -signal=%s -synthetic=false -scale=75 -seconds=30 -token=…\n", clientAddr)
	fmt.Printf("  recv: go run ./cmd/screen-stream -mode=recv -signal=%s -seconds=30 -preview=127.0.0.1:8090 -token=…\n", clientAddr)
	select {}
}

func runDoctor(signalAddr, token string) {
	fmt.Println("screen-stream doctor")
	fmt.Printf("  go=%s  os=%s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  auth env %s set=%v\n", streamer.EnvAuthToken, token != "")
	fmt.Printf("  ocr_available=%v  tesseract_available=%v  pipewire_builtin=%v\n",
		engine.OCRAvailable(), engine.TesseractAvailable(), engine.PipeWireBuiltIn())
	if runtime.GOOS == "linux" && !engine.PipeWireBuiltIn() {
		fmt.Println("  pipewire hint: rebuild without -tags nopipewire; need libpipewire + gio (pkg-config)")
	} else if runtime.GOOS == "linux" {
		fmt.Println("  pipewire: linked (opt out: go build -tags nopipewire)")
	}
	fmt.Println("  capture staging: -capture=cpu|gpu-surface (gpu-surface = one-copy retain; SCK/PipeWire)")
	if idPath, err := streamer.DefaultIdentityPath(); err == nil {
		fmt.Printf("  identity_path=%s\n", idPath)
	}
	if tofuPath, err := streamer.DefaultTOFUPath(); err == nil {
		fmt.Printf("  tofu_path=%s  (-tofu=auto pins identity fingerprint)\n", tofuPath)
	}
	fmt.Printf("  h264_hw_encoder=%v  (-codec=h264)\n", engine.HardwareEncoderAvailable())

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
	res, err := httpGetStatus(base, token)
	if err != nil {
		fmt.Printf("  hub %s: DOWN (%v)\n", base, err)
		fmt.Println("  hint: go run ./cmd/screen-stream -mode=hub -signal=127.0.0.1:8787 -token=…")
	} else {
		fmt.Printf("  hub %s: UP %s\n", base, res)
	}
	fmt.Println("  preview URL (while recv runs): http://127.0.0.1:8090/?token=…")
}

func httpGetStatus(base, token string) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodGet, base+"/status", nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := client.Do(req)
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
	excludeWin              string
	seed                    uint64
	snapshot                string
	preview                 string // MJPEG listen addr
	token                   string
	stun                    string
	expectFP                string
	tofu                    string // auto|off
	tofuFile                string
	identityPath            string
	peer                    string
	peers                   []string
	captureMode             engine.CaptureMode
	codec                   engine.VideoCodec
	transport               string // local | loopback | send | recv
	signalURL               string
}

func checkFingerprint(got, expect string) {
	if expect == "" {
		return
	}
	if got != expect {
		fmt.Fprintf(os.Stderr, "fingerprint mismatch: got %s want %s\n", got, expect)
		os.Exit(1)
	}
}

func (o sessionOpts) withIdentity(cfg streamer.SessionConfig) streamer.SessionConfig {
	need := o.tofu == "auto" || o.identityPath != ""
	if !need || o.transport == "loopback" || o.transport == "local" {
		return cfg
	}
	priv, pub, err := streamer.LoadOrCreateIdentity(o.identityPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "identity: %v\n", err)
		os.Exit(1)
	}
	cfg.IdentityPriv = priv
	cfg.IdentityPub = pub
	return cfg
}

func enforceFingerprint(o sessionOpts, signalURL, peer, fp string) {
	checkFingerprint(fp, o.expectFP)
	if o.tofu != "auto" || fp == "" {
		return
	}
	if o.transport == "loopback" || o.transport == "local" {
		return
	}
	store, err := streamer.LoadTOFUStore(o.tofuFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu: %v\n", err)
		os.Exit(1)
	}
	key := streamer.TOFUPinKey(signalURL, peer)
	if err := store.CheckOrPin(key, fp); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if err := store.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "tofu save: %v\n", err)
		os.Exit(1)
	}
}

func startMetricsPusher(ctx context.Context, cli *streamer.HTTPSignalingClient, sessions ...*streamer.WebRTCSession) {
	if cli == nil || len(sessions) == 0 {
		return
	}
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				var m streamer.PeerMetrics
				for _, s := range sessions {
					if s == nil {
						continue
					}
					m.BytesSent += s.BytesSent()
					m.BytesRecv += s.BytesRecv()
					m.Sent += s.Sent()
					m.Recv += s.Recv()
					m.Dropped += s.Dropped()
				}
				_ = cli.PostMetrics(m)
			}
		}
	}()
}

func runSession(o sessionOpts) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(o.seconds*float64(time.Second)))
	defer cancel()

	var (
		sink         streamer.Sink
		recvCount    atomic.Uint64
		reconFrames  atomic.Uint64
		rec          *streamer.FrameReconstructor
		rtc          *streamer.WebRTCSession
		rtcPeer      *streamer.WebRTCSession
		h264Sessions []*streamer.WebRTCSession
	)

	switch o.transport {
	case "local":
		local := streamer.NewLocalSink(4, nil)
		_ = local.Start(ctx)
		defer local.Stop()
		sink = local

	case "loopback":
		rec = streamer.NewFrameReconstructor(1)
		prev := startPreview(o.preview, o.token)
		if prev != nil {
			defer prev.Close()
		}
		var lastPrev time.Time
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 45*time.Second)
		var err error
		useH264 := o.codec == engine.CodecH264
		var h264Dec engine.VideoDecoder
		if useH264 {
			h264Dec = engine.NewHardwareDecoder()
			if err := h264Dec.Open(3840, 2160); err != nil {
				fmt.Fprintf(os.Stderr, "h264 decode: %v\n", err)
				os.Exit(1)
			}
			defer h264Dec.Close()
		}
		rtc, rtcPeer, err = streamer.ConnectLoopbackOpts(dialCtx, streamer.SessionConfig{
			RingDepth: 4,
			Compress:  o.compress,
			STUNURL:   o.stun,
			H264:      useH264,
			OnSessionReady: func(seed uint64) {
				o.seed = seed
				if rec != nil {
					rec.SetSeed(seed)
				}
			},
			OnPacket: func(b []byte) {
				recvCount.Add(1)
				if err := rec.Apply(b); err == nil {
					reconFrames.Add(1)
					maybePreview(prev, rec, &lastPrev)
				}
			},
			OnH264AU: func(au []byte) {
				recvCount.Add(1)
				bgra, w, h, stride, err := h264Dec.Decode(context.Background(), au)
				if err != nil || bgra == nil {
					return
				}
				reconFrames.Add(1)
				if prev != nil {
					_ = prev.UpdateFromBGRA(bgra, w, h, stride)
				}
			},
		})
		dialCancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "webrtc loopback: %v\n", err)
			os.Exit(1)
		}
		o.seed = rtc.SessionSeed()
		fp := rtc.SessionFingerprint()
		enforceFingerprint(o, "", "", fp)
		defer rtc.Stop()
		defer rtcPeer.Stop()
		sink = rtc
		h264Sessions = []*streamer.WebRTCSession{rtc}
		fmt.Printf("webrtc: loopback codec=%s (compress=%v ecdh_seed=0x%X fingerprint=%s)\n", o.codec, o.compress, o.seed, fp)

	case "send":
		peers := o.peers
		if len(peers) == 0 {
			peers = []string{"default"}
		}
		useH264 := o.codec == engine.CodecH264
		sessions := make([]*streamer.WebRTCSession, 0, len(peers))
		var metricsCLI *streamer.HTTPSignalingClient
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 60*time.Second)
		for _, peer := range peers {
			sig := streamer.NewHTTPSignalingClientPeer(o.signalURL, o.token, peer)
			if metricsCLI == nil {
				metricsCLI = sig
			}
			s := streamer.NewWebRTCSession(o.withIdentity(streamer.SessionConfig{
				RingDepth: 4, Compress: o.compress, STUNURL: o.stun, H264: useH264,
			}))
			if err := s.DialOfferer(dialCtx, sig); err != nil {
				dialCancel()
				fmt.Fprintf(os.Stderr, "dial offer peer=%s: %v\n", peer, err)
				os.Exit(1)
			}
			fp := s.SessionFingerprint()
			enforceFingerprint(o, o.signalURL, peer, fp)
			_ = s.StartSendLoop(context.Background())
			sessions = append(sessions, s)
			fmt.Printf("webrtc: send peer=%s codec=%s fingerprint=%s ecdh_seed=0x%X\n", peer, o.codec, fp, s.SessionSeed())
		}
		dialCancel()
		o.seed = sessions[0].SessionSeed()
		rtc = sessions[0]
		defer func() {
			for _, s := range sessions {
				_ = s.Stop()
			}
		}()
		h264Sessions = sessions
		if len(sessions) == 1 {
			sink = sessions[0]
		} else {
			sink = streamer.NewFanoutSink(sessions...)
		}
		startMetricsPusher(ctx, metricsCLI, sessions...)
		fmt.Printf("webrtc: send → %s peers=%v codec=%s\n", o.signalURL, peers, o.codec)

	case "recv":
		rec = streamer.NewFrameReconstructor(1)
		prev := startPreview(o.preview, o.token)
		if prev != nil {
			defer prev.Close()
		}
		var lastPrev time.Time
		peer := o.peer
		if peer == "" {
			peer = "default"
		}
		useH264 := o.codec == engine.CodecH264
		var h264Dec engine.VideoDecoder
		if useH264 {
			h264Dec = engine.NewHardwareDecoder()
			if err := h264Dec.Open(3840, 2160); err != nil {
				fmt.Fprintf(os.Stderr, "h264 decode: %v\n", err)
				os.Exit(1)
			}
			defer h264Dec.Close()
		}
		sig := streamer.NewHTTPSignalingClientPeer(o.signalURL, o.token, peer)
		rtc = streamer.NewWebRTCSession(o.withIdentity(streamer.SessionConfig{
			RingDepth: 4,
			Compress:  o.compress,
			STUNURL:   o.stun,
			H264:      useH264,
			OnSessionReady: func(seed uint64) {
				o.seed = seed
				if rec != nil {
					rec.SetSeed(seed)
				}
			},
			OnPacket: func(b []byte) {
				recvCount.Add(1)
				if err := rec.Apply(b); err == nil {
					reconFrames.Add(1)
					maybePreview(prev, rec, &lastPrev)
				}
			},
			OnH264AU: func(au []byte) {
				recvCount.Add(1)
				bgra, w, h, stride, err := h264Dec.Decode(context.Background(), au)
				if err != nil || bgra == nil {
					return
				}
				reconFrames.Add(1)
				if prev != nil {
					_ = prev.UpdateFromBGRA(bgra, w, h, stride)
				}
			},
		}))
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := rtc.DialAnswerer(dialCtx, sig); err != nil {
			dialCancel()
			fmt.Fprintf(os.Stderr, "dial answer: %v\n", err)
			fmt.Fprintf(os.Stderr, "hint: hub + send + recv, same -token; matching -peer / -peers\n")
			os.Exit(1)
		}
		dialCancel()
		o.seed = rtc.SessionSeed()
		fp := rtc.SessionFingerprint()
		enforceFingerprint(o, o.signalURL, peer, fp)
		defer rtc.Stop()
		startMetricsPusher(ctx, sig, rtc)
		fmt.Printf("webrtc: recv ← %s peer=%s codec=%s ecdh_seed=0x%X fingerprint=%s\n", o.signalURL, peer, o.codec, o.seed, fp)
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
		Mode: o.captureMode,
	}
	if o.exclude != "" {
		cfg.ExcludeBundleIDs = splitCSV(o.exclude)
	}
	if o.excludeWin != "" {
		cfg.ExcludeWindowIDs = parseUint32CSV(o.excludeWin)
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
	if o.ocr || o.pii {
		detector = engine.NewDefaultPIIDetector()
		if engine.OCRAvailable() {
			fmt.Println("pii: Vision OCR + pattern scan")
		} else {
			fmt.Println("pii: pattern/ASCII scan (OCR unavailable on this build)")
		}
	}

	pool := engine.NewBufferPool(stride * h)
	samples := make([]time.Duration, 0, int(o.seconds*float64(o.fps))+16)
	var frames int
	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)
	buf := pool.Acquire()
	defer pool.Release(buf)

	if o.codec == engine.CodecH264 {
		enc := engine.NewHardwareEncoder()
		if err := enc.Open(engine.EncoderConfig{
			Width: w, Height: h, FPS: o.fps,
			Bitrate: engine.DefaultH264Bitrate(w, h, o.fps),
			KeyframeEvery: o.fps * 2,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "h264 encode open: %v\n", err)
			os.Exit(1)
		}
		defer enc.Close()
		fmt.Printf("encoder=%s bitrate≈%d\n", enc.Backend(), engine.DefaultH264Bitrate(w, h, o.fps))

	loopH264:
		for {
			select {
			case <-ctx.Done():
				break loopH264
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
			if detector != nil {
				_ = engine.RedactBGRA(ctx, detector, m, buf, stride)
			}
			t0 := time.Now()
			aus, err := enc.Encode(ctx, buf, stride, m.PTS)
			dt := time.Since(t0)
			if err != nil {
				fmt.Fprintf(os.Stderr, "h264 encode: %v\n", err)
				break
			}
			for _, au := range aus {
				for _, s := range h264Sessions {
					s.WriteH264(au)
				}
			}
			samples = append(samples, dt)
			frames++
		}
	} else {
		pipe := engine.NewPipeline(engine.PipelineConfig{
			Width: w, Height: h, Stride: stride, BlockSize: block,
			Detector: detector, XORSeed: o.seed,
		})
		defer pipe.Close()

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

	loopDirty:
		for {
			select {
			case <-ctx.Done():
				break loopDirty
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

func startPreview(addr, token string) *streamer.PreviewServer {
	if addr == "" {
		return nil
	}
	if !streamer.IsLoopbackAddr(addr) {
		fmt.Fprintf(os.Stderr, "WARN: preview is NOT on loopback (%s)\n", addr)
	}
	p := streamer.NewPreviewServer(70)
	p.Token = token
	url, err := p.ListenAndServe(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "preview: %v\n", err)
		return nil
	}
	fmt.Printf("preview: %s\n", url)
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

func parseUint32CSV(s string) []uint32 {
	parts := splitCSV(s)
	out := make([]uint32, 0, len(parts))
	for _, p := range parts {
		var n uint64
		if _, err := fmt.Sscanf(strings.TrimSpace(p), "%d", &n); err != nil {
			continue
		}
		out = append(out, uint32(n))
	}
	return out
}
