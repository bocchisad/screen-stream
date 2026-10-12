package streamer

import (
	"bytes"
	"compress/zlib"
	"io"
	"sync"

	"github.com/salfetka/screen-stream/engine"
)

var (
	zlibBufPool = sync.Pool{New: func() any {
		b := make([]byte, 0, 1<<20)
		return &b
	}}
	zlibWriterPool = sync.Pool{New: func() any {
		w, err := zlib.NewWriterLevel(io.Discard, zlib.BestSpeed)
		if err != nil {
			return nil
		}
		return w
	}}
)

// CompressPayload zlib-compresses pkt.Payload into its own capacity when smaller.
// Skips tiny payloads (overhead not worth it). Reuses writer/buffer pools.
func CompressPayload(pkt *engine.DirtyPacket) error {
	if len(pkt.Payload) < 2048 || pkt.Flags&engine.FlagCompressed != 0 {
		return nil
	}
	bp := zlibBufPool.Get().(*[]byte)
	buf := bytes.NewBuffer((*bp)[:0])
	w, _ := zlibWriterPool.Get().(*zlib.Writer)
	if w == nil {
		var err error
		w, err = zlib.NewWriterLevel(buf, zlib.BestSpeed)
		if err != nil {
			zlibBufPool.Put(bp)
			return err
		}
	} else {
		w.Reset(buf)
	}
	_, err := w.Write(pkt.Payload)
	if err != nil {
		_ = w.Close()
		zlibWriterPool.Put(w)
		b := buf.Bytes()
		*bp = b[:0]
		zlibBufPool.Put(bp)
		return err
	}
	if err := w.Close(); err != nil {
		zlibWriterPool.Put(w)
		b := buf.Bytes()
		*bp = b[:0]
		zlibBufPool.Put(bp)
		return err
	}
	zlibWriterPool.Put(w)
	out := buf.Bytes()
	if len(out) >= len(pkt.Payload) {
		*bp = out[:0]
		zlibBufPool.Put(bp)
		return nil
	}
	if cap(pkt.Payload) < len(out) {
		*bp = out[:0]
		zlibBufPool.Put(bp)
		return nil
	}
	pkt.Payload = pkt.Payload[:len(out)]
	copy(pkt.Payload, out)
	pkt.Flags |= engine.FlagCompressed
	*bp = out[:0]
	zlibBufPool.Put(bp)
	return nil
}

// DecompressPayload expands zlib payload when FlagCompressed is set.
func DecompressPayload(flags uint16, payload []byte) ([]byte, error) {
	if flags&engine.FlagCompressed == 0 {
		return payload, nil
	}
	r, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	bp := zlibBufPool.Get().(*[]byte)
	buf := bytes.NewBuffer((*bp)[:0])
	_, err = io.Copy(buf, r)
	if err != nil {
		*bp = buf.Bytes()[:0]
		zlibBufPool.Put(bp)
		return nil, err
	}
	out := append([]byte(nil), buf.Bytes()...)
	*bp = buf.Bytes()[:0]
	zlibBufPool.Put(bp)
	return out, nil
}
