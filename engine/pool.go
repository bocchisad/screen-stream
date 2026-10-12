package engine

import (
	"sync/atomic"
)

// BufferPool is a fixed freelist of frame buffers (GC cannot drop slots).
type BufferPool struct {
	size int
	free chan []byte
	gets atomic.Uint64
	puts atomic.Uint64
}

func NewBufferPool(frameBytes int) *BufferPool {
	const slots = 8
	bp := &BufferPool{
		size: frameBytes,
		free: make(chan []byte, slots),
	}
	for i := 0; i < slots; i++ {
		bp.free <- make([]byte, frameBytes)
	}
	return bp
}

func (p *BufferPool) Size() int { return p.size }

func (p *BufferPool) Acquire() []byte {
	p.gets.Add(1)
	select {
	case b := <-p.free:
		return b[:p.size]
	default:
		// Overflow path (should be rare after sizing ring correctly).
		return make([]byte, p.size)
	}
}

func (p *BufferPool) Release(b []byte) {
	if b == nil || cap(b) < p.size {
		return
	}
	p.puts.Add(1)
	b = b[:p.size]
	select {
	case p.free <- b:
	default:
		// Freelist full — drop reference (GC).
	}
}

func (p *BufferPool) Stats() (gets, puts uint64) {
	return p.gets.Load(), p.puts.Load()
}

func (p *BufferPool) NewFrame(meta FrameMeta, pixels []byte) *Frame {
	return &Frame{Meta: meta, Pixels: pixels, pool: p}
}

// PacketPool is a fixed freelist of DirtyPackets with pre-sized backing stores.
type PacketPool struct {
	payloadSize int
	maxRegions  int
	free        chan *DirtyPacket
}

func NewPacketPool(payloadSize, maxRegions int) *PacketPool {
	const slots = 8
	pp := &PacketPool{
		payloadSize: payloadSize,
		maxRegions:  maxRegions,
		free:        make(chan *DirtyPacket, slots),
	}
	for i := 0; i < slots; i++ {
		pp.free <- &DirtyPacket{
			Regions: make([]DirtyRegion, maxRegions),
			Payload: make([]byte, payloadSize),
			raw:     make([]byte, 64+maxRegions*16+payloadSize),
			pool:    pp,
		}
	}
	return pp
}

func (p *PacketPool) Acquire() *DirtyPacket {
	var pkt *DirtyPacket
	select {
	case pkt = <-p.free:
	default:
		// Overflow — never block capture hot path.
		pkt = &DirtyPacket{
			Regions: make([]DirtyRegion, 0, p.maxRegions),
			Payload: make([]byte, 0, p.payloadSize),
			raw:     make([]byte, 0, 64+p.maxRegions*16+p.payloadSize),
		}
	}
	if cap(pkt.Regions) < p.maxRegions {
		pkt.Regions = make([]DirtyRegion, 0, p.maxRegions)
	}
	if cap(pkt.Payload) < p.payloadSize {
		pkt.Payload = make([]byte, 0, p.payloadSize)
	}
	pkt.Regions = pkt.Regions[:0]
	pkt.Payload = pkt.Payload[:0]
	pkt.raw = pkt.raw[:0]
	pkt.Magic = PacketMagic
	pkt.Version = 1
	pkt.Flags = 0
	pkt.pool = p
	return pkt
}

func (p *PacketPool) Release(pkt *DirtyPacket) {
	if pkt == nil || pkt.pool != p {
		return
	}
	pkt.Regions = pkt.Regions[:0]
	pkt.Payload = pkt.Payload[:0]
	pkt.raw = pkt.raw[:0]
	select {
	case p.free <- pkt:
	default:
	}
}

// DeliveryRing is a fixed-depth non-blocking handoff (drop-oldest).
type DeliveryRing struct {
	ch      chan *DirtyPacket
	dropped atomic.Uint64
}

func NewDeliveryRing(depth int) *DeliveryRing {
	if depth < 1 {
		depth = 3
	}
	return &DeliveryRing{ch: make(chan *DirtyPacket, depth)}
}

func (r *DeliveryRing) TryEnqueue(pkt *DirtyPacket) {
	for {
		select {
		case r.ch <- pkt:
			return
		default:
			select {
			case old := <-r.ch:
				old.Release()
				r.dropped.Add(1)
			default:
			}
		}
	}
}

func (r *DeliveryRing) Dequeue() <-chan *DirtyPacket { return r.ch }

func (r *DeliveryRing) Dropped() uint64 { return r.dropped.Load() }
