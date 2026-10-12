package streamer

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestHubMetricsStatus(t *testing.T) {
	hub := NewHTTPSignalingHub()
	hub.Token = "secret"
	url, err := hub.ListenAndServe("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	cli := NewHTTPSignalingClientPeer(url, "secret", "default")
	if err := cli.PostMetrics(PeerMetrics{Peer: "default", BytesSent: 42, Sent: 3}); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet, url+"/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var st map[string]any
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	m, ok := st["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("metrics missing: %+v", st)
	}
	if m["bytesSent"].(float64) != 42 {
		t.Fatalf("bytesSent=%v", m["bytesSent"])
	}
	_ = time.Second
}
