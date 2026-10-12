//go:build !darwin || !cgo

package engine

// OCRAvailable is false without darwin+cgo Vision.
func OCRAvailable() bool { return false }
