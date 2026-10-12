//go:build linux && cgo && !nopipewire

package engine

/*
Default Linux builds enable PipeWire/portal when pkg-config finds the libs.
Use -tags nopipewire on hosts without libpipewire / gio.

#cgo pkg-config: libpipewire-0.3 gio-2.0 gobject-2.0
#cgo CFLAGS: -DSS_HAVE_PIPEWIRE=1
*/
import "C"

// PipeWireBuiltIn is true when this binary was linked with libpipewire.
func PipeWireBuiltIn() bool { return true }
