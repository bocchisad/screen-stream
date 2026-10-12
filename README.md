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

export SCREEN_STREAM_TOKEN=dev-secret
go run ./cmd/screen-stream -mode=hub -signal=127.0.0.1:8787
go run ./cmd/screen-stream -mode=recv -signal=127.0.0.1:8787 \
  -seconds=30 -preview=127.0.0.1:8090 -peer=alice
go run ./cmd/screen-stream -mode=send -signal=127.0.0.1:8787 \
  -synthetic=false -scale=75 -seconds=30 -peers=alice
# both print fingerprint=… — compare or pass -expect-fingerprint=
```

**Auth (required):** `-token` / `SCREEN_STREAM_TOKEN` for `hub` / `send` / `recv` / `-preview`.

**Keys:** X25519 ECDH → ChaCha20-Poly1305 + XOR seed; `fingerprint=` is a TOFU hash of both pubs.

**Flags:**
- `-mode=cli` — interactive terminal menu
- `-capture=cpu|gpu-surface` — staging path (`gpu-surface` = retain surface/buffer, one copy into Go; still SCK/PipeWire, experimental)
- `-stun=stun:host:port` — optional ICE STUN (no TURN/SFU)
- `-peer` / `-peers` — hub slot(s) for multi-viewer
- `-expect-fingerprint=HEX` — fail if ECDH transcript mismatches
- `-pii` / `-ocr` — Vision (macOS) or Tesseract (if on PATH) + ASCII scan

Hub `/status` includes `uptimeSec`, `peers`, and last POST `/metrics` counters.

**Linux PipeWire:** default build links via pkg-config when libs exist. Without libpipewire/gio use `go build -tags nopipewire` (X11 path only). `doctor` prints `pipewire_builtin=`.

## Capture source

OS screen recording APIs → CPU BGRA (not raw GPU VRAM). See [GPU notes](docs/superpowers/specs/2026-10-12-gpu-vram-capture-notes.md).

`gpu-surface`: macOS retains `CVPixelBuffer`; Linux PipeWire holds the mapped buffer and copies once into the Go dst (skips intermediate staging). Still the same OS screen APIs — not foreign VRAM.

## Specs / plan

- [v0 design](docs/superpowers/specs/2026-10-12-screen-stream-design.md)
- [v1 extras](docs/superpowers/specs/2026-10-12-screen-stream-v1-extras-design.md)
- [v1.1 plan](docs/superpowers/plans/2026-10-12-screen-stream-v1.1.md)

## Layout

```
engine/     grabber_*, pipeline, pii*, ocr_*, pool, native/
streamer/   webrtc, kx, aead, auth, chunk, http_signaling, preview
cmd/screen-stream/
```
