package streamer

// PeerMetrics is a snapshot of wire counters from send/recv.
type PeerMetrics struct {
	Peer      string `json:"peer,omitempty"`
	BytesSent uint64 `json:"bytesSent"`
	BytesRecv uint64 `json:"bytesRecv"`
	Sent      uint64 `json:"sent"`
	Recv      uint64 `json:"recv"`
	Dropped   uint64 `json:"dropped"`
}

// MetricsFromSession builds a PeerMetrics from a live WebRTCSession.
func MetricsFromSession(peer string, s *WebRTCSession) PeerMetrics {
	if s == nil {
		return PeerMetrics{Peer: peer}
	}
	return PeerMetrics{
		Peer:      peer,
		BytesSent: s.BytesSent(),
		BytesRecv: s.BytesRecv(),
		Sent:      s.Sent(),
		Recv:      s.Recv(),
		Dropped:   s.Dropped(),
	}
}
