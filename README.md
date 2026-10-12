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
export SCREEN_STREAM_TOKEN=dev-secret
go run ./cmd/screen-stream -mode=loopback -synthetic=false -scale=100 \
  -seconds=10 -snapshot=/tmp/ss.png -preview=127.0.0.1:8090

# Three terminals — same -token; session keys via X25519 ECDH
export SCREEN_STREAM_TOKEN=dev-secret
go run ./cmd/screen-stream -mode=hub -signal=127.0.0.1:8787
go run ./cmd/screen-stream -mode=recv -signal=127.0.0.1:8787 \
  -seconds=30 -snapshot=/tmp/recv.png -preview=127.0.0.1:8090
go run ./cmd/screen-stream -mode=send -signal=127.0.0.1:8787 \
  -synthetic=false -scale=75 -seconds=30
```

**Auth (required):** `-token` / `SCREEN_STREAM_TOKEN` for `hub` / `send` / `recv` / `-preview`.

**Keys:** X25519 on offer/answer → HKDF → ChaCha20-Poly1305 + RAM XOR seed (`ecdh_seed`).
`-xor-seed` only for local `bench`.

Useful flags:
- `-pii` / `-ocr` — on-device redaction (Vision OCR on macOS + ASCII/pattern scan)
- `-exclude` / `-exclude-window` — macOS share-set filters
- `-preview=host:port` — MJPEG on recv/loopback

Exit `2` = screen permission denied.

## TZ status

| Spec item | Status |
|-----------|--------|
| Zero-alloc capture pool | Process (+PixelScan) 0 alloc after warmup; OS capture may still native-malloc |
| macOS ScreenCaptureKit BGRA + filter | Done (bundle + window exclude) |
| Linux X11 XShm | Done (packed BGRA normalize) |
| Wayland PipeWire | Linked by default on Linux via pkg-config (`libpipewire`+gio); use `-tags nopipewire` without libs |
| OutputSize API | Done |
| Frame diff + dirty boxes | Done |
| RAM XOR protect | Done; pipeline plain + OCR async scratch cleared |
| PII masking | `-pii`/`-ocr` → Vision (frame-1 sync) + ASCII pixel scan |
| WebRTC DataChannel | Done (SSCH + zlib + AEAD) |
| P2P without cloud SFU | Done (hub + mandatory token + ECDH) |
| Session key exchange | Done (X25519 + HKDF) |
| Viewer UI | MJPEG + PNG |
| FHD p99 &lt; 10ms | CI: Process + Capture+Process (synthetic); live high-res may auto `-block=128` |
| Doctor | `-mode=doctor` |
| GPU / video encode | Not in v0 |

## Layout

```
engine/     grabber_*, pipeline, protect_*, pii*, ocr_vision_*, pool, native/
streamer/   webrtc, kx, aead, auth, chunk, compress, proto, receiver, preview, http_signaling
cmd/screen-stream/
```
