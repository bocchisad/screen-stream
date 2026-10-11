# screen-stream

Privacy-preserving, low-latency screen capture and P2P frame delivery (Go + cgo + pion WebRTC).

## What's included

- **Zero-alloc hot path** (engine freelist) — capture → diff → XOR protect → PII redact
- **macOS ScreenCaptureKit** at **Retina native pixels** (`backingScaleFactor`)
- **Linux** X11/XShm + PipeWire API skeleton
- **Dirty-region** encoding + optional **zlib** payload compression
- **Chunked DataChannel** (60 KiB) for FHD / 3K / 4K packets
- **pion WebRTC** loopback + HTTP signaling hub for two-process peers
- **Receiver reconstruct** + PNG snapshot

## Quick start

```bash
go work sync
go test ./engine/ ./streamer/ -count=1

# FHD synthetic → WebRTC → PNG
go run ./cmd/screen-stream -synthetic -webrtc -width 1920 -height 1080 \
  -seconds 2 -snapshot /tmp/ss-fhd.png

# Live Retina native (e.g. 2940×1912) + WebRTC
go run ./cmd/screen-stream -synthetic=false -webrtc -scale 100 \
  -seconds 2 -snapshot /tmp/ss-live.png

# Faster live (75% scale) if p99 > 10ms
go run ./cmd/screen-stream -synthetic=false -scale 75 -seconds 3
```

Exit `2` = Screen Recording / portal permission denied.

## Flags

| Flag | Default | Meaning |
|------|---------|---------|
| `-synthetic` | true | Generated frames |
| `-width` / `-height` | 1920×1080 | Synthetic size |
| `-scale` | 100 | Capture scale % (100 = Retina pixels) |
| `-max-width` / `-max-height` | 0 | Cap after scale |
| `-webrtc` | false | pion DataChannel + reconstruct |
| `-compress` | true | zlib on WebRTC payloads |
| `-snapshot` | | Write reconstructed PNG |
| `-fps` | 60 | Target FPS |
| `-block` | 64 | Diff block size |
| `-exclude` | | macOS bundle IDs to omit |
| `-seconds` | 3 | Duration |

## Architecture

```
engine/     Grabber → pool → StreamPipeline → DirtyPacket
streamer/   DeliveryRing → chunk/compress → DataChannel
            ChunkReassembler → FrameReconstructor
cmd/        bench / live / snapshot
```

Design: `docs/superpowers/specs/2026-10-12-screen-stream-design.md`
