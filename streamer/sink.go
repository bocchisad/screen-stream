package streamer

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/salfetka/screen-stream/engine"
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

func (s *LocalSink) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	return nil
}
