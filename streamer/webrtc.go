package streamer

import (
	"context"
	"crypto/cipher"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/salfetka/screen-stream/engine"
)

const DefaultChannelLabel = "screen-frames"

// WebRTCSession is one side of a P2P DirtyPacket pipe over a DataChannel.
type WebRTCSession struct {
	ring      *engine.DeliveryRing
	label     string
	pc        *webrtc.PeerConnection
	dc        *webrtc.DataChannel
	sent      atomic.Uint64
	recv      atomic.Uint64
	bytesSent atomic.Uint64
	bytesRecv atomic.Uint64
	onPacket  func([]byte) // full reassembled DirtyPacket wire
	cancel    context.CancelFunc
	iceCancel context.CancelFunc
	wg        sync.WaitGroup
	packBuf   []byte
	chunkBuf  []byte
	sendCopy  []byte // reused per-chunk copy for dc.Send
	mu        sync.Mutex
	dcOpen    chan struct{}
	dcOnce    sync.Once
	offerer   bool
	compress  bool
	chunkSize int
	reasm     *ChunkReassembler
	aead      cipher.AEAD
	aeadBuf   []byte
	seed      uint64
}

type SessionConfig struct {
	RingDepth int
	Label     string
	OnPacket  func([]byte) // inbound DirtyPacket wire after reassembly (+ AEAD open)
	Compress  bool         // zlib payload before send
	ChunkSize int          // 0 → DefaultChunkSize
	Seed      uint64       // shared seed → wire AEAD key (0 → DefaultXORSeed)
	DisableAEAD bool       // test-only: send plaintext DirtyPackets
}

func NewWebRTCSession(cfg SessionConfig) *WebRTCSession {
	if cfg.RingDepth < 1 {
		cfg.RingDepth = 3
	}
	if cfg.Label == "" {
		cfg.Label = DefaultChannelLabel
	}
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = DefaultChunkSize
	}
	if cfg.Seed == 0 {
		cfg.Seed = engine.DefaultXORSeed
	}
	s := &WebRTCSession{
		ring:      engine.NewDeliveryRing(cfg.RingDepth),
		label:     cfg.Label,
		onPacket:  cfg.OnPacket,
		compress:  cfg.Compress,
		chunkSize: cfg.ChunkSize,
		seed:      cfg.Seed,
		packBuf:   make([]byte, 0, 8<<20),
		chunkBuf:  make([]byte, ChunkHeaderSize+DefaultChunkSize),
		sendCopy:  make([]byte, ChunkHeaderSize+DefaultChunkSize),
		aeadBuf:   make([]byte, 0, 8<<20),
		dcOpen:    make(chan struct{}),
	}
	if !cfg.DisableAEAD {
		aead, err := NewWireAEAD(cfg.Seed)
		if err == nil {
			s.aead = aead
		}
	}
	s.reasm = NewChunkReassemblerSize(func(full []byte) {
		s.recv.Add(1)
		s.bytesRecv.Add(uint64(len(full)))
		plain, err := OpenWire(s.aead, full)
		if err != nil {
			return
		}
		if s.onPacket != nil {
			s.onPacket(plain)
		}
	}, s.chunkSize)
	return s
}

func (s *WebRTCSession) TryEnqueue(pkt *engine.DirtyPacket) { s.ring.TryEnqueue(pkt) }
func (s *WebRTCSession) Sent() uint64                       { return s.sent.Load() }
func (s *WebRTCSession) Recv() uint64                       { return s.recv.Load() }
func (s *WebRTCSession) BytesSent() uint64                  { return s.bytesSent.Load() }
func (s *WebRTCSession) BytesRecv() uint64                  { return s.bytesRecv.Load() }
func (s *WebRTCSession) Dropped() uint64                    { return s.ring.Dropped() }

func defaultPCConfig() webrtc.Configuration {
	return webrtc.Configuration{}
}

func newPeerConnection() (*webrtc.PeerConnection, error) {
	se := webrtc.SettingEngine{}
	se.SetSCTPMaxMessageSize(16 << 20)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	return api.NewPeerConnection(defaultPCConfig())
}

func (s *WebRTCSession) DialOfferer(ctx context.Context, sig Signaling) error {
	s.offerer = true
	pc, err := newPeerConnection()
	if err != nil {
		return err
	}
	s.pc = pc

	ordered := true
	// Ordered + reliable (no MaxRetransmits) — required for chunk reassembly.
	dc, err := pc.CreateDataChannel(s.label, &webrtc.DataChannelInit{Ordered: &ordered})
	if err != nil {
		_ = pc.Close()
		return err
	}
	s.attachDC(dc)
	s.wireICE(ctx, sig, true)

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return err
	}
	<-webrtc.GatheringCompletePromise(pc)
	if err := sig.SendOffer(pc.LocalDescription().SDP); err != nil {
		return err
	}

	answerSDP, err := sig.RecvAnswer(ctx)
	if err != nil {
		return err
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answerSDP}); err != nil {
		return err
	}
	return s.waitDC(ctx)
}

func (s *WebRTCSession) DialAnswerer(ctx context.Context, sig Signaling) error {
	s.offerer = false
	pc, err := newPeerConnection()
	if err != nil {
		return err
	}
	s.pc = pc
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		s.attachDC(dc)
	})
	s.wireICE(ctx, sig, false)

	offerSDP, err := sig.RecvOffer(ctx)
	if err != nil {
		return err
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		return err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return err
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return err
	}
	<-webrtc.GatheringCompletePromise(pc)
	if err := sig.SendAnswer(pc.LocalDescription().SDP); err != nil {
		return err
	}
	return s.waitDC(ctx)
}

func (s *WebRTCSession) wireICE(ctx context.Context, sig Signaling, offerer bool) {
	_ = ctx
	iceCtx, iceCancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.iceCancel = iceCancel
	s.mu.Unlock()
	s.pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		j := c.ToJSON()
		mid := ""
		if j.SDPMid != nil {
			mid = *j.SDPMid
		}
		_ = sig.SendICE(offerer, ICECandidateJSON{
			Candidate:     j.Candidate,
			SDPMid:        mid,
			SDPMLineIndex: j.SDPMLineIndex,
		})
	})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := sig.RecvICE(iceCtx, !offerer)
			if err != nil {
				return
			}
			var mid *string
			if c.SDPMid != "" {
				m := c.SDPMid
				mid = &m
			}
			cand := webrtc.ICECandidateInit{
				Candidate:     c.Candidate,
				SDPMid:        mid,
				SDPMLineIndex: c.SDPMLineIndex,
			}
			if s.pc != nil {
				_ = s.pc.AddICECandidate(cand)
			}
		}
	}()
}

func (s *WebRTCSession) attachDC(dc *webrtc.DataChannel) {
	s.mu.Lock()
	s.dc = dc
	s.mu.Unlock()
	dc.OnOpen(func() {
		s.dcOnce.Do(func() { close(s.dcOpen) })
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if msg.IsString {
			return
		}
		// ChunkReassembler copies into pooled buffers before returning;
		// Push must finish before this callback returns (pion may recycle Data).
		s.reasm.Push(msg.Data)
	})
}

func (s *WebRTCSession) waitDC(ctx context.Context) error {
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("datachannel wait canceled: %w (is the other peer running? hub+/send+/recv)", ctx.Err())
	case <-s.dcOpen:
		return nil
	case <-timer.C:
		return fmt.Errorf("datachannel open timeout (start hub, then send + recv together; restart hub if stale)")
	}
}

func (s *WebRTCSession) Start(ctx context.Context) error {
	return s.StartSendLoop(ctx)
}

func (s *WebRTCSession) StartSendLoop(ctx context.Context) error {
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
				s.sendPacket(pkt)
				pkt.Release()
			}
		}
	}()
	return nil
}

func (s *WebRTCSession) sendPacket(pkt *engine.DirtyPacket) {
	s.mu.Lock()
	dc := s.dc
	s.mu.Unlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}
	if s.compress {
		_ = CompressPayload(pkt)
	}
	n := PackSize(pkt)
	if cap(s.packBuf) < n {
		s.packBuf = make([]byte, n)
	}
	buf := s.packBuf[:n]
	if _, err := PackInto(pkt, buf); err != nil {
		return
	}
	wire := buf
	if s.aead != nil {
		sealed, err := SealWire(s.aead, buf, s.aeadBuf)
		if err != nil {
			return
		}
		s.aeadBuf = sealed
		wire = sealed
	}
	id := NextMsgID()
	var wireBytes int
	_ = SplitWire(id, wire, s.chunkSize, s.chunkBuf, func(chunk []byte) error {
		if cap(s.sendCopy) < len(chunk) {
			s.sendCopy = make([]byte, len(chunk))
		}
		cp := s.sendCopy[:len(chunk)]
		copy(cp, chunk)
		wireBytes += len(cp)
		return dc.Send(cp)
	})
	s.sent.Add(1)
	s.bytesSent.Add(uint64(wireBytes))
}

func (s *WebRTCSession) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Lock()
	if s.iceCancel != nil {
		s.iceCancel()
	}
	if s.dc != nil {
		_ = s.dc.Close()
	}
	pc := s.pc
	s.mu.Unlock()
	if pc != nil {
		_ = pc.Close()
	}
	s.wg.Wait()
	return nil
}

// ConnectLoopback brings up offerer+answerer in-process.
func ConnectLoopback(ctx context.Context, onPacket func([]byte)) (sender, receiver *WebRTCSession, err error) {
	return ConnectLoopbackOpts(ctx, SessionConfig{Compress: true, OnPacket: onPacket})
}

func ConnectLoopbackOpts(ctx context.Context, cfg SessionConfig) (sender, receiver *WebRTCSession, err error) {
	sig := NewLoopbackSignaling()
	sendCfg := cfg
	sendCfg.OnPacket = nil
	recvCfg := cfg
	sender = NewWebRTCSession(sendCfg)
	receiver = NewWebRTCSession(recvCfg)

	errCh := make(chan error, 2)
	go func() { errCh <- receiver.DialAnswerer(ctx, sig) }()
	go func() { errCh <- sender.DialOfferer(ctx, sig) }()

	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			_ = sender.Stop()
			_ = receiver.Stop()
			return nil, nil, ctx.Err()
		case e := <-errCh:
			if e != nil {
				_ = sender.Stop()
				_ = receiver.Stop()
				return nil, nil, e
			}
		}
	}
	if err := sender.StartSendLoop(context.Background()); err != nil {
		_ = sender.Stop()
		_ = receiver.Stop()
		return nil, nil, err
	}
	return sender, receiver, nil
}

type DiscardWriter struct{ N atomic.Uint64 }

func (d *DiscardWriter) Write(p []byte) (int, error) {
	d.N.Add(uint64(len(p)))
	return len(p), nil
}

var _ io.Writer = (*DiscardWriter)(nil)
