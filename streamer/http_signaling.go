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
	SDP    string `json:"sdp"`
	PubKey []byte `json:"pubKey"` // X25519, 32 bytes
}

type iceBody struct {
	FromOfferer bool             `json:"fromOfferer"`
	Candidate   ICECandidateJSON `json:"candidate"`
}

type peerSlot struct {
	offer     SessionDesc
	answer    SessionDesc
	iceOffer  []ICECandidateJSON
	iceAnswer []ICECandidateJSON
}

// HTTPSignalingHub is a tiny local signaling server (no cloud).
// Peers are keyed by ?peer= (default "default") for multi-viewer fanout.
type HTTPSignalingHub struct {
	mu      sync.Mutex
	slots   map[string]*peerSlot
	metrics PeerMetrics
	started time.Time
	cond    *sync.Cond
	srv     *http.Server
	addr    string
	Token   string
}

func NewHTTPSignalingHub() *HTTPSignalingHub {
	h := &HTTPSignalingHub{
		slots:   make(map[string]*peerSlot),
		started: time.Now(),
	}
	h.cond = sync.NewCond(&h.mu)
	return h
}

func peerName(r *http.Request) string {
	p := strings.TrimSpace(r.URL.Query().Get("peer"))
	if p == "" {
		return "default"
	}
	return p
}

func (h *HTTPSignalingHub) slotLocked(name string) *peerSlot {
	s, ok := h.slots[name]
	if !ok {
		s = &peerSlot{}
		h.slots[name] = s
	}
	return s
}

func (h *HTTPSignalingHub) ListenAndServe(addr string) (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handleIndex)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("/status", requireToken(h.handleStatus, h.Token))
	mux.HandleFunc("/metrics", requireToken(h.handleMetrics, h.Token))
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
<p>Bind to 127.0.0.1. Use <code>-token</code> / <code>SCREEN_STREAM_TOKEN</code>.</p>
<p>Multi-viewer: <code>?peer=name</code> on offer/answer/ice.</p>
<p>API: <a href="/status">/status</a> · <code>/metrics</code> · <code>/offer</code> · <code>/answer</code> · <code>/ice</code></p>
</body>`)
}

func (h *HTTPSignalingHub) handleStatus(w http.ResponseWriter, _ *http.Request) {
	h.mu.Lock()
	peers := make([]string, 0, len(h.slots))
	hasOffer, hasAnswer := false, false
	iceO, iceA := 0, 0
	for name, s := range h.slots {
		peers = append(peers, name)
		if s.offer.SDP != "" {
			hasOffer = true
		}
		if s.answer.SDP != "" {
			hasAnswer = true
		}
		iceO += len(s.iceOffer)
		iceA += len(s.iceAnswer)
	}
	st := map[string]any{
		"hasOffer":  hasOffer,
		"hasAnswer": hasAnswer,
		"iceOffer":  iceO,
		"iceAnswer": iceA,
		"auth":      h.Token != "",
		"uptimeSec": int(time.Since(h.started).Seconds()),
		"peers":     peers,
		"metrics":   h.metrics,
	}
	h.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

func (h *HTTPSignalingHub) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	var m PeerMetrics
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&m); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	h.mu.Lock()
	h.metrics = m
	h.mu.Unlock()
	w.WriteHeader(204)
}

func (h *HTTPSignalingHub) Close() error {
	if h.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.srv.Shutdown(ctx)
}

func (h *HTTPSignalingHub) resetSlotLocked(s *peerSlot) {
	s.offer = SessionDesc{}
	s.answer = SessionDesc{}
	s.iceOffer = nil
	s.iceAnswer = nil
}

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
	name := peerName(r)
	switch r.Method {
	case http.MethodPost:
		var b sdpBody
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if b.SDP == "" || len(b.PubKey) != 32 {
			http.Error(w, "sdp and 32-byte pubKey required", 400)
			return
		}
		h.mu.Lock()
		s := h.slotLocked(name)
		h.resetSlotLocked(s)
		s.offer = SessionDesc{SDP: b.SDP, PubKey: append([]byte(nil), b.PubKey...)}
		h.cond.Broadcast()
		h.mu.Unlock()
		w.WriteHeader(204)
	case http.MethodGet:
		h.mu.Lock()
		s := h.slotLocked(name)
		ok := h.waitCond(func() bool { return s.offer.SDP != "" }, time.Duration(MaxSignalingWaitSeconds)*time.Second)
		if !ok {
			h.mu.Unlock()
			http.Error(w, "timeout waiting for offer", 504)
			return
		}
		d := s.offer
		s.offer = SessionDesc{}
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(sdpBody{SDP: d.SDP, PubKey: d.PubKey})
	case http.MethodDelete:
		h.mu.Lock()
		if s, ok := h.slots[name]; ok {
			h.resetSlotLocked(s)
		}
		h.cond.Broadcast()
		h.mu.Unlock()
		w.WriteHeader(204)
	default:
		http.Error(w, "method", 405)
	}
}

func (h *HTTPSignalingHub) handleAnswer(w http.ResponseWriter, r *http.Request) {
	name := peerName(r)
	switch r.Method {
	case http.MethodPost:
		var b sdpBody
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if b.SDP == "" || len(b.PubKey) != 32 {
			http.Error(w, "sdp and 32-byte pubKey required", 400)
			return
		}
		h.mu.Lock()
		s := h.slotLocked(name)
		s.answer = SessionDesc{SDP: b.SDP, PubKey: append([]byte(nil), b.PubKey...)}
		h.cond.Broadcast()
		h.mu.Unlock()
		w.WriteHeader(204)
	case http.MethodGet:
		h.mu.Lock()
		s := h.slotLocked(name)
		ok := h.waitCond(func() bool { return s.answer.SDP != "" }, time.Duration(MaxSignalingWaitSeconds)*time.Second)
		if !ok {
			h.mu.Unlock()
			http.Error(w, "timeout waiting for answer", 504)
			return
		}
		d := s.answer
		s.answer = SessionDesc{}
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(sdpBody{SDP: d.SDP, PubKey: d.PubKey})
	default:
		http.Error(w, "method", 405)
	}
}

func (h *HTTPSignalingHub) handleICE(w http.ResponseWriter, r *http.Request) {
	name := peerName(r)
	switch r.Method {
	case http.MethodPost:
		var b iceBody
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		h.mu.Lock()
		s := h.slotLocked(name)
		if b.FromOfferer {
			if len(s.iceOffer) < 256 {
				s.iceOffer = append(s.iceOffer, b.Candidate)
			}
		} else if len(s.iceAnswer) < 256 {
			s.iceAnswer = append(s.iceAnswer, b.Candidate)
		}
		h.cond.Broadcast()
		h.mu.Unlock()
		w.WriteHeader(204)
	case http.MethodGet:
		from := r.URL.Query().Get("from")
		wantOffer := from != "answer"
		h.mu.Lock()
		s := h.slotLocked(name)
		ok := h.waitCond(func() bool {
			if wantOffer {
				return len(s.iceOffer) > 0
			}
			return len(s.iceAnswer) > 0
		}, time.Duration(MaxSignalingWaitSeconds)*time.Second)
		if !ok {
			h.mu.Unlock()
			http.Error(w, "timeout waiting for ICE", 504)
			return
		}
		var c ICECandidateJSON
		if wantOffer {
			c = s.iceOffer[0]
			s.iceOffer = s.iceOffer[1:]
		} else {
			c = s.iceAnswer[0]
			s.iceAnswer = s.iceAnswer[1:]
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
	peer   string
	client *http.Client
}

func NewHTTPSignalingClient(baseURL string) *HTTPSignalingClient {
	return NewHTTPSignalingClientToken(baseURL, "")
}

func NewHTTPSignalingClientToken(baseURL, token string) *HTTPSignalingClient {
	return NewHTTPSignalingClientPeer(baseURL, token, "default")
}

func NewHTTPSignalingClientPeer(baseURL, token, peer string) *HTTPSignalingClient {
	if peer == "" {
		peer = "default"
	}
	return &HTTPSignalingClient{
		base:   baseURL,
		token:  token,
		peer:   peer,
		client: &http.Client{Timeout: time.Duration(MaxSignalingWaitSeconds+30) * time.Second},
	}
}

func (c *HTTPSignalingClient) peerQ(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "peer=" + c.peer
}

func (c *HTTPSignalingClient) SendOffer(d SessionDesc) error {
	return c.postJSON(c.peerQ("/offer"), sdpBody{SDP: d.SDP, PubKey: d.PubKey})
}
func (c *HTTPSignalingClient) SendAnswer(d SessionDesc) error {
	return c.postJSON(c.peerQ("/answer"), sdpBody{SDP: d.SDP, PubKey: d.PubKey})
}
func (c *HTTPSignalingClient) RecvOffer(ctx context.Context) (SessionDesc, error) {
	return c.getDesc(ctx, c.peerQ("/offer"))
}
func (c *HTTPSignalingClient) RecvAnswer(ctx context.Context) (SessionDesc, error) {
	return c.getDesc(ctx, c.peerQ("/answer"))
}

func (c *HTTPSignalingClient) SendICE(fromOfferer bool, cand ICECandidateJSON) error {
	return c.postJSON(c.peerQ("/ice"), iceBody{FromOfferer: fromOfferer, Candidate: cand})
}

func (c *HTTPSignalingClient) RecvICE(ctx context.Context, fromOfferer bool) (ICECandidateJSON, error) {
	q := "from=offer"
	if !fromOfferer {
		q = "from=answer"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+c.peerQ("/ice")+"&"+q, nil)
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

// PostMetrics pushes wire counters to the hub.
func (c *HTTPSignalingClient) PostMetrics(m PeerMetrics) error {
	if m.Peer == "" {
		m.Peer = c.peer
	}
	return c.postJSON("/metrics", m)
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

func (c *HTTPSignalingClient) getDesc(ctx context.Context, path string) (SessionDesc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return SessionDesc{}, err
	}
	c.setAuth(req)
	res, err := c.client.Do(req)
	if err != nil {
		return SessionDesc{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == 401 {
		return SessionDesc{}, fmt.Errorf("signaling unauthorized (check -token)")
	}
	if res.StatusCode == 504 {
		return SessionDesc{}, fmt.Errorf("signaling %s timeout", path)
	}
	var b sdpBody
	if err := json.NewDecoder(res.Body).Decode(&b); err != nil {
		return SessionDesc{}, err
	}
	if b.SDP == "" || len(b.PubKey) != 32 {
		return SessionDesc{}, fmt.Errorf("signaling %s: missing sdp/pubKey", path)
	}
	return SessionDesc{SDP: b.SDP, PubKey: b.PubKey}, nil
}
