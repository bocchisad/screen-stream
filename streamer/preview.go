package streamer

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"net"
	"net/http"
	"sync"
	"time"
)

// PreviewServer serves a live MJPEG view of reconstructed frames (TZ viewer surface).
// Unauthenticated — bind to loopback only.
type PreviewServer struct {
	mu       sync.Mutex
	jpeg     []byte
	seq      uint64
	cond     *sync.Cond
	srv      *http.Server
	addr     string
	quality  int
	viewers  int
	Token    string

	rgba   *image.RGBA
	jpgBuf bytes.Buffer
}

func NewPreviewServer(quality int) *PreviewServer {
	if quality <= 0 || quality > 100 {
		quality = 70
	}
	p := &PreviewServer{quality: quality}
	p.cond = sync.NewCond(&p.mu)
	p.setWaitingJPEG()
	return p
}

func (p *PreviewServer) setWaitingJPEG() {
	img := image.NewRGBA(image.Rect(0, 0, 640, 360))
	// dark gray background
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+0] = 0x22
		img.Pix[i+1] = 0x22
		img.Pix[i+2] = 0x22
		img.Pix[i+3] = 0xff
	}
	// light horizontal bar as "waiting" marker
	for y := 170; y < 190; y++ {
		for x := 180; x < 460; x++ {
			i := y*img.Stride + x*4
			img.Pix[i+0] = 0xaa
			img.Pix[i+1] = 0xaa
			img.Pix[i+2] = 0xaa
			img.Pix[i+3] = 0xff
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 60})
	p.jpeg = append(p.jpeg[:0], buf.Bytes()...)
	p.seq++
}

// UpdateFromBGRA encodes current canvas to JPEG for viewers.
func (p *PreviewServer) UpdateFromBGRA(bgra []byte, width, height, stride int) error {
	if width <= 0 || height <= 0 || len(bgra) < stride*height {
		return fmt.Errorf("bad frame")
	}
	if p.rgba == nil || p.rgba.Bounds().Dx() != width || p.rgba.Bounds().Dy() != height {
		p.rgba = image.NewRGBA(image.Rect(0, 0, width, height))
	}
	img := p.rgba
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			si := y*stride + x*4
			di := y*img.Stride + x*4
			img.Pix[di+0] = bgra[si+2]
			img.Pix[di+1] = bgra[si+1]
			img.Pix[di+2] = bgra[si+0]
			img.Pix[di+3] = 255
		}
	}
	p.jpgBuf.Reset()
	if err := jpeg.Encode(&p.jpgBuf, img, &jpeg.Options{Quality: p.quality}); err != nil {
		return err
	}
	p.mu.Lock()
	p.jpeg = append(p.jpeg[:0], p.jpgBuf.Bytes()...)
	p.seq++
	p.cond.Broadcast()
	p.mu.Unlock()
	return nil
}

func (p *PreviewServer) ListenAndServe(addr string) (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", requireToken(p.handleIndex, p.Token))
	mux.HandleFunc("/stream", requireToken(p.handleStream, p.Token))
	mux.HandleFunc("/frame.jpg", requireToken(p.handleFrame, p.Token))
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			return "", err
		}
	}
	p.addr = ln.Addr().String()
	p.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	url := "http://" + p.addr
	if p.Token != "" {
		url += "/?token=" + p.Token
	}
	return url, nil
}

func (p *PreviewServer) Close() error {
	p.mu.Lock()
	p.cond.Broadcast()
	p.mu.Unlock()
	if p.srv == nil {
		return nil
	}
	return p.srv.Close()
}

func (p *PreviewServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	src := "/stream"
	if p.Token != "" {
		src = "/stream?token=" + p.Token
	}
	_, _ = fmt.Fprintf(w, `<!doctype html><title>screen-stream preview</title>
<body style="margin:0;background:#111;display:flex;justify-content:center;align-items:center;height:100vh">
<img src="%s" alt="live" style="max-width:100%%;max-height:100%%;object-fit:contain"/>
</body>`, src)
	_ = r
}

func (p *PreviewServer) handleFrame(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	b := append([]byte(nil), p.jpeg...)
	p.mu.Unlock()
	if len(b) == 0 {
		http.Error(w, "no frame", 503)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (p *PreviewServer) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flush", 500)
		return
	}
	p.mu.Lock()
	if p.viewers >= MaxPreviewClients {
		p.mu.Unlock()
		http.Error(w, "too many viewers", 503)
		return
	}
	p.viewers++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.viewers--
		p.cond.Broadcast()
		p.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "close")
	var last uint64
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		p.mu.Lock()
		for p.seq == last {
			p.cond.Wait()
			if r.Context().Err() != nil {
				p.mu.Unlock()
				return
			}
		}
		last = p.seq
		b := append([]byte(nil), p.jpeg...)
		p.mu.Unlock()
		if len(b) == 0 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		_, _ = fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(b))
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\r\n"))
		flusher.Flush()
	}
}
