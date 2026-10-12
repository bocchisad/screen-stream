# screen-stream backlog

## Soft gaps

1. ~~Async OCR: SyncFirst + detached timeout context~~
2. ~~Linux `gpu-surface`: hold PipeWire mapped buffer → one copy into Go dst~~
3. ~~Pipeline: hash/redact/pack in-place on `frame.Pixels`, XOR wipe at end~~
4. ~~Doctor/docs: `-tags nopipewire` / missing libpipewire rebuild hint~~
5. ~~Smoke tests~~

## Future

- DMA-BUF past Go heap, GPU encode, Noise TOFU UI, bubbletea TUI, Windows DXGI

## Non-goals

Input injection, cloud SFU, foreign VRAM scrape, browser viewer
