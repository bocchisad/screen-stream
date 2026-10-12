package engine

import (
	"context"
	"sync"
)

// StreamPipeline: diff → redact → protect tiles → packetize.
type StreamPipeline struct {
	mu          sync.Mutex
	blockSize   int
	packetPool  *PacketPool
	detector    PIIDetector
	prevHashes  []uint64
	currHashes  []uint64
	rectScratch []uint16
	havePrev    bool
	frameCount  uint64
	keyEvery    uint64
	width       int
	height      int
	stride      int
	seed        uint64
	hitScratch  []Rect // reused for detector output when supported
}

type PipelineConfig struct {
	Width     int
	Height    int
	Stride    int // bytes/row; 0 → Width*4
	BlockSize int
	KeyEvery  uint64
	Detector  PIIDetector
	XORSeed   uint64
}

func NewPipeline(cfg PipelineConfig) *StreamPipeline {
	if cfg.BlockSize <= 0 {
		cfg.BlockSize = 64
	}
	if cfg.KeyEvery == 0 {
		cfg.KeyEvery = 120
	}
	if cfg.XORSeed == 0 {
		cfg.XORSeed = DefaultXORSeed
	}
	if cfg.Stride <= 0 {
		cfg.Stride = cfg.Width * 4
	}
	bx := (cfg.Width + cfg.BlockSize - 1) / cfg.BlockSize
	by := (cfg.Height + cfg.BlockSize - 1) / cfg.BlockSize
	nBlocks := bx * by
	// Payload tiles are tightly packed (w*h*4). Process works in-place on frame.Pixels
	// (no full-frame scratch copy).
	tileBytes := cfg.Width * cfg.Height * 4
	return &StreamPipeline{
		blockSize:   cfg.BlockSize,
		packetPool:  NewPacketPool(tileBytes, nBlocks),
		detector:    cfg.Detector,
		prevHashes:  make([]uint64, nBlocks),
		currHashes:  make([]uint64, nBlocks),
		rectScratch: make([]uint16, nBlocks*4),
		keyEvery:    cfg.KeyEvery,
		width:       cfg.Width,
		height:      cfg.Height,
		stride:      cfg.Stride,
		seed:        cfg.XORSeed,
		hitScratch:  make([]Rect, 0, 32),
	}
}

func frameKey(seed, frameID uint64) uint64 {
	return seed ^ frameID
}

func (p *StreamPipeline) Process(ctx context.Context, frame *Frame) (*DirtyPacket, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	meta := frame.Meta
	w, h, stride := int(meta.Width), int(meta.Height), meta.Stride
	if stride == 0 {
		stride = w * 4
	}
	need := stride * h
	if len(frame.Pixels) < need {
		return nil, ErrBufferTooSmall
	}
	pix := frame.Pixels[:need]
	fk := frameKey(p.seed, meta.FrameID)

	p.frameCount++
	force := !p.havePrev || (p.frameCount%p.keyEvery == 0)
	n := hashAndDiff(pix, w, h, stride, p.blockSize,
		p.currHashes, p.prevHashes, p.havePrev, force, p.rectScratch)

	if p.detector != nil {
		meta.BlockSize = uint16(p.blockSize)
		rects, err := p.detector.Detect(ctx, meta, pix)
		if err == nil {
			for _, r := range rects {
				fillBlack(pix, stride, int(r.X), int(r.Y), int(r.W), int(r.H))
			}
		}
	}

	pkt := p.packetPool.Acquire()
	pkt.FrameID = meta.FrameID
	pkt.PTSUs = uint64(meta.PTS.Microseconds())
	pkt.Width = meta.Width
	pkt.Height = meta.Height
	pkt.BlockSize = uint16(p.blockSize)
	if force {
		pkt.Flags |= FlagKeyframe
	}

	// Write tiles directly into pre-sized pkt.Payload (no append / no second buffer).
	payloadCap := cap(pkt.Payload)
	payloadLen := 0
	regions := pkt.Regions[:0]
	if cap(regions) < n {
		n = cap(regions)
	}
	for i := 0; i < n; i++ {
		x := int(p.rectScratch[i*4+0])
		y := int(p.rectScratch[i*4+1])
		rw := int(p.rectScratch[i*4+2])
		rh := int(p.rectScratch[i*4+3])
		tileBytes := rw * rh * 4
		if payloadLen+tileBytes > payloadCap {
			break
		}
		dst := pkt.Payload[:payloadCap][payloadLen : payloadLen+tileBytes]
		extractTileInto(pix, stride, x, y, rw, rh, dst)
		tk := fk ^ (uint64(x)<<32 | uint64(y))
		xorBuffer(dst, &tk)
		regions = regions[:len(regions)+1]
		regions[len(regions)-1] = DirtyRegion{
			Rect:          Rect{X: uint16(x), Y: uint16(y), W: uint16(rw), H: uint16(rh)},
			PayloadOffset: uint32(payloadLen),
			PayloadLen:    uint32(tileBytes),
		}
		payloadLen += tileBytes
	}
	pkt.Regions = regions
	pkt.Payload = pkt.Payload[:payloadLen]

	// Wipe capture buffer in place (no separate plaintext scratch).
	k := fk
	xorBuffer(pix, &k)
	frame.Protected = true

	copy(p.prevHashes, p.currHashes)
	p.havePrev = true
	return pkt, nil
}

func extractTileInto(src []byte, stride, x, y, w, h int, dst []byte) {
	for row := 0; row < h; row++ {
		srcOff := (y+row)*stride + x*4
		dstOff := row * w * 4
		copy(dst[dstOff:dstOff+w*4], src[srcOff:srcOff+w*4])
	}
}

func (p *StreamPipeline) Close() error { return nil }

var _ Pipeline = (*StreamPipeline)(nil)
