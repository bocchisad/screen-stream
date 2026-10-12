#ifndef SCREEN_STREAM_XSHM_LINUX_H
#define SCREEN_STREAM_XSHM_LINUX_H

#include <stdint.h>
#include "helper.h"

#ifdef __cplusplus
extern "C" {
#endif

int ss_x11_available(void);
int ss_x11_start(int screen_index);
int ss_x11_stop(void);
int ss_x11_output_size(int *out_w, int *out_h, int *out_stride);
int ss_x11_capture(uint8_t *dst, int dst_len, ss_frame_info *info);

#ifdef __cplusplus
}
#endif

#endif
