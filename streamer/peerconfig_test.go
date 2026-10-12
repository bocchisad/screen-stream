package streamer

import "testing"

func TestPeerConfig(t *testing.T) {
	empty := PeerConfig("")
	if len(empty.ICEServers) != 0 {
		t.Fatalf("want no ICE servers, got %v", empty.ICEServers)
	}
	with := PeerConfig("stun:stun.l.google.com:19302")
	if len(with.ICEServers) != 1 || len(with.ICEServers[0].URLs) != 1 {
		t.Fatalf("want one STUN server, got %+v", with.ICEServers)
	}
	if with.ICEServers[0].URLs[0] != "stun:stun.l.google.com:19302" {
		t.Fatalf("url=%q", with.ICEServers[0].URLs[0])
	}
}
