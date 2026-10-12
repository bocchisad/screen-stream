package streamer

import (
	"context"
	"encoding/json"
)

// ICECandidateJSON is a trickle ICE candidate on the wire.
type ICECandidateJSON struct {
	Candidate     string  `json:"candidate"`
	SDPMid        string  `json:"sdpMid"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex"`
}

// Signaling exchanges SDP+ECDH pubkeys and ICE.
type Signaling interface {
	SendOffer(d SessionDesc) error
	RecvOffer(ctx context.Context) (SessionDesc, error)
	SendAnswer(d SessionDesc) error
	RecvAnswer(ctx context.Context) (SessionDesc, error)
	SendICE(fromOfferer bool, c ICECandidateJSON) error
	RecvICE(ctx context.Context, fromOfferer bool) (ICECandidateJSON, error)
}

// LoopbackSignaling connects two local peers in-process.
type LoopbackSignaling struct {
	offer     chan SessionDesc
	answer    chan SessionDesc
	iceOffer  chan ICECandidateJSON
	iceAnswer chan ICECandidateJSON
}

func NewLoopbackSignaling() *LoopbackSignaling {
	return &LoopbackSignaling{
		offer:     make(chan SessionDesc, 1),
		answer:    make(chan SessionDesc, 1),
		iceOffer:  make(chan ICECandidateJSON, 32),
		iceAnswer: make(chan ICECandidateJSON, 32),
	}
}

func (s *LoopbackSignaling) SendOffer(d SessionDesc) error  { s.offer <- d; return nil }
func (s *LoopbackSignaling) SendAnswer(d SessionDesc) error { s.answer <- d; return nil }

func (s *LoopbackSignaling) RecvOffer(ctx context.Context) (SessionDesc, error) {
	select {
	case <-ctx.Done():
		return SessionDesc{}, ctx.Err()
	case v := <-s.offer:
		return v, nil
	}
}

func (s *LoopbackSignaling) RecvAnswer(ctx context.Context) (SessionDesc, error) {
	select {
	case <-ctx.Done():
		return SessionDesc{}, ctx.Err()
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

func EncodeICE(c ICECandidateJSON) ([]byte, error) { return json.Marshal(c) }
func DecodeICE(b []byte) (ICECandidateJSON, error) {
	var c ICECandidateJSON
	err := json.Unmarshal(b, &c)
	return c, err
}
