package streamer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

type sdpBody struct {
	SDP string `json:"sdp"`
}

type iceBody struct {
	FromOfferer bool             `json:"fromOfferer"`
	Candidate   ICECandidateJSON `json:"candidate"`
}

// HTTPSignalingHub is a tiny local signaling server for two peers.
type HTTPSignalingHub struct {
	mu     sync.Mutex
	offer  string
	answer string
	ice    []iceBody
	cond   *sync.Cond
	srv    *http.Server
	addr   string
}

func NewHTTPSignalingHub() *HTTPSignalingHub {
	h := &HTTPSignalingHub{}
	h.cond = sync.NewCond(&h.mu)
	return h
}

func (h *HTTPSignalingHub) ListenAndServe(addr string) (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/offer", h.handleOffer)
	mux.HandleFunc("/answer", h.handleAnswer)
	mux.HandleFunc("/ice", h.handleICE)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	h.addr = ln.Addr().String()
	h.srv = &http.Server{Handler: mux}
	go func() { _ = h.srv.Serve(ln) }()
	return "http://" + h.addr, nil
}

func (h *HTTPSignalingHub) Close() error {
	if h.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.srv.Shutdown(ctx)
}

func (h *HTTPSignalingHub) handleOffer(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var b sdpBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		h.mu.Lock()
		h.offer = b.SDP
		h.cond.Broadcast()
		h.mu.Unlock()
		w.WriteHeader(204)
	case http.MethodGet:
		h.mu.Lock()
		for h.offer == "" {
			h.cond.Wait()
		}
		sdp := h.offer
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(sdpBody{SDP: sdp})
	default:
		http.Error(w, "method", 405)
	}
}

func (h *HTTPSignalingHub) handleAnswer(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var b sdpBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		h.mu.Lock()
		h.answer = b.SDP
		h.cond.Broadcast()
		h.mu.Unlock()
		w.WriteHeader(204)
	case http.MethodGet:
		h.mu.Lock()
		for h.answer == "" {
			h.cond.Wait()
		}
		sdp := h.answer
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(sdpBody{SDP: sdp})
	default:
		http.Error(w, "method", 405)
	}
}

func (h *HTTPSignalingHub) handleICE(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var b iceBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		h.mu.Lock()
		h.ice = append(h.ice, b)
		h.cond.Broadcast()
		h.mu.Unlock()
		w.WriteHeader(204)
	case http.MethodGet:
		h.mu.Lock()
		// Long-poll: wait for any ICE if empty.
		for len(h.ice) == 0 {
			h.cond.Wait()
		}
		out := h.ice
		h.ice = nil
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(out)
	default:
		http.Error(w, "method", 405)
	}
}

// HTTPSignalingClient implements Signaling against a hub base URL.
type HTTPSignalingClient struct {
	base   string
	client *http.Client
	role   string // "offer" | "answer"
}

func NewHTTPSignalingClient(baseURL, role string) *HTTPSignalingClient {
	return &HTTPSignalingClient{
		base:   baseURL,
		client: &http.Client{Timeout: 60 * time.Second},
		role:   role,
	}
}

func (c *HTTPSignalingClient) SendOffer(sdp string) error {
	return c.postJSON("/offer", sdpBody{SDP: sdp})
}
func (c *HTTPSignalingClient) SendAnswer(sdp string) error {
	return c.postJSON("/answer", sdpBody{SDP: sdp})
}

func (c *HTTPSignalingClient) RecvOffer(ctx context.Context) (string, error) {
	return c.getSDP(ctx, "/offer")
}
func (c *HTTPSignalingClient) RecvAnswer(ctx context.Context) (string, error) {
	return c.getSDP(ctx, "/answer")
}

func (c *HTTPSignalingClient) SendICE(fromOfferer bool, cand ICECandidateJSON) error {
	return c.postJSON("/ice", iceBody{FromOfferer: fromOfferer, Candidate: cand})
}

func (c *HTTPSignalingClient) RecvICE(ctx context.Context, fromOfferer bool) (ICECandidateJSON, error) {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/ice", nil)
		if err != nil {
			return ICECandidateJSON{}, err
		}
		res, err := c.client.Do(req)
		if err != nil {
			return ICECandidateJSON{}, err
		}
		var list []iceBody
		err = json.NewDecoder(res.Body).Decode(&list)
		res.Body.Close()
		if err != nil {
			return ICECandidateJSON{}, err
		}
		for _, b := range list {
			if b.FromOfferer == fromOfferer {
				return b.Candidate, nil
			}
		}
	}
}

func (c *HTTPSignalingClient) postJSON(path string, v any) error {
	b, _ := json.Marshal(v)
	res, err := c.client.Post(c.base+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("signaling %s: %s %s", path, res.Status, body)
	}
	return nil
}

func (c *HTTPSignalingClient) getSDP(ctx context.Context, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return "", err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var b sdpBody
	if err := json.NewDecoder(res.Body).Decode(&b); err != nil {
		return "", err
	}
	return b.SDP, nil
}
