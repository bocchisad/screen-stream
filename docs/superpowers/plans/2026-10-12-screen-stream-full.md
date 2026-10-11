# screen-stream Full Implementation Plan

> **For agentic workers:** execute task-by-task; checkboxes below.

**Goal:** Complete P2P delivery (pion DataChannel), receiver reconstruct, live macOS capture, hardened wire format.

**Architecture:** `engine` produces `DirtyPacket` → `streamer` sends on WebRTC data channel → peer unpacks/reconstructs BGRA. Loopback signaling for local e2e.

**Tech Stack:** Go 1.21+, cgo ScreenCaptureKit/X11, pion/webrtc/v4

## Tasks

- [x] T1: streamer — Unpack + Reconstruct + tests
- [x] T2: streamer — pion PeerConnection + DataChannel (offer/answer loopback)
- [x] T3: cmd — flags `-webrtc`, `-synthetic=false`, live geometry from grabber
- [x] T4: wire format Pack/Unpack + shared XOR keys (pure Go protect; C sources retained)
- [x] T5: e2e verify `go test` + live SCK + webrtc loopback
