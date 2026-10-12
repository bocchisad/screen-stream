# Design: screen-stream v1 extras (security, transport, UX)

**Date:** 2026-10-12  
**Status:** As-built normative addendum to [v0 design](./2026-10-12-screen-stream-design.md)  
**Module:** `github.com/bocchisad/screen-stream`  
**Codename layer:** v1 extras (shipped beyond original v0 acceptance)

## Relation to v0

v0 locked: capture → pool → XOR/diff/PII → `DirtyPacket` → ring → WebRTC DataChannel + loopback signaling.

This document **locks** features that shipped above that baseline and defines their contracts so they are not treated as ad-hoc. It does **not** reopen v0 non-goals (input injection, cloud SFU/TURN provisioning, heavy in-tree OCR engines, GPU encode).

```
v0 core                          v1 extras (this doc)
─────────────────────────        ────────────────────────────────
DirtyPacket + ring               SSCH chunking + zlib + SSAE AEAD
Loopback Signaling               HTTP hub + mandatory token
Shared CLI XOR seed (P2P)        X25519 ECDH → AEAD key + XOR seed
Pattern PII                      Vision OCR (darwin) + ASCII pixel scan
—                                MJPEG preview + PNG snapshot + doctor
```

## Goals (v1 extras)

1. **Confidentiality on the wire** — ChaCha20-Poly1305 over DirtyPacket payloads (not RAM XOR).
2. **Ephemeral session keys** — no shared `-xor-seed` for P2P; X25519 on signaling + HKDF.
3. **Local signaling auth** — hub/preview require a shared bearer token; bind loopback by default.
4. **Large-frame transport** — chunk DirtyPackets under SCTP limits; optional zlib.
5. **Operator UX** — reconstruct viewer (MJPEG/PNG), doctor, honest CLI modes (`hub`/`send`/`recv`).
6. **Stronger on-device redact** — Vision OCR where available; zero-alloc ASCII overlay scan always on `-pii`.

## Non-goals (still)

- TURN / STUN cloud catalogs; NAT traversal beyond host candidates.
- Multi-party rooms / SFU.
- End-to-end identity (TOFU fingerprints, certificate pinning).
- Browser viewer (MJPEG is intentional thin client).
- Replacing DirtyPacket with video codecs.

---

## 1. Session key exchange (ECDH)

### Contract

- Each peer generates an ephemeral **X25519** keypair at dial time.
- Offer and answer carry `SessionDesc{ SDP, PubKey[32] }` (HTTP JSON field `pubKey`, base64).
- `DeriveSessionSecrets(priv, peerPub)`:
  - ECDH shared secret
  - HKDF-SHA256  
    - salt: `screen-stream-v1`  
    - info: `session-v1`  
    - out: 32-byte AEAD key + 8-byte little-endian XOR seed (`0` remapped to `1`)
- Sender pipeline and receiver reconstructor **must** use the derived XOR seed after keys are ready (`OnSessionReady` / `SessionSeed()`).
- CLI `-xor-seed` applies only to local `bench` (no ECDH).

### Acceptance

- [ ] Two peers derive identical AEAD key and XOR seed (unit: `TestDeriveSessionSecretsSymmetric`).
- [ ] Loopback WebRTC reconstructs frames without a CLI shared seed.
- [ ] Missing/short `pubKey` on hub POST → HTTP 400.

---

## 2. Wire AEAD (SSAE)

### Framing

| Field | Size | Notes |
|-------|------|--------|
| magic | u32 | `0x53534145` ("SSAE") |
| nonce | 12 B | random per seal |
| ciphertext+tag | … | ChaCha20-Poly1305 over DirtyPacket bytes |

- Seal after Pack (+ optional zlib); Open before Unpack on recv.
- Legacy seed→key via HKDF (`wire-aead-v1`) retained for tests only; production dial uses ECDH key.
- `DisableAEAD` is test-only.

### Acceptance

- [ ] Round-trip seal/open; wrong key fails auth.
- [ ] Reassembler feeds OpenWire; corrupt tag drops packet (no panic).

---

## 3. Chunking (SSCH) + zlib

### SSCH

| Field | Size |
|-------|------|
| magic | u32 `0x53534348` |
| msg_id | u32 |
| index / count | u16 / u16 |
| total_len | u32 |
| payload | … |

- Default chunk ≈ 60 KiB (under typical SCTP).
- Caps: `MaxWireMessage` 32 MiB, `MaxChunkFragments` 2048, `MaxPendingChunks` 64.
- Ordered reliable DataChannel required for reassembly.

### zlib

- Optional compress of DirtyPacket payload before AEAD (`-compress`, default on for WebRTC modes).
- Decompress cap: `MaxDecompressedPayload` 64 MiB.

### Acceptance

- [ ] FHD keyframe survives chunk reassembly (`TestWebRTCHighResChunked`).
- [ ] Oversized total / bomb zlib rejected.

---

## 4. HTTP signaling hub + auth

### Hub

- Local HTTP only (default `127.0.0.1:8787`): `/offer`, `/answer`, `/ice`, `/status`, `/`.
- One-shot consume of offer/answer (DELETE `/offer` resets).
- Wait budget: `MaxSignalingWaitSeconds` (90).

### Auth

- Token via `-token` or `SCREEN_STREAM_TOKEN`.
- **Required** for CLI modes `hub`, `send`, `recv`, and any `-preview`.
- Validation: `Authorization: Bearer` or `?token=`; constant-time compare.
- Empty hub `Token` allowed only in unit tests.

### Acceptance

- [ ] Wrong/missing token → 401 on protected routes.
- [ ] CLI exits non-zero if token missing when required.

---

## 5. Preview + snapshot + doctor

### Preview

- MJPEG server on recv/loopback (`-preview=127.0.0.1:8090`).
- Same token as hub; URL embeds `?token=` when set.
- Cap concurrent clients: `MaxPreviewClients` (8).
- Prefer loopback bind; warn otherwise.

### Snapshot

- `-snapshot=path.png` writes reconstructed BGRA as PNG after session.

### Doctor

- `-mode=doctor`: grabber probe, `ocr_available`, `pipewire_builtin`, hub `/status`.

### Acceptance

- [ ] Preview refuses unauthenticated clients when token set.
- [ ] Doctor prints capability booleans without crashing if hub down.

---

## 6. PII extras (OCR + pixel scan)

### Behavior

- `-pii` and `-ocr` both select `NewDefaultPIIDetector()`:
  - **darwin+cgo:** Vision OCR; frame 1 **sync** (same-frame glyphs); later frames async every N (default 3); ASCII pixel scan on.
  - **else:** PatternDetector ASCII-luminance scan only.
- Pixel scan: grayscale pixels with luminance ≈ ASCII codepoint; hand matchers (ghp_, Luhn PAN, password=/api_key=) — **no heap** on Process hot path.
- OCR async scratch wiped with `clear` after scan; pipeline `plain` cleared after Process.

### Acceptance

- [ ] `TestPipelineZeroAllocWithPixelScan` → 0 alloc/op.
- [ ] `TestOCRSyncFirstSameFrame` → hits on frame 1.
- [ ] `TestOCRScratchWiped`.

---

## 7. Capture hardening (v1)

- macOS SCK: preallocate BGRA staging at start; steady-state no per-frame `malloc`.
- PipeWire (Linux, default via pkg-config): preallocate after portal Start; `-tags nopipewire` without libs.
- X11: normalize to packed BGRA.
- Go wait-loops use `time.Sleep` (not `time.After`) to avoid timer allocs.

---

## CLI surface (locked)

| Mode | Role |
|------|------|
| `bench` | local pipeline |
| `loopback` | in-process WebRTC (+ optional preview) |
| `hub` | signaling |
| `send` | offerer capture |
| `recv` | answerer reconstruct |
| `doctor` | diagnostics |

Notable flags: `-token`, `-preview`, `-snapshot`, `-pii`/`-ocr`, `-compress`, `-exclude`, `-exclude-window`, `-scale`, `-xor-seed` (bench only).

---

## Threat model (honest)

| Layer | Protects against | Does not protect |
|-------|------------------|------------------|
| RAM XOR | casual memory scrapes | forensic / cold-boot |
| Wire AEAD | passive network observer (if signaling not MITM) | active MITM on hub who swaps ECDH pubs |
| Hub token | LAN strangers hitting loopback-bound ports | anyone who stole the token |
| PII redact | accidental secret pixels on send | adversarial steganography / tiny fonts OCR miss |

**Mitigation gap (documented, not v1):** signaling MITM can still do key substitution. Future: pin fingerprint / Noise IK. Out of scope here.

---

## Testing matrix

| Area | Tests |
|------|--------|
| ECDH | `kx_test.go` |
| AEAD / auth | `aead_test.go` |
| Chunk / zlib | `chunk_test.go`, `compress_test.go` |
| WebRTC e2e | `webrtc_test.go` |
| Alloc / latency | `alloc_test.go` |
| PII / OCR | `pipeline_test.go`, `pii_ocr_test.go` |

---

## v1.1 status (implemented)

1. **Signaling TOFU** — `fingerprint=` + `-expect-fingerprint`
2. **Optional STUN** — `-stun=URL`
3. **Multi-viewer fanout** — hub `?peer=` + send `-peers=`
4. **Linux OCR adapter** — Tesseract CLI backend when on PATH
5. **Metrics export** — POST `/metrics`, shown on `/status`

GPU VRAM capture: research notes only — see `2026-10-12-gpu-vram-capture-notes.md`.

---

## Decisions locked

- ECDH on every P2P dial; no shared CLI seed for send/recv.
- Token mandatory for hub/send/recv/preview.
- AEAD default on; chunk + zlib default on for WebRTC.
- Preview = MJPEG, not a full web app.
- PipeWire linked by default on Linux; opt out with `nopipewire`.
