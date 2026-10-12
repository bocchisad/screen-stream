package streamer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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
// Set Token for Bearer/?token= auth; empty token = open (dev). Prefer loopback bind.
type HTTPSignalingHub struct {
	mu        sync.Mutex
	offer     string
	answer    string
	iceOffer  []ICECandidateJSON
	iceAnswer []ICECandidateJSON
	cond      *sync.Cond
	srv       *http.Server
	addr      string
	Token     string
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
	mux.HandleFunc("/status", requireToken(h.handleStatus, h.Token))
	mux.HandleFunc("/offer", requireToken(h.handleOffer, h.Token))
	mux.HandleFunc("/answer", requireToken(h.handleAnswer, h.Token))
	mux.HandleFunc("/ice", requireToken(h.handleICE, h.Token))
	network := "tcp4"
	if len(addr) > 0 && addr[0] == '[' {
		network = "tcp"
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			return "", err
		}
	}
	h.addr = ln.Addr().String()
	h.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Duration(MaxSignalingWaitSeconds+30) * time.Second,
		WriteTimeout:      30 * time.Second,
	}
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
<p>Bind to 127.0.0.1. Use <code>-token</code> / <code>SCREEN_STREAM_TOKEN</code> for auth.</p>
<p>Preview: <code>http://127.0.0.1:8090/</code></p>
<p>API: <a href="/status">/status</a> · <code>/offer</code> · <code>/answer</code> · <code>/ice</code></p>
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

// waitCond waits until pred() or timeout; caller holds h.mu.
func (h *HTTPSignalingHub) waitCond(pred func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for !pred() {
		left := time.Until(deadline)
		if left <= 0 {
			return false
		}
		timer := time.AfterFunc(left, func() {
			h.mu.Lock()
			h.cond.Broadcast()
			h.mu.Unlock()
		})
		h.cond.Wait()
		timer.Stop()
		if !pred() && time.Now().After(deadline) {
			return false
		}
	}
	return true
}

func (h *HTTPSignalingHub) handleOffer(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var b sdpBody
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		h.mu.Lock()
		h.resetLocked()
		h.offer = b.SDP
		h.cond.Broadcast()
		h.mu.Unlock()
		w.WriteHeader(204)
	case http.MethodGet:
		h.mu.Lock()
		ok := h.waitCond(func() bool { return h.offer != "" }, time.Duration(MaxSignalingWaitSeconds)*time.Second)
		if !ok {
			h.mu.Unlock()
			http.Error(w, "timeout waiting for offer", 504)
			return
		}
		sdp := h.offer
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
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b); err != nil {
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
		ok := h.waitCond(func() bool { return h.answer != "" }, time.Duration(MaxSignalingWaitSeconds)*time.Second)
		if !ok {
			h.mu.Unlock()
			http.Error(w, "timeout waiting for answer", 504)
			return
		}
		sdp := h.answer
		h.answer = ""
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
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		h.mu.Lock()
		if b.FromOfferer {
			if len(h.iceOffer) < 256 {
				h.iceOffer = append(h.iceOffer, b.Candidate)
			}
		} else if len(h.iceAnswer) < 256 {
			h.iceAnswer = append(h.iceAnswer, b.Candidate)
		}
		h.cond.Broadcast()
		h.mu.Unlock()
		w.WriteHeader(204)
	case http.MethodGet:
		from := r.URL.Query().Get("from")
		wantOffer := from != "answer"
		h.mu.Lock()
		ok := h.waitCond(func() bool {
			if wantOffer {
				return len(h.iceOffer) > 0
			}
			return len(h.iceAnswer) > 0
		}, time.Duration(MaxSignalingWaitSeconds)*time.Second)
		if !ok {
			h.mu.Unlock()
			http.Error(w, "timeout waiting for ICE", 504)
			return
		}
		var c ICECandidateJSON
		if wantOffer {
			c = h.iceOffer[0]
			h.iceOffer = h.iceOffer[1:]
		} else {
			c = h.iceAnswer[0]
			h.iceAnswer = h.iceAnswer[1:]
		}
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(c)
	default:
		http.Error(w, "method", 405)
	}
}

// IsLoopbackAddr reports whether host is a loopback bind/dial target.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// HTTPSignalingClient implements Signaling against a hub base URL.
type HTTPSignalingClient struct {
	base   string
	token  string
	client *http.Client
}

func NewHTTPSignalingClient(baseURL string) *HTTPSignalingClient {
	return NewHTTPSignalingClientToken(baseURL, "")
}

func NewHTTPSignalingClientToken(baseURL, token string) *HTTPSignalingClient {
	return &HTTPSignalingClient{
		base:   baseURL,
		token:  token,
		client: &http.Client{Timeout: time.Duration(MaxSignalingWaitSeconds+30) * time.Second},
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
	q := "from=offer"
	if !fromOfferer {
		q = "from=answer"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/ice?"+q, nil)
	if err != nil {
		return ICECandidateJSON{}, err
	}
	c.setAuth(req)
	res, err := c.client.Do(req)
	if err != nil {
		return ICECandidateJSON{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == 504 {
		return ICECandidateJSON{}, fmt.Errorf("signaling ICE timeout")
	}
	var cnd ICECandidateJSON
	if err := json.NewDecoder(res.Body).Decode(&cnd); err != nil {
		return ICECandidateJSON{}, err
	}
	return cnd, nil
}

func (c *HTTPSignalingClient) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func (c *HTTPSignalingClient) postJSON(path string, v any) error {
	b, _ := json.Marshal(v)
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("signaling %s: %s %s", path, res.Status, body)
	}
	return nil
}

func (c *HTTPSignalingClient) getSDP(ctx context.Context, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return "", err
	}
	c.setAuth(req)
	res, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode == 401 {
		return "", fmt.Errorf("signaling unauthorized (check -token)")
	}
	if res.StatusCode == 504 {
		return "", fmt.Errorf("signaling %s timeout", path)
	}
	var b sdpBody
	if err := json.NewDecoder(res.Body).Decode(&b); err != nil {
		return "", err
	}
	return b.SDP, nil
}
