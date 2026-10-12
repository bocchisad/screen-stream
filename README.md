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

# Three terminals — same -xor-seed and -token on all peers
export SCREEN_STREAM_TOKEN=dev-secret
# terminal 1:
go run ./cmd/screen-stream -mode=hub -signal=127.0.0.1:8787
# terminal 2 (recv first):
go run ./cmd/screen-stream -mode=recv -signal=127.0.0.1:8787 \
  -seconds=30 -snapshot=/tmp/recv.png -preview=127.0.0.1:8090 \
  -xor-seed=0xDEADBEEFCAFEBABE
# terminal 3:
go run ./cmd/screen-stream -mode=send -signal=127.0.0.1:8787 \
  -synthetic=false -scale=75 -seconds=30 -xor-seed=0xDEADBEEFCAFEBABE
# open the preview URL printed by recv (includes ?token= when set)
```

Shared seed: `-xor-seed` / `SCREEN_STREAM_XOR_SEED` → RAM XOR **and** wire **ChaCha20-Poly1305** (HKDF).
Default seed is public — set a private seed for real use. RAM XOR alone is still only obfuscation.

Auth: `-token` / `SCREEN_STREAM_TOKEN` on hub + preview (`Authorization: Bearer` or `?token=`).
Bind hub/preview to `127.0.0.1`. Without `-token`, signaling/preview stay open (dev).

Useful flags:
- `-pii` — demo corner blackout (off by default)
- `-ocr` — on-device Vision OCR → pattern redaction (macOS)
- `-preview=host:port` — MJPEG viewer on recv/loopback

Exit `2` = screen permission denied.

## TZ status

| Spec item | Status |
|-----------|--------|
| Zero-alloc capture pool | Done (~0–1 alloc/frame after warmup; pure-Go XOR/hash/diff) |
| macOS ScreenCaptureKit BGRA + filter | Done (Retina native) |
| Linux X11 XShm | Done |
| Wayland PipeWire | Portal CreateSession plumbing when `-DSS_HAVE_PIPEWIRE=1`; capture still needs grant+stream map |
| OutputSize API | Done — no 8K probe alloc; macOS/X11/synthetic |
| Frame diff + dirty boxes | Done (word FNV + H/V run merge) |
| RAM XOR protect | Done (pure-Go hot path; C retained in tree) |
| PII masking | PatternDetector + Luhn PANs + OCR/Vision (`-ocr`) |
| WebRTC DataChannel | Done (chunked SSCH + pooled zlib + ChaCha20-Poly1305) |
| P2P without cloud SFU | Done (local hub; token auth; `/` help + `/status`) |
| Hub/preview auth | Done (`-token` / `SCREEN_STREAM_TOKEN`) |
| Viewer UI | MJPEG `-preview` + PNG `-snapshot` |
| Wire stats | `wire_bytes_*` + Mbit/s on send/recv/loopback |
| FHD p99 &lt; 10ms | Done on synthetic FHD; high-res auto `-block=128` |
| Doctor | `-mode=doctor` checks grabber + hub |
| GPU / video encode | Not in v0 |

**Preview URL:** `http://127.0.0.1:8090/` (recv). Hub `:8787` is signaling only.

## Layout

```
engine/     grabber_*, pipeline, protect_{cgo,pure}, pii, ocr_vision_*, pool, native/
streamer/   webrtc, chunk, compress, proto, receiver, preview, http_signaling
cmd/screen-stream/
```
