#ifndef SCREEN_STREAM_PROTECT_H
#define SCREEN_STREAM_PROTECT_H

#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Rolling XOR over buffer; updates *key_state. In-place, no heap. */
void ss_xor_buffer(uint8_t *buf, size_t len, uint64_t *key_state);

/* Block hashes: out_hashes length = blocks_x * blocks_y (uint64 each). */
void ss_block_hashes(
    const uint8_t *bgra,
    int width,
    int height,
    int stride,
    int block_size,
    uint64_t *out_hashes
);

/*
 * Compare current vs previous hashes; write dirty AABBs into out_rects
 * (x,y,w,h as uint16 packed: 4 * max_out). Returns count. Merges adjacent.
 */
int ss_diff_blocks(
    const uint64_t *curr,
    const uint64_t *prev,
    int blocks_x,
    int blocks_y,
    int block_size,
    int frame_w,
    int frame_h,
    uint16_t *out_rects, /* 4 * max_out */
    int max_out,
    int force_all
);

/* Fill BGRA rect with black (0,0,0,255). */
void ss_fill_black(
    uint8_t *bgra,
    int stride,
    int x, int y, int w, int h
);

#ifdef __cplusplus
}
#endif

#endif
