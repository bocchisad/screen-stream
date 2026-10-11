#ifndef SCREEN_STREAM_VISION_OCR_H
#define SCREEN_STREAM_VISION_OCR_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    uint16_t x, y, w, h;
} ss_ocr_rect;

/*
 * Run on-device text recognition on a BGRA buffer.
 * Writes up to max_out rects; each texts[i] is malloc'd UTF-8 (caller frees).
 * Returns number of lines, or <0 on error, 0 if unavailable.
 */
int ss_vision_ocr(
    const uint8_t *bgra,
    int width,
    int height,
    int stride,
    ss_ocr_rect *rects,
    char **texts,
    int max_out
);

#ifdef __cplusplus
}
#endif

#endif
