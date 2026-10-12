package streamer

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"sync"

	"github.com/salfetka/screen-stream/engine"
)

// FrameReconstructor applies dirty tiles onto a BGRA canvas (decrypts with shared seed).
type FrameReconstructor struct {
	mu      sync.Mutex
	seed    uint64
	width   int
	height  int
	stride  int
	canvas  []byte
	tileBuf []byte
	plain   []byte // decompressed payload scratch
	tiles   uint64
	frames  uint64
}

func NewFrameReconstructor(seed uint64) *FrameReconstructor {
	if seed == 0 {
		seed = engine.DefaultXORSeed
	}
	return &FrameReconstructor{seed: seed}
}

// SetSeed updates the XOR seed (call after ECDH dial before applying frames).
func (r *FrameReconstructor) SetSeed(seed uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seed != 0 {
		r.seed = seed
	}
}

func (r *FrameReconstructor) Stats() (frames, tiles uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frames, r.tiles
}

func (r *FrameReconstructor) Canvas() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.canvas) == 0 {
		return nil
	}
	out := make([]byte, len(r.canvas))
	copy(out, r.canvas)
	return out
}

func (r *FrameReconstructor) Size() (w, h int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.width, r.height
}

func (r *FrameReconstructor) Apply(wire []byte) error {
	u, err := Unpack(wire)
	if err != nil {
		return err
	}
	return r.ApplyUnpacked(u)
}

func (r *FrameReconstructor) ApplyUnpacked(u *Unpacked) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, h := int(u.Width), int(u.Height)
	if w <= 0 || h <= 0 {
		return fmt.Errorf("invalid geometry %dx%d", w, h)
	}
	if w*h > MaxFramePixels || w > 8192 || h > 8192 {
		return fmt.Errorf("geometry exceeds cap: %dx%d", w, h)
	}
	stride := w * 4
	need := stride * h
	if need <= 0 {
		return fmt.Errorf("invalid frame bytes")
	}
	if r.width != w || r.height != h || len(r.canvas) < need {
		r.width, r.height, r.stride = w, h, stride
		r.canvas = make([]byte, need)
	}

	payload, err := DecompressPayload(u.Flags, u.Payload)
	if err != nil {
		return err
	}
	if u.Flags&engine.FlagCompressed != 0 {
		r.plain = payload
	}

	fk := engine.FrameKey(r.seed, u.FrameID)
	for i := range u.Regions {
		reg := u.Regions[i]
		off := int(reg.PayloadOffset)
		n := int(reg.PayloadLen)
		if off < 0 || n < 0 || off+n > len(payload) {
			return ErrTruncated
		}
		if cap(r.tileBuf) < n {
			r.tileBuf = make([]byte, n)
		}
		tile := r.tileBuf[:n]
		copy(tile, payload[off:off+n])
		tk := engine.TileKey(fk, reg.X, reg.Y)
		engine.XORBuffer(tile, &tk)
		if err := blitBGRA(r.canvas, r.stride, tile, int(reg.X), int(reg.Y), int(reg.W), int(reg.H)); err != nil {
			return err
		}
		r.tiles++
	}
	r.frames++
	return nil
}

// WithCanvas calls fn with the live BGRA canvas (valid only during the call).
func (r *FrameReconstructor) WithCanvas(fn func(bgra []byte, width, height, stride int)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.canvas) == 0 || fn == nil {
		return
	}
	fn(r.canvas, r.width, r.height, r.stride)
}

// WritePNG saves the current canvas as a PNG (BGRA→RGBA).
func (r *FrameReconstructor) WritePNG(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.canvas) == 0 {
		return fmt.Errorf("empty canvas")
	}
	img := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	for y := 0; y < r.height; y++ {
		for x := 0; x < r.width; x++ {
			si := y*r.stride + x*4
			di := y*img.Stride + x*4
			img.Pix[di+0] = r.canvas[si+2] // R
			img.Pix[di+1] = r.canvas[si+1] // G
			img.Pix[di+2] = r.canvas[si+0] // B
			img.Pix[di+3] = r.canvas[si+3] // A
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func blitBGRA(dst []byte, stride int, tile []byte, x, y, w, h int) error {
	if w <= 0 || h <= 0 || x < 0 || y < 0 {
		return fmt.Errorf("bad blit rect")
	}
	if w*h*4 != len(tile) {
		return fmt.Errorf("tile size mismatch: %d != %d", len(tile), w*h*4)
	}
	if x+w > stride/4 {
		return fmt.Errorf("blit x overflow")
	}
	for row := 0; row < h; row++ {
		dstOff := (y+row)*stride + x*4
		srcOff := row * w * 4
		if dstOff < 0 || dstOff+w*4 > len(dst) {
			return fmt.Errorf("blit out of bounds")
		}
		copy(dst[dstOff:dstOff+w*4], tile[srcOff:srcOff+w*4])
	}
	return nil
}
