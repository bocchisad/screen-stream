package streamer

import (
	"context"
	"encoding/json"
)

// ICECandidateJSON is a trickle ICE candidate on the wire.
type ICECandidateJSON struct {
	Candidate     string `json:"candidate"`
	SDPMid        string `json:"sdpMid"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex"`
}

// Signaling exchanges SDP/ICE. Loopback used for local e2e.
type Signaling interface {
	SendOffer(sdp string) error
	RecvOffer(ctx context.Context) (string, error)
	SendAnswer(sdp string) error
	RecvAnswer(ctx context.Context) (string, error)
	SendICE(fromOfferer bool, c ICECandidateJSON) error
	RecvICE(ctx context.Context, fromOfferer bool) (ICECandidateJSON, error)
}

// LoopbackSignaling connects two local peers in-process.
type LoopbackSignaling struct {
	offer     chan string
	answer    chan string
	iceOffer  chan ICECandidateJSON // offerer → answerer
	iceAnswer chan ICECandidateJSON // answerer → offerer
}

func NewLoopbackSignaling() *LoopbackSignaling {
	return &LoopbackSignaling{
		offer:     make(chan string, 1),
		answer:    make(chan string, 1),
		iceOffer:  make(chan ICECandidateJSON, 32),
		iceAnswer: make(chan ICECandidateJSON, 32),
	}
}

func (s *LoopbackSignaling) SendOffer(sdp string) error  { s.offer <- sdp; return nil }
func (s *LoopbackSignaling) SendAnswer(sdp string) error { s.answer <- sdp; return nil }

func (s *LoopbackSignaling) RecvOffer(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case v := <-s.offer:
		return v, nil
	}
}

func (s *LoopbackSignaling) RecvAnswer(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case v := <-s.answer:
		return v, nil
	}
}

func (s *LoopbackSignaling) SendICE(fromOfferer bool, c ICECandidateJSON) error {
	if fromOfferer {
		select {
		case s.iceOffer <- c:
			return nil
		default:
			return nil
		}
	}
	select {
	case s.iceAnswer <- c:
		return nil
	default:
		return nil
	}
}

func (s *LoopbackSignaling) RecvICE(ctx context.Context, fromOfferer bool) (ICECandidateJSON, error) {
	ch := s.iceAnswer
	if fromOfferer {
		ch = s.iceOffer
	}
	select {
	case <-ctx.Done():
		return ICECandidateJSON{}, ctx.Err()
	case c := <-ch:
		return c, nil
	}
}

// EncodeICE / DecodeICE helpers for text transports.
func EncodeICE(c ICECandidateJSON) ([]byte, error) { return json.Marshal(c) }
func DecodeICE(b []byte) (ICECandidateJSON, error) {
	var c ICECandidateJSON
	err := json.Unmarshal(b, &c)
	return c, err
}
