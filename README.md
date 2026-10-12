# screen-stream

Privacy-preserving, low-latency screen capture + P2P DirtyPacket delivery (Go + cgo + pion WebRTC).

## Modes

| `-mode` | Role |
|---------|------|
| `bench` | Capture → pipeline → local ring (default) |
| `loopback` | In-process WebRTC offer↔answer + reconstruct (`-webrtc` alias) |
| `hub` | Local HTTP signaling (no cloud) |
| `send` | Capture → WebRTC offerer → hub |
| `recv` | WebRTC answerer ← hub → reconstruct / PNG / MJPEG preview |

## Quick start

```bash
go work sync
go test ./engine/ ./streamer/ -count=1

# Local bench / Retina live
go run ./cmd/screen-stream -mode=bench -synthetic=false -scale=100 -seconds=3

# Loopback P2P + snapshot + live preview
go run ./cmd/screen-stream -mode=loopback -synthetic=false -scale=100 \
  -seconds=10 -snapshot=/tmp/ss.png -preview=127.0.0.1:8090
# open http://127.0.0.1:8090/

# Three terminals — hub must stay up; start recv then send (or both together)
# terminal 1:
go run ./cmd/screen-stream -mode=hub -signal=127.0.0.1:8787
# terminal 2 (receiver + preview) — start first so it waits for offer:
go run ./cmd/screen-stream -mode=recv -signal=127.0.0.1:8787 \
  -seconds=30 -snapshot=/tmp/recv.png -preview=127.0.0.1:8090 \
  -xor-seed=0xC0FFEE42D00D
# terminal 3 (sender):
go run ./cmd/screen-stream -mode=send -signal=127.0.0.1:8787 \
  -synthetic=false -scale=75 -seconds=30 -xor-seed=0xC0FFEE42D00D
# open http://127.0.0.1:8090/  — gray bar = waiting; picture = stream OK
# if dial times out: restart hub (clears stale SDP), then recv+send again
```

Shared XOR seed: `-xor-seed` or env `SCREEN_STREAM_XOR_SEED`.

Useful flags:
- `-pii` — demo corner blackout (off by default)
- `-ocr` — on-device Vision OCR → pattern redaction (macOS)
- `-preview=host:port` — MJPEG viewer on recv/loopback

Exit `2` = screen permission denied.

## TZ status

| Spec item | Status |
|-----------|--------|
| Zero-alloc capture pool | Done (engine freelist; PacketPool non-blocking) |
| macOS ScreenCaptureKit BGRA + filter | Done (Retina native) |
| Linux X11 XShm | Done |
| Wayland PipeWire | State-machine skeleton; full portal needs `-DSS_HAVE_PIPEWIRE=1` |
| Frame diff + dirty boxes | Done (word-sized FNV hash; horizontal run merge) |
| RAM XOR protect | Done (C via cgo) |
| PII masking | PatternDetector + Luhn PANs + OCR/Vision (`-ocr`) |
| WebRTC DataChannel | Done (chunked SSCH + pooled zlib) |
| P2P without cloud SFU | Done (local hub; `/` help page + `/status`) |
| Viewer UI | MJPEG `-preview` + PNG `-snapshot` |
| FHD p99 &lt; 10ms | Best-effort; high-res auto `-block=128`; use `-scale=50` / `-compress=false` if needed |
| GPU / video encode | Not in v0 |

**Preview URL:** `http://127.0.0.1:8090/` (recv). Hub `:8787` is signaling only.

## Layout

```
engine/     grabber_*, pipeline, protect_{cgo,pure}, pii, ocr_vision_*, pool, native/
streamer/   webrtc, chunk, compress, proto, receiver, preview, http_signaling
cmd/screen-stream/
```
