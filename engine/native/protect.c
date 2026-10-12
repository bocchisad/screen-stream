#include "protect.h"
#include <string.h>

void ss_xor_buffer(uint8_t *buf, size_t len, uint64_t *key_state) {
    uint64_t k = *key_state;
    size_t i = 0;
    for (; i + 8 <= len; i += 8) {
        k = k * 6364136223846793005ULL + 1ULL;
        uint64_t w;
        memcpy(&w, buf + i, 8); /* avoid unaligned UB */
        w ^= k;
        memcpy(buf + i, &w, 8);
    }
    for (; i < len; i++) {
        k = k * 6364136223846793005ULL + 1ULL;
        buf[i] ^= (uint8_t)(k >> 56);
    }
    *key_state = k;
}

void ss_block_hashes(
    const uint8_t *bgra,
    int width,
    int height,
    int stride,
    int block_size,
    uint64_t *out_hashes
) {
    int bx = (width + block_size - 1) / block_size;
    int by = (height + block_size - 1) / block_size;
    int idx = 0;
    for (int byi = 0; byi < by; byi++) {
        for (int bxi = 0; bxi < bx; bxi++) {
            int x0 = bxi * block_size;
            int y0 = byi * block_size;
            int x1 = x0 + block_size;
            int y1 = y0 + block_size;
            if (x1 > width) x1 = width;
            if (y1 > height) y1 = height;
            uint64_t h = 14695981039346656037ULL;
            for (int y = y0; y < y1; y++) {
                const uint8_t *row = bgra + (size_t)y * (size_t)stride + (size_t)x0 * 4;
                int nbytes = (x1 - x0) * 4;
                int i = 0;
                /* Word-sized FNV — same mixing, far fewer ops on FHD/Retina. */
                for (; i + 8 <= nbytes; i += 8) {
                    uint64_t w;
                    memcpy(&w, row + i, 8);
                    h ^= w;
                    h *= 1099511628211ULL;
                }
                for (; i < nbytes; i++) {
                    h ^= row[i];
                    h *= 1099511628211ULL;
                }
            }
            out_hashes[idx++] = h;
        }
    }
}

static void write_rect(uint16_t *out, int i, int x, int y, int w, int h) {
    out[i * 4 + 0] = (uint16_t)x;
    out[i * 4 + 1] = (uint16_t)y;
    out[i * 4 + 2] = (uint16_t)w;
    out[i * 4 + 3] = (uint16_t)h;
}

int ss_diff_blocks(
    const uint64_t *curr,
    const uint64_t *prev,
    int blocks_x,
    int blocks_y,
    int block_size,
    int frame_w,
    int frame_h,
    uint16_t *out_rects,
    int max_out,
    int force_all
) {
    int count = 0;
    for (int by = 0; by < blocks_y; by++) {
        int run_x0 = -1;
        for (int bx = 0; bx <= blocks_x; bx++) {
            int dirty = 0;
            if (bx < blocks_x) {
                int i = by * blocks_x + bx;
                dirty = force_all || (curr[i] != prev[i]);
            }
            if (dirty && run_x0 < 0) {
                run_x0 = bx;
            } else if (!dirty && run_x0 >= 0) {
                if (count >= max_out) return count;
                int x = run_x0 * block_size;
                int y = by * block_size;
                int w = (bx - run_x0) * block_size;
                int h = block_size;
                if (x + w > frame_w) w = frame_w - x;
                if (y + h > frame_h) h = frame_h - y;
                write_rect(out_rects, count, x, y, w, h);
                count++;
                run_x0 = -1;
            }
        }
    }
    /* Vertical merge: same x,w and contiguous y → one taller rect. */
    int wcount = 0;
    for (int i = 0; i < count; i++) {
        uint16_t x = out_rects[i * 4 + 0];
        uint16_t y = out_rects[i * 4 + 1];
        uint16_t w = out_rects[i * 4 + 2];
        uint16_t h = out_rects[i * 4 + 3];
        if (wcount > 0) {
            uint16_t *p = &out_rects[(wcount - 1) * 4];
            if (p[0] == x && p[2] == w && (int)p[1] + (int)p[3] == (int)y) {
                int nh = (int)p[3] + (int)h;
                if (nh > 65535) nh = 65535;
                p[3] = (uint16_t)nh;
                continue;
            }
        }
        write_rect(out_rects, wcount, x, y, w, h);
        wcount++;
    }
    return wcount;
}

void ss_fill_black(uint8_t *bgra, int stride, int x, int y, int w, int h) {
    if (!bgra || stride <= 0 || w <= 0 || h <= 0 || x < 0 || y < 0) return;
    for (int row = y; row < y + h; row++) {
        uint8_t *p = bgra + (size_t)row * (size_t)stride + (size_t)x * 4;
        for (int col = 0; col < w; col++) {
            p[col * 4 + 0] = 0;
            p[col * 4 + 1] = 0;
            p[col * 4 + 2] = 0;
            p[col * 4 + 3] = 255;
        }
    }
}

int ss_hash_and_diff(
    const uint8_t *plain,
    int width,
    int height,
    int stride,
    int block_size,
    uint64_t *curr_hashes,
    const uint64_t *prev_hashes,
    int have_prev,
    int force_all,
    uint16_t *out_rects,
    int max_out
) {
    int bx = (width + block_size - 1) / block_size;
    int by = (height + block_size - 1) / block_size;
    ss_block_hashes(plain, width, height, stride, block_size, curr_hashes);
    int force = force_all || !have_prev;
    return ss_diff_blocks(curr_hashes, prev_hashes, bx, by, block_size,
                          width, height, out_rects, max_out, force);
}
