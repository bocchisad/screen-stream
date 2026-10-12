package streamer

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/bocchisad/screen-stream/engine"
)

// Sink is a non-blocking consumer of DirtyPackets.
type Sink interface {
	TryEnqueue(pkt *engine.DirtyPacket)
	Start(ctx context.Context) error
	Stop() error
	Sent() uint64
	Dropped() uint64
}

// LocalSink drains the ring and optionally writes packed bytes to Writer.
type LocalSink struct {
	ring   *engine.DeliveryRing
	w      io.Writer
	sent   atomic.Uint64
	cancel context.CancelFunc
	wg     sync.WaitGroup
	packBuf []byte
}

func NewLocalSink(depth int, w io.Writer) *LocalSink {
	return &LocalSink{ring: engine.NewDeliveryRing(depth), w: w, packBuf: make([]byte, 0, 4<<20)}
}

func (s *LocalSink) TryEnqueue(pkt *engine.DirtyPacket) { s.ring.TryEnqueue(pkt) }
func (s *LocalSink) Dropped() uint64                    { return s.ring.Dropped() }
func (s *LocalSink) Sent() uint64                       { return s.sent.Load() }
func (s *LocalSink) Ring() *engine.DeliveryRing         { return s.ring }

func (s *LocalSink) Start(ctx context.Context) error {
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case pkt, ok := <-s.ring.Dequeue():
				if !ok {
					return
				}
				if s.w != nil {
					n := PackSize(pkt)
					if cap(s.packBuf) < n {
						s.packBuf = make([]byte, n)
					}
					buf := s.packBuf[:n]
					_, _ = PackInto(pkt, buf)
					_, _ = s.w.Write(buf)
				}
				s.sent.Add(1)
				pkt.Release()
			}
		}
	}()
	return nil
}

// FanoutSink enqueues the same DirtyPacket to multiple WebRTC sessions (multi-viewer).
type FanoutSink struct {
	sessions []*WebRTCSession
}

func NewFanoutSink(sessions ...*WebRTCSession) *FanoutSink {
	return &FanoutSink{sessions: sessions}
}

func (f *FanoutSink) Start(ctx context.Context) error { _ = ctx; return nil }
func (f *FanoutSink) Stop() error {
	for _, s := range f.sessions {
		if s != nil {
			_ = s.Stop()
		}
	}
	return nil
}

func (f *FanoutSink) TryEnqueue(pkt *engine.DirtyPacket) {
	if pkt == nil || len(f.sessions) == 0 {
		return
	}
	// First session owns the pooled packet; others get heap clones (pool=nil → Release no-op).
	for i, s := range f.sessions {
		if s == nil {
			continue
		}
		if i == 0 {
			s.TryEnqueue(pkt)
			continue
		}
		s.TryEnqueue(cloneDirtyPacket(pkt))
	}
}

func (f *FanoutSink) Dropped() uint64 {
	var n uint64
	for _, s := range f.sessions {
		if s != nil {
			n += s.Dropped()
		}
	}
	return n
}

func (f *FanoutSink) Sent() uint64 {
	var n uint64
	for _, s := range f.sessions {
		if s != nil {
			n += s.Sent()
		}
	}
	return n
}

func (f *FanoutSink) BytesSent() uint64 {
	var n uint64
	for _, s := range f.sessions {
		if s != nil {
			n += s.BytesSent()
		}
	}
	return n
}

func cloneDirtyPacket(pkt *engine.DirtyPacket) *engine.DirtyPacket {
	if pkt == nil {
		return nil
	}
	cp := &engine.DirtyPacket{
		FrameID:   pkt.FrameID,
		PTSUs:     pkt.PTSUs,
		Width:     pkt.Width,
		Height:    pkt.Height,
		BlockSize: pkt.BlockSize,
		Flags:     pkt.Flags,
		Regions:   append([]engine.DirtyRegion(nil), pkt.Regions...),
		Payload:   append([]byte(nil), pkt.Payload...),
	}
	return cp
}

func (s *LocalSink) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	return nil
}
