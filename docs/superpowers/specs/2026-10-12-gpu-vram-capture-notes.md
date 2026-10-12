# Notes: GPU VRAM capture (future, not planned for v1.1)

**Status:** research only — do not implement without a new design + threat review.

## Today

Frames come from **OS screen-capture APIs** (SCK / XShm / PipeWire). We never map other processes’ GPU textures.

## What “from VRAM” would mean

| Approach | Idea | Reality |
|----------|------|---------|
| IOSurface / Metal shared textures | Zero-copy from compositor | Still goes through ScreenCaptureKit / WindowServer; same privacy prompts |
| DXGI Desktop Duplication (Windows) | GPU texture → CPU map | Platform-specific; still “desktop duplication”, not arbitrary VRAM scrape |
| CUDA/Vulkan peek at foreign allocations | Read another app’s framebuffer | Blocked by OS isolation; malware-shaped; will not ship |
| Own offscreen FBO | Capture only our GL/Metal context | Not a full desktop |

Useful optimization later: keep SCK `CVPixelBuffer` **IOSurface-backed** and avoid an extra `memcpy` into our staging buffer (still not “raw VRAM dump”).

## Why not now

1. Privacy / detectability unchanged or worse (still screen recording entitlement).
2. Security boundary: foreign VRAM access is not a legitimate product path.
3. Portability: no shared API across darwin/linux/windows.

## If we revisit

Prefer: **zero-copy path from OS-provided IOSurface/DMA-BUF into encoder**, not GPU memory scraping. Separate design doc required.
