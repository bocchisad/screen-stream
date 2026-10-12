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

// HTTPSignalingHub is a tiny local signaling server for two peers (no cloud).
type HTTPSignalingHub struct {
	mu        sync.Mutex
	offer     string
	answer    string
	iceOffer  []ICECandidateJSON // offerer → answerer
	iceAnswer []ICECandidateJSON // answerer → offerer
	cond      *sync.Cond
	srv       *http.Server
	addr      string
}

func NewHTTPSignalingHub() *HTTPSignalingHub {
	h := &HTTPSignalingHub{}
	h.cond = sync.NewCond(&h.mu)
	return h
}

func (h *HTTPSignalingHub) ListenAndServe(addr string) (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handleIndex)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("/status", h.handleStatus)
	mux.HandleFunc("/offer", h.handleOffer)
	mux.HandleFunc("/answer", h.handleAnswer)
	mux.HandleFunc("/ice", h.handleICE)
	// Prefer tcp4 so clients dialing 127.0.0.1 work (tcp on macOS often binds [::] only).
	network := "tcp4"
	if len(addr) > 0 && addr[0] == '[' {
		network = "tcp"
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		// Fallback if tcp4 unavailable.
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			return "", err
		}
	}
	h.addr = ln.Addr().String()
	h.srv = &http.Server{Handler: mux}
	go func() { _ = h.srv.Serve(ln) }()
	return "http://" + h.addr, nil
}

func (h *HTTPSignalingHub) Addr() string { return h.addr }

func (h *HTTPSignalingHub) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html><title>screen-stream hub</title>
<body style="font:14px/1.4 system-ui;max-width:40rem;margin:2rem;background:#111;color:#ddd">
<h1>signaling hub</h1>
<p>This is <b>not</b> the live preview. Preview is on recv: <code>http://127.0.0.1:8090/</code></p>
<p>API: <a href="/status">/status</a> · <code>/offer</code> · <code>/answer</code> · <code>/ice</code> · <code>/health</code></p>
</body>`)
}

func (h *HTTPSignalingHub) handleStatus(w http.ResponseWriter, _ *http.Request) {
	h.mu.Lock()
	st := map[string]any{
		"hasOffer":  h.offer != "",
		"hasAnswer": h.answer != "",
		"iceOffer":  len(h.iceOffer),
		"iceAnswer": len(h.iceAnswer),
	}
	h.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

func (h *HTTPSignalingHub) Close() error {
	if h.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.srv.Shutdown(ctx)
}

func (h *HTTPSignalingHub) resetLocked() {
	h.offer = ""
	h.answer = ""
	h.iceOffer = nil
	h.iceAnswer = nil
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
		// New offer starts a fresh session (drops stale answer/ICE from prior runs).
		h.resetLocked()
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
		// One-shot: consume offer so a late recv cannot bind to a dead sender.
		h.offer = ""
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(sdpBody{SDP: sdp})
	case http.MethodDelete:
		h.mu.Lock()
		h.resetLocked()
		h.cond.Broadcast()
		h.mu.Unlock()
		w.WriteHeader(204)
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
		h.answer = "" // one-shot
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
		if b.FromOfferer {
			h.iceOffer = append(h.iceOffer, b.Candidate)
		} else {
			h.iceAnswer = append(h.iceAnswer, b.Candidate)
		}
		h.cond.Broadcast()
		h.mu.Unlock()
		w.WriteHeader(204)
	case http.MethodGet:
		// ?from=offer → fetch candidates produced by offerer (for answerer)
		from := r.URL.Query().Get("from")
		wantOffer := from != "answer"
		h.mu.Lock()
		for {
			if wantOffer && len(h.iceOffer) > 0 {
				c := h.iceOffer[0]
				h.iceOffer = h.iceOffer[1:]
				h.mu.Unlock()
				_ = json.NewEncoder(w).Encode(c)
				return
			}
			if !wantOffer && len(h.iceAnswer) > 0 {
				c := h.iceAnswer[0]
				h.iceAnswer = h.iceAnswer[1:]
				h.mu.Unlock()
				_ = json.NewEncoder(w).Encode(c)
				return
			}
			h.cond.Wait()
		}
	default:
		http.Error(w, "method", 405)
	}
}

// HTTPSignalingClient implements Signaling against a hub base URL.
type HTTPSignalingClient struct {
	base   string
	client *http.Client
}

func NewHTTPSignalingClient(baseURL string) *HTTPSignalingClient {
	return &HTTPSignalingClient{
		base:   baseURL,
		client: &http.Client{Timeout: 120 * time.Second},
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
	// fromOfferer=true means we want candidates that came FROM the offerer.
	q := "from=offer"
	if !fromOfferer {
		q = "from=answer"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/ice?"+q, nil)
	if err != nil {
		return ICECandidateJSON{}, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return ICECandidateJSON{}, err
	}
	defer res.Body.Close()
	var cnd ICECandidateJSON
	if err := json.NewDecoder(res.Body).Decode(&cnd); err != nil {
		return ICECandidateJSON{}, err
	}
	return cnd, nil
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
