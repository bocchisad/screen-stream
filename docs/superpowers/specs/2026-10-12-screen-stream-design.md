# Design: Privacy-preserving P2P screen stream engine

**Date:** 2026-10-12  
**Codename:** screen-stream (module path: `github.com/salfetka/screen-stream`)  
**Stack:** Go 1.20+ (core) + cgo (platform capture) + pion/webrtc (P2P data channel)

## Goals

Low-latency cross-platform screen capture and P2P delivery with:

1. Zero heap allocation on the hot capture path (preallocated buffer pool / ring).
2. Stable 60 FPS target; capture + process latency p99 &lt; 10 ms (Full HD, best-effort).
3. Bandwidth savings via block-based dirty-region encoding (frame diff).
4. In-memory buffer protection (fast XOR cipher with rolling key) so plaintext frames do not rest in RAM.
5. On-device PII redaction before network send (API tokens, card-like numbers, credential patterns).
6. WebRTC DataChannel transport without blocking the acquisition thread.

## Non-goals (v0)

- Full remote-desktop input injection.
- Cloud SFU / TURN provisioning (signaling is pluggable; loopback for bench).
- Heavy OCR engines in-tree (pluggable detector; default = pattern/layout scanner).

## Module layout (two Go modules + cmd)

```
screen/
├── go.work
├── docs/superpowers/specs/
├── engine/                 # cgo capture + pipeline (no webrtc)
│   ├── go.mod
│   ├── interface.go
│   ├── pool.go
│   ├── pipeline.go
│   ├── pii.go
│   ├── grabber_darwin.go   //go:build darwin
│   ├── grabber_linux.go    //go:build linux
│   ├── grabber_stub.go     //go:build !darwin && !linux
│   └── native/
│       ├── helper.m / helper.h          # macOS ScreenCaptureKit
│       ├── xshm_linux.c / .h            # X11 shared-memory capture
│       ├── portal_pipewire_linux.c / .h # Wayland portal + PipeWire path
│       └── protect.c / .h               # block diff + buffer XOR
├── streamer/               # pion/webrtc, no cgo
│   ├── go.mod
│   ├── streamer.go
│   └── proto.go
└── cmd/screen-stream/
    ├── go.mod
    └── main.go             # bench, alloc audit, permission errors
```

**Boundary:** `engine` emits `DirtyPacket` only. `streamer` never sees plaintext BGRA and never links cgo.

## Data flow

```
Grabber → pool buffer (BGRA)
       → protect: XOR in-place (C)
       → block diff vs previous (C)
       → PIIDetector rects → black fill (scratch from pool) → re-XOR
       → DirtyPacket (pool)
       → streamer.TryEnqueue (ring depth 3; drop-oldest on full)
       → WebRTC DataChannel (async send loop)
```

## DirtyPacket wire format

| Field | Type |
|-------|------|
| magic | u32 |
| version | u16 |
| flags | u16 (bit0 = keyframe) |
| frame_id | u64 |
| pts_us | u64 |
| width, height, block_size | u16 each |
| region_count | u16 |
| regions[] | x,y,w,h u16; payload_offset, payload_len u32 |
| payload | concatenated protected BGRA tiles |

## Zero-alloc rules

Hot path forbids `make` / growing `append` / `fmt` / map inserts.  
Pools: frame buffers, previous-frame slot, dirty region arrays, packet bytes, PII rect lists.  
Ring depth 3; `TryEnqueue` never blocks capture.

## Platform capture

### macOS 13+
- ScreenCaptureKit `SCStream`, BGRA via `CVPixelBufferLockBaseAddress`.
- `SCContentFilter` for display share and optional app/window exclusion (user-selected share set).
- Permission: screen recording APIs; map denial → `ErrPermissionDenied`.

### Linux
- Detect session: X11 vs Wayland.
- **X11:** Xlib + MIT-SHM (`XShmGetImage`) full path.
- **Wayland:** xdg-desktop-portal ScreenCast → PipeWire → map buffer → BGRA into pool. Build tag `pipewire` when libs present; otherwise clear `ErrBackendUnavailable` with full source still in tree.

## PII redaction

```go
type PIIDetector interface {
    Detect(ctx context.Context, meta FrameMeta, protectedBGRA []byte) ([]Rect, error)
}
```

Default v0: decode-to-scratch (pool) + regex/layout patterns (`ghp_`, card-like with Luhn, password-like). OCR adapter is a second implementation of the same interface.

## Streamer

- pion `PeerConnection` + binary DataChannel `screen-frames`.
- `Signaling` interface; loopback impl for benchmarks.
- Send loop separate from capture; coalesce/drop-oldest under load.

## Errors

- `ErrPermissionDenied` — OS privacy / portal denial  
- `ErrBackendUnavailable` — missing SHM / PipeWire / SCK  
- `ErrNotSupported` — unsupported OS stub  
- `ErrBufferTooSmall` — pool vs stride mismatch  
- `ErrStopped` — grabber stopped  

## Acceptance (v0)

1. `go work` builds on darwin and linux (tags).
2. Bench reports µs/frame; p99 processing &lt; 10 ms target on FHD.
3. Alloc test: 0 heap alloc/frame after warmup on hot path.
4. Diff sends dirty regions; keyframe flag for full refresh.
5. Buffer XOR before retain/send; streamer sees only protected payloads.
6. Default PII detector redacts test patterns.
7. Loopback WebRTC delivers packets without blocking capture.

## Decisions locked

- Delivery scope: full skeleton (all modules).
- Repo shape: two modules (`engine`, `streamer`) + `cmd` via `go.work`.
- Backpressure: drop-oldest, ring depth 3.
- Naming in docs/code comments: privacy / protect / redact (neutral engineering terms).
