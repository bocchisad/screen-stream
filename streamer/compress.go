package streamer

import (
	"bytes"
	"compress/zlib"
	"io"

	"github.com/salfetka/screen-stream/engine"
)

// CompressPayload zlib-compresses pkt.Payload in place (new backing) and sets FlagCompressed.
func CompressPayload(pkt *engine.DirtyPacket) error {
	if len(pkt.Payload) == 0 || pkt.Flags&engine.FlagCompressed != 0 {
		return nil
	}
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.BestSpeed)
	if err != nil {
		return err
	}
	if _, err := w.Write(pkt.Payload); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	// Only keep if smaller (high-res keyframes usually are).
	if buf.Len() >= len(pkt.Payload) {
		return nil
	}
	pkt.Payload = append(pkt.Payload[:0], buf.Bytes()...)
	pkt.Flags |= engine.FlagCompressed
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
	return io.ReadAll(r)
}
