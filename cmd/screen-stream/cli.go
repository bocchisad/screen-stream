package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/bocchisad/screen-stream/engine"
	"github.com/bocchisad/screen-stream/streamer"
)

// runCLI is an interactive terminal menu that fills flags then returns chosen mode + opts.
func runCLI() (mode string, o sessionOpts, signalAddr string) {
	in := bufio.NewReader(os.Stdin)
	fmt.Println()
	fmt.Println("screen-stream interactive CLI")
	fmt.Println("────────────────────────────")
	fmt.Println("  1) hub")
	fmt.Println("  2) send")
	fmt.Println("  3) recv")
	fmt.Println("  4) loopback")
	fmt.Println("  5) bench")
	fmt.Println("  6) doctor")
	fmt.Println("  q) quit")
	fmt.Print("mode> ")
	choice := readLine(in)
	switch strings.TrimSpace(choice) {
	case "1", "hub":
		mode = "hub"
	case "2", "send":
		mode = "send"
	case "3", "recv":
		mode = "recv"
	case "4", "loopback":
		mode = "loopback"
	case "5", "bench":
		mode = "bench"
	case "6", "doctor":
		mode = "doctor"
	case "q", "quit", "":
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown choice %q\n", choice)
		os.Exit(1)
	}

	signalAddr = promptDefault(in, "signal", "127.0.0.1:8787")
	o.token = streamer.ResolveAuthToken(promptDefault(in, "token (or env SCREEN_STREAM_TOKEN)", os.Getenv(streamer.EnvAuthToken)))
	o.seconds = promptFloat(in, "seconds", 30)
	o.fps = promptInt(in, "fps", 60)
	o.scale = promptInt(in, "scale %", 75)
	o.synthetic = promptYes(in, "synthetic frames", mode == "bench" || mode == "loopback")
	o.compress = promptYes(in, "compress", true)
	o.pii = promptYes(in, "pii/ocr", false)
	o.ocr = o.pii

	capMode := promptDefault(in, "capture mode (cpu|gpu-surface)", "cpu")
	cm, err := engine.ParseCaptureMode(capMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	o.captureMode = cm
	if cm == engine.CaptureGPUSurface {
		fmt.Fprintln(os.Stderr, "WARN: gpu-surface is EXPERIMENTAL — still ScreenCaptureKit/PipeWire (same OS visibility); at your own risk")
	}

	if mode == "recv" || mode == "loopback" {
		if promptYes(in, "preview MJPEG", mode == "recv") {
			o.preview = promptDefault(in, "preview addr", "127.0.0.1:8090")
		}
	}
	if mode == "send" || mode == "recv" {
		o.peer = promptDefault(in, "peer", "default")
		o.peers = []string{o.peer}
		o.stun = promptDefault(in, "stun (empty=none)", "")
	}
	o.signalURL = "http://" + signalAddr
	o.block = 64
	o.width, o.height = 1920, 1080
	return mode, o, signalAddr
}

func readLine(in *bufio.Reader) string {
	s, _ := in.ReadString('\n')
	return strings.TrimRight(s, "\r\n")
}

func promptDefault(in *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]> ", label, def)
	} else {
		fmt.Printf("%s> ", label)
	}
	s := strings.TrimSpace(readLine(in))
	if s == "" {
		return def
	}
	return s
}

func promptInt(in *bufio.Reader, label string, def int) int {
	s := promptDefault(in, label, strconv.Itoa(def))
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func promptFloat(in *bufio.Reader, label string, def float64) float64 {
	s := promptDefault(in, label, strconv.FormatFloat(def, 'g', -1, 64))
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return n
}

func promptYes(in *bufio.Reader, label string, def bool) bool {
	d := "n"
	if def {
		d = "y"
	}
	s := strings.ToLower(promptDefault(in, label+" (y/n)", d))
	return s == "y" || s == "yes" || s == "1"
}
