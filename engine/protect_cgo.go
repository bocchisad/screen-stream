//go:build cgo

package engine

/*
#cgo CFLAGS: -I${SRCDIR} -I${SRCDIR}/native
#include "protect.h"
*/
import "C"
import "unsafe"

func xorBuffer(buf []byte, key *uint64) {
	if len(buf) == 0 {
		return
	}
	ks := C.uint64_t(*key)
	C.ss_xor_buffer((*C.uint8_t)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)), &ks)
	*key = uint64(ks)
}

func blockHashes(bgra []byte, width, height, stride, blockSize int, out []uint64) {
	if len(bgra) == 0 || len(out) == 0 {
		return
	}
	C.ss_block_hashes(
		(*C.uint8_t)(unsafe.Pointer(&bgra[0])),
		C.int(width), C.int(height), C.int(stride), C.int(blockSize),
		(*C.uint64_t)(unsafe.Pointer(&out[0])),
	)
}

func diffBlocks(curr, prev []uint64, blocksX, blocksY, blockSize, frameW, frameH int, outRects []uint16, forceAll bool) int {
	if len(outRects) < 4 {
		return 0
	}
	fa := 0
	if forceAll {
		fa = 1
	}
	return int(C.ss_diff_blocks(
		(*C.uint64_t)(unsafe.Pointer(&curr[0])),
		(*C.uint64_t)(unsafe.Pointer(&prev[0])),
		C.int(blocksX), C.int(blocksY), C.int(blockSize),
		C.int(frameW), C.int(frameH),
		(*C.uint16_t)(unsafe.Pointer(&outRects[0])),
		C.int(len(outRects)/4),
		C.int(fa),
	))
}

func fillBlack(bgra []byte, stride, x, y, w, h int) {
	if len(bgra) == 0 {
		return
	}
	C.ss_fill_black((*C.uint8_t)(unsafe.Pointer(&bgra[0])), C.int(stride),
		C.int(x), C.int(y), C.int(w), C.int(h))
}
