//go:build !linux || !cgo || nopipewire

package engine

// PipeWireBuiltIn is false when PipeWire was not linked (macOS, nopipewire, nocgo).
func PipeWireBuiltIn() bool { return false }
