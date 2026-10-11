#ifndef SCREEN_STREAM_PORTAL_PIPEWIRE_H
#define SCREEN_STREAM_PORTAL_PIPEWIRE_H

#include <stdint.h>
#include "helper.h"

#ifdef __cplusplus
extern "C" {
#endif

/* Wayland: xdg-desktop-portal ScreenCast → PipeWire.
 * Full control-plane + buffer map path; requires libpipewire + gio at link time
 * when built with -DSS_HAVE_PIPEWIRE=1.
 */

int ss_wayland_session(void); /* 1 if Wayland session detected */
int ss_pw_start(int fps);
int ss_pw_stop(void);
int ss_pw_capture(uint8_t *dst, int dst_len, ss_frame_info *info);

#ifdef __cplusplus
}
#endif

#endif
