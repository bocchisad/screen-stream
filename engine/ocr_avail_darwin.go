//go:build darwin && cgo

package engine

// OCRAvailable reports on-device Vision text recognition.
func OCRAvailable() bool { return true }
