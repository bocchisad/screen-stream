package engine

import "testing"

func TestParseCaptureMode(t *testing.T) {
	m, err := ParseCaptureMode("gpu-surface")
	if err != nil || m != CaptureGPUSurface {
		t.Fatalf("got %v %v", m, err)
	}
	m, err = ParseCaptureMode("cpu")
	if err != nil || m != CaptureCPU {
		t.Fatalf("got %v %v", m, err)
	}
	if _, err := ParseCaptureMode("nope"); err == nil {
		t.Fatal("expected error")
	}
}
