# screen-stream backlog

## Done

- Soft gaps, TOFU identity, bubbletea TUI, Windows GDI+DXGI
- **Hardware H.264**
  - darwin: VideoToolbox encode/decode + WebRTC track (`-codec=h264`), Annex-B roundtrip fixed
  - linux: VAAPI EncSlice encode + VLD decode (`h264_bs` SPS/PPS/slice parser, IDR SPS/PPS prepend) — fail-closed
  - windows: MediaFoundation `MFT_ENUM_FLAG_HARDWARE` encode/decode — fail-closed

## Next

1. DMA past Go into encoder (IOSurface / DXGI texture → VT/MF/VAAPI without BGRA round-trip)
2. Full Noise IK
3. Cross-host verify: Arch/Hyprland (`vainfo` EncSlice+VLD) + Windows HW MFT — `HardwareEncoder` + `-codec=h264` loopback

## Non-goals

Software encode pretending to be GPU, input injection, cloud SFU, foreign VRAM scrape
