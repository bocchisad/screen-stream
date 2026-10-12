#ifndef SCREEN_STREAM_HELPER_H
#define SCREEN_STREAM_HELPER_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    int width;
    int height;
    int stride; /* bytes per row */
} ss_frame_info;

/* 0 = ok, 1 = permission, 2 = backend, 3 = other */
int ss_mac_preflight(void);
int ss_mac_request_permission(void);

/* scale_percent: 100 = native pixels, 50 = half, 0 = native. max_w/max_h cap (0 = none). */
int ss_mac_start(int display_index, int fps);
int ss_mac_start_ex(int display_index, int fps, int scale_percent, int max_w, int max_h);
int ss_mac_stop(void);
int ss_mac_display_pixels(int display_index, int *out_w, int *out_h);

/* After ss_mac_start_ex: configured output geometry (stride ≈ width*4 until first frame). */
int ss_mac_output_size(int *out_w, int *out_h, int *out_stride);

/*
 * Copies latest BGRA frame into dst (dst_len bytes).
 * Returns 0 on success, fills info. Non-blocking: returns 4 if no new frame.
 */
int ss_mac_capture(uint8_t *dst, int dst_len, ss_frame_info *info);

int ss_mac_set_exclude_bundle(const char *bundle_id);
int ss_mac_set_exclude_window(uint32_t window_id);
int ss_mac_clear_excludes(void);

/* 0 = cpu staging (default), 1 = gpu-surface: retain CVPixelBuffer, one copy into dst */
int ss_mac_set_capture_mode(int mode);

#ifdef __cplusplus
}
#endif

#endif
