package streamer

import (
	"context"
	"encoding/binary"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/bocchisad/screen-stream/engine"
)

// H264Sink pushes Annex-B access units to a WebRTC video track (SRTP).
type H264Sink struct {
	track   *webrtc.TrackLocalStaticSample
	sent    atomic.Uint64
	dropped atomic.Uint64
	bytes   atomic.Uint64
	mu      sync.Mutex
	closed  bool
}

func newH264Track() (*webrtc.TrackLocalStaticSample, error) {
	return webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		},
		"video",
		"screen-stream",
	)
}

func (s *H264Sink) TryEnqueueAU(au engine.EncodedFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.track == nil || len(au.Data) == 0 {
		s.dropped.Add(1)
		return
	}
	dur := au.Duration
	if dur <= 0 {
		dur = time.Second / 30
	}
	if err := s.track.WriteSample(media.Sample{Data: au.Data, Duration: dur}); err != nil {
		s.dropped.Add(1)
		return
	}
	s.sent.Add(1)
	s.bytes.Add(uint64(len(au.Data)))
}

func (s *H264Sink) Sent() uint64    { return s.sent.Load() }
func (s *H264Sink) Dropped() uint64 { return s.dropped.Load() }
func (s *H264Sink) BytesSent() uint64 {
	return s.bytes.Load()
}

func (s *H264Sink) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// registerH264Codecs installs H264 into a MediaEngine (Baseline/Main, packetization-mode=1).
func registerH264Codecs(m *webrtc.MediaEngine) error {
	for _, codec := range []webrtc.RTPCodecParameters{
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:     webrtc.MimeTypeH264,
				ClockRate:    90000,
				Channels:     0,
				SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=4d001f",
				RTCPFeedback: nil,
			},
			PayloadType: 96,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    webrtc.MimeTypeH264,
				ClockRate:   90000,
				SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			},
			PayloadType: 97,
		},
	} {
		if err := m.RegisterCodec(codec, webrtc.RTPCodecTypeVideo); err != nil {
			return err
		}
	}
	return nil
}

func newPeerConnectionMedia(stunURL string, h264 bool) (*webrtc.PeerConnection, error) {
	se := webrtc.SettingEngine{}
	se.SetSCTPMaxMessageSize(16 << 20)
	m := &webrtc.MediaEngine{}
	if h264 {
		if err := registerH264Codecs(m); err != nil {
			return nil, err
		}
	} else {
		// DataChannel-only sessions still need a MediaEngine for pion v4.
		if err := m.RegisterDefaultCodecs(); err != nil {
			return nil, err
		}
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se), webrtc.WithMediaEngine(m))
	return api.NewPeerConnection(PeerConfig(stunURL))
}

// h264Depacketizer reassembles RTP H264 (single NAL / STAP-A / FU-A) into Annex-B AUs.
type h264Depacketizer struct {
	fuBuf   []byte
	pending []byte
}

func (d *h264Depacketizer) Push(pkt *rtp.Packet) (aus [][]byte) {
	p := pkt.Payload
	if len(p) < 1 {
		return nil
	}
	nalType := p[0] & 0x1f
	switch {
	case nalType >= 1 && nalType <= 23:
		au := make([]byte, 4+len(p))
		au[0], au[1], au[2], au[3] = 0, 0, 0, 1
		copy(au[4:], p)
		return [][]byte{au}
	case nalType == 24: // STAP-A
		off := 1
		for off+2 <= len(p) {
			n := int(binary.BigEndian.Uint16(p[off:]))
			off += 2
			if off+n > len(p) {
				break
			}
			au := make([]byte, 4+n)
			au[0], au[1], au[2], au[3] = 0, 0, 0, 1
			copy(au[4:], p[off:off+n])
			aus = append(aus, au)
			off += n
		}
		return aus
	case nalType == 28: // FU-A
		if len(p) < 2 {
			return nil
		}
		fuHeader := p[1]
		start := fuHeader&0x80 != 0
		end := fuHeader&0x40 != 0
		nalHeader := (p[0] & 0xe0) | (fuHeader & 0x1f)
		if start {
			d.fuBuf = d.fuBuf[:0]
			d.fuBuf = append(d.fuBuf, 0, 0, 0, 1, nalHeader)
			d.fuBuf = append(d.fuBuf, p[2:]...)
		} else if len(d.fuBuf) > 0 {
			d.fuBuf = append(d.fuBuf, p[2:]...)
		}
		if end && len(d.fuBuf) > 0 {
			au := append([]byte(nil), d.fuBuf...)
			d.fuBuf = d.fuBuf[:0]
			return [][]byte{au}
		}
		return nil
	default:
		return nil
	}
}

// ReadRemoteH264 consumes a remote video track and invokes onAU per Annex-B access unit.
func ReadRemoteH264(ctx context.Context, track *webrtc.TrackRemote, onAU func([]byte)) error {
	dep := &h264Depacketizer{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		pkt, _, err := track.ReadRTP()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		for _, au := range dep.Push(pkt) {
			onAU(au)
		}
	}
}

// IsH264Keyframe reports whether Annex-B contains an IDR (type 5) NAL.
func IsH264Keyframe(annexB []byte) bool {
	i := 0
	for i+4 < len(annexB) {
		if annexB[i] == 0 && annexB[i+1] == 0 && annexB[i+2] == 0 && annexB[i+3] == 1 {
			i += 4
		} else if annexB[i] == 0 && annexB[i+1] == 0 && annexB[i+2] == 1 {
			i += 3
		} else {
			i++
			continue
		}
		if i >= len(annexB) {
			break
		}
		t := annexB[i] & 0x1f
		if t == 5 {
			return true
		}
		// skip to next start code
		j := i + 1
		for j+3 < len(annexB) {
			if annexB[j] == 0 && annexB[j+1] == 0 && (annexB[j+2] == 1 || (j+3 < len(annexB) && annexB[j+2] == 0 && annexB[j+3] == 1)) {
				break
			}
			j++
		}
		i = j
	}
	return false
}
