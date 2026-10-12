# screen-stream v1.1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the five v1.1 candidates from the [v1 extras spec](../specs/2026-10-12-screen-stream-v1-extras-design.md): TOFU transcript hash, optional STUN, multi-viewer fanout, Linux OCR adapter, metrics on `/status`.

**Architecture:** Keep `engine` (capture/pipeline) and `streamer` (WebRTC/signaling) boundaries. New work is mostly `streamer` + thin CLI flags; Linux OCR is a new `engine` backend behind existing `OCRBackend`.

**Tech Stack:** Go 1.21+, pion/webrtc ICE servers, existing X25519/HKDF, optional Tesseract via CGO or exec adapter (prefer pure interface + stub first).

## Global Constraints

- No cloud SFU; STUN is optional single URL only.
- Token auth stays mandatory for hub/send/recv/preview.
- ECDH + AEAD remain default; TOFU is *detection*, not a full Noise rewrite.
- Do not break v0/v1 acceptance tests (`go test ./engine/ ./streamer/`).
- Prefer loopback hub; document non-loopback + STUN risks.

## Where pixels come from (context — not a task)

We do **not** read GPU VRAM ourselves (no CUDA/Metal texture scrape).

| Platform | Source | What we copy |
|----------|--------|----------------|
| macOS | ScreenCaptureKit → `CMSampleBuffer` → `CVPixelBuffer` | `CVPixelBufferLockBaseAddress` + `memcpy` BGRA into prealloc CPU buffer (`helper.m`) |
| Linux X11 | root window via MIT-SHM (`XShmGetImage`) | SHM → packed BGRA (`xshm_linux.c`) |
| Linux Wayland | xdg-desktop-portal ScreenCast → PipeWire node | map PW buffer → BGRA staging (`portal_pipewire_linux.c`) |

OS may *internally* use GPU to composite the desktop into that buffer; our hot path always sees **CPU BGRA** in the Go `BufferPool`. DirtyPacket tiles are XOR’d CPU-side; WebRTC sends those bytes.

```
Display compositor (GPU/CPU, OS-owned)
        ↓ ScreenCaptureKit / XShm / PipeWire
  CVPixelBuffer / SHM / PW mem (CPU-mapped)
        ↓ memcpy once
  engine pool BGRA → pipeline → DirtyPacket → AEAD/SSCH → DataChannel
```

---

## File map

| File | Role |
|------|------|
| `streamer/kx.go` | Add transcript hash helper |
| `streamer/webrtc.go` | Print/expose hash; ICE config from STUN URL |
| `streamer/http_signaling.go` | Multi-answer or room fanout; `/status` metrics |
| `streamer/auth.go` / `cmd/.../main.go` | Flags `-stun`, `-fingerprint`, metrics |
| `engine/ocr_tesseract_*.go` | Linux OCRBackend |
| `streamer/metrics.go` (new) | Counters snapshot for hub/status |
| `docs/.../v1-extras` / README | Document flags |

---

### Task 1: Signaling TOFU (ECDH transcript hash)

**Files:**
- Modify: `streamer/kx.go`
- Modify: `streamer/webrtc.go` (`DialOfferer` / `DialAnswerer` after `DeriveSessionSecrets`)
- Modify: `cmd/screen-stream/main.go` (print hash on send/recv)
- Test: `streamer/kx_test.go`

**Interfaces:**
- Produces: `func TranscriptHash(localPub, remotePub []byte) string` — hex SHA-256 of `min(pubA,pubB)||max(pubA,pubB)` (order-independent)
- Consumes: existing `DeriveSessionSecrets` pubs

- [ ] **Step 1: Failing test** — same keypair pair → identical hash; swapped local/remote → same hash; different peer → different hash

```go
func TestTranscriptHashSymmetric(t *testing.T) {
    aPriv, aPub, _ := GenerateKXKeypair()
    bPriv, bPub, _ := GenerateKXKeypair()
    _, _, _ = aPriv, bPriv
    h1 := TranscriptHash(aPub, bPub)
    h2 := TranscriptHash(bPub, aPub)
    if h1 != h2 || len(h1) < 16 {
        t.Fatalf("hash mismatch %q vs %q", h1, h2)
    }
}
```

- [ ] **Step 2: Implement `TranscriptHash`** in `kx.go` (SHA-256, sort the two 32-byte pubs lexicographically, hex encode first 16 bytes for display)
- [ ] **Step 3: Store hash on `WebRTCSession`** after ECDH; `SessionFingerprint() string`
- [ ] **Step 4: CLI** — after successful dial, `fmt.Printf("fingerprint=%s\n", rtc.SessionFingerprint())` on send/recv/loopback
- [ ] **Step 5: Optional `-expect-fingerprint=HEX`** — if set and mismatch, exit 1 (TOFU check)
- [ ] **Step 6: Run** `go test ./streamer/ -run Transcript -count=1`
- [ ] **Step 7: Commit** `feat(streamer): ECDH transcript fingerprint for TOFU`

---

### Task 2: Optional STUN (`-stun`)

**Files:**
- Modify: `streamer/webrtc.go` (`defaultPCConfig` / `newPeerConnection`)
- Modify: `cmd/screen-stream/main.go` — flag `-stun=URL` (empty = host-only as today)
- Test: unit test that config includes ICEServer when URL set (table-driven; no live STUN required)

**Interfaces:**
- Produces: `func PeerConfig(stunURL string) webrtc.Configuration`
- Consumes: pion `ICEServers`

- [ ] **Step 1: Test** — `PeerConfig("")` has empty ICEServers; `PeerConfig("stun:stun.l.google.com:19302")` has one server
- [ ] **Step 2: Implement `PeerConfig`**; wire into `newPeerConnection` via package-level or session config field `STUNURL string` on `SessionConfig`
- [ ] **Step 3: CLI** — pass stun into `SessionConfig` for send/recv/loopback
- [ ] **Step 4: README** — one line: optional STUN, still no TURN/SFU
- [ ] **Step 5: Commit** `feat(streamer): optional STUN via -stun`

---

### Task 3: Hub metrics on `/status`

**Files:**
- Create: `streamer/metrics.go` — atomic counters (packets sent/recv, bytes, drops) with `Snapshot() map[string]any`
- Modify: `streamer/webrtc.go` — bump metrics (or keep session-local and register with hub later)
- Modify: `streamer/http_signaling.go` `handleStatus` — merge signaling state + optional process metrics
- Modify: `cmd/screen-stream/main.go` — send/recv already print wire stats; hub `/status` exposes last known if posted

**Simpler v1.1 scope (preferred):** hub `/status` adds fixed fields: `uptimeSec`, `hasOffer`, `hasAnswer`, `iceOffer`, `iceAnswer`, `auth`, plus **POST `/metrics`** from send/recv every N seconds with `{bytesSent, bytesRecv, sent, recv, dropped}`.

**Interfaces:**
- Produces: `type PeerMetrics struct{ BytesSent, BytesRecv, Sent, Recv, Dropped uint64 }`
- Hub stores last metrics under mutex; `/status` JSON includes `peer *PeerMetrics`

- [ ] **Step 1: Test** — hub with Token; POST metrics; GET `/status` contains values (httptest)
- [ ] **Step 2: Implement store + handlers**
- [ ] **Step 3: send/recv goroutine** — every 2s POST metrics with bearer token
- [ ] **Step 4: Commit** `feat(hub): peer metrics on /status`

---

### Task 4: Multi-viewer fanout (2 answerers)

**Files:**
- Modify: `streamer/http_signaling.go` — support N answers **or** “relay” mode
- Modify: `streamer/webrtc.go` / CLI

**Recommended MVP (smallest useful):** **SFU-less fanout via multiple independent sessions** is hard with one hub slot. Instead:

**MVP design:** hub holds one offer; **first answer wins** stays default; add `-mode=relay` on a middle node later — too big.

**v1.1 MVP that fits:** allow **sequential** viewers: after answer consumed, offerer can `POST /offer` again for a new viewer (document restart). Real N-way needs either:
- (A) send creates N PeerConnections (one per recv), hub routes by `room`+`peerId`, or
- (B) recv mesh.

**Pick (A):**

- Hub paths: `/room/{id}/offer/{peer}` or query `?peer=recv1`
- Send maintains `map[peerID]*WebRTCSession`
- Recv passes `-peer=name`

- [ ] **Step 1: Spec spike in code comments / short design note in this plan** — confirm (A)
- [ ] **Step 2: Extend hub to keyed offer/answer by `peer` query param** (default `default`)
- [ ] **Step 3: send** — `-peers=alice,bob` dials N answerers (wait for each)
- [ ] **Step 4: Test** — two loopback answerers against one offerer with in-process dual `LoopbackSignaling` **or** httptest hub with two peer keys
- [ ] **Step 5: Commit** `feat: multi-peer hub keys + send fanout`

---

### Task 5: Linux OCR adapter (pluggable)

**Files:**
- Create: `engine/ocr_tesseract_stub.go` (`!linux || !cgo || !tesseract`)
- Create: `engine/ocr_tesseract_linux.go` (`linux && cgo && tesseract`) — thin wrapper **or** exec `tesseract stdin tsv`
- Modify: `engine/pii_default.go` — if `!OCRAvailable()` but `TesseractAvailable()`, use it
- Test: fake backend already covers detector; add `TestTesseractUnavailableNoPanic`

**Prefer exec-based adapter first** (no libtesseract link hell): write PNG/PGM to temp or stdin, parse TSV boxes → `OCRLine`.

- [ ] **Step 1: Interface stays `OCRBackend.ScanFrame`**
- [ ] **Step 2: `NewTesseractOCRBackend() OCRBackend`** — returns stub if binary missing
- [ ] **Step 3: `NewDefaultPIIDetector`** tries Vision → Tesseract → Pattern-only
- [ ] **Step 4: doctor** prints `tesseract_available=`
- [ ] **Step 5: Commit** `feat(engine): optional tesseract OCR backend`

---

### Task 6: Docs + acceptance checklist

**Files:**
- Modify: `README.md`, v1 extras spec (move candidates → Done)
- [ ] **Step 1: Update README flags** (`-stun`, `-expect-fingerprint`, `-peers`, metrics)
- [ ] **Step 2: Run full** `go test ./engine/ ./streamer/ -count=1`
- [ ] **Step 3: Manual** hub+send+recv with fingerprint print match
- [ ] **Step 4: Commit** `docs: v1.1 usage`

---

## Execution order

1 → 2 → 3 → 5 → 4 → 6  

(Fanout is largest; do after metrics/TOFU/STUN/OCR stub.)

## Done when

- [ ] Fingerprints match on send/recv; `-expect-fingerprint` enforces
- [ ] `-stun` optional and tested offline
- [ ] `/status` shows peer metrics
- [ ] Tesseract path no-ops cleanly without binary
- [ ] Multi-peer hub key works for ≥2 recv (or explicitly deferred with issue note if cut)

## Cut line

If timeboxed: **ship Tasks 1–3 + 6**; defer 4–5 with README “planned”.
