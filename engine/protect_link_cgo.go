//go:build cgo

package engine

/*
#cgo CFLAGS: -I${SRCDIR} -I${SRCDIR}/native
#include "protect.h"
*/
import "C"

// Keep native/protect.c linked (via c_bridge.c) for tooling / future opt-in.
var _ = C.ss_xor_buffer
