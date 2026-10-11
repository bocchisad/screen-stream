# screen-stream

Privacy-preserving, low-latency screen capture + P2P DirtyPacket delivery (Go + cgo + pion WebRTC).

## Modes

| `-mode` | Role |
|---------|------|
| `bench` | Capture → pipeline → local ring (default) |
| `loopback` | In-process WebRTC offer↔answer + reconstruct (`-webrtc` alias) |
| `hub` | Local HTTP signaling (no cloud) |
| `send` | Capture → WebRTC offerer → hub |
| `recv` | WebRTC answerer ← hub → reconstruct / PNG |

## Quick start

```bash
go work sync
go test ./engine/ ./streamer/ -count=1

# Local bench / Retina live
go run ./cmd/screen-stream -mode=bench -synthetic=false -scale=100 -seconds=3

# Loopback P2P + snapshot
go run ./cmd/screen-stream -mode=loopback -synthetic=false -scale=100 \
  -seconds=3 -snapshot=/tmp/ss.png

# Two-process P2P (same machine or LAN)
# terminal 1:
go run ./cmd/screen-stream -mode=hub -signal=0.0.0.0:8787
# terminal 2 (sender):
go run ./cmd/screen-stream -mode=send -signal=127.0.0.1:8787 \
  -synthetic=false -scale=75 -seconds=10 -xor-seed=0xC0FFEE42D00D
# terminal 3 (receiver):
go run ./cmd/screen-stream -mode=recv -signal=127.0.0.1:8787 \
  -seconds=10 -snapshot=/tmp/recv.png -xor-seed=0xC0FFEE42D00D
```

Shared XOR seed: `-xor-seed` or env `SCREEN_STREAM_XOR_SEED`.

Exit `2` = screen permission denied.

## TZ status

| Spec item | Status |
|-----------|--------|
| Zero-alloc capture pool | Done (engine freelist) |
| macOS ScreenCaptureKit BGRA + filter | Done (Retina native) |
| Linux X11 XShm | Done |
| Wayland PipeWire | State-machine skeleton; full portal needs `-DSS_HAVE_PIPEWIRE=1` |
| Frame diff + dirty boxes | Done (C via cgo, pure Go fallback) |
| RAM XOR protect | Done (C via cgo) |
| PII masking | PatternDetector + OCRDetector adapter (stub OCR backend) |
| WebRTC DataChannel | Done (chunked + zlib) |
| P2P without cloud SFU | Done (local hub signaling) |
| Viewer UI | Snapshot PNG only |
| GPU / video encode | Not in v0 |

## Layout

```
engine/     grabber_*, pipeline, protect_{cgo,pure}, pii, pool, native/
streamer/   webrtc, chunk, compress, proto, receiver, http_signaling
cmd/screen-stream/
```
