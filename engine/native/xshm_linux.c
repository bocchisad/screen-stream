// X11 + MIT-SHM high-performance capture path.
#ifdef __linux__

#include "xshm_linux.h"
#include <stdlib.h>
#include <string.h>
#include <sys/ipc.h>
#include <sys/shm.h>
#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/extensions/XShm.h>
#include <stdint.h>

static Display *gDisp = NULL;
static Window gRoot = 0;
static XImage *gImage = NULL;
static XShmSegmentInfo gShm;
static int gUseShm = 0;
static int gWidth = 0;
static int gHeight = 0;

static void x11_cleanup(void) {
    if (gDisp && gUseShm && gImage) {
        XShmDetach(gDisp, &gShm);
        if (gShm.shmaddr && gShm.shmaddr != (char *)-1) {
            shmdt(gShm.shmaddr);
        }
        if (gShm.shmid >= 0) {
            shmctl(gShm.shmid, IPC_RMID, NULL);
        }
        gImage->data = NULL;
        XDestroyImage(gImage);
        gImage = NULL;
    } else if (gImage) {
        XDestroyImage(gImage);
        gImage = NULL;
    }
    if (gDisp) {
        XCloseDisplay(gDisp);
        gDisp = NULL;
    }
    gUseShm = 0;
    gWidth = gHeight = 0;
    gShm.shmid = -1;
    gShm.shmaddr = NULL;
}

int ss_x11_available(void) {
    const char *d = getenv("DISPLAY");
    return (d && d[0]) ? 1 : 0;
}

int ss_x11_start(int screen_index) {
    if (!ss_x11_available()) return 2;
    x11_cleanup();
    memset(&gShm, 0, sizeof(gShm));
    gShm.shmid = -1;

    gDisp = XOpenDisplay(NULL);
    if (!gDisp) return 2;

    int scr = screen_index;
    if (scr < 0 || scr >= ScreenCount(gDisp)) scr = DefaultScreen(gDisp);
    gRoot = RootWindow(gDisp, scr);
    gWidth = DisplayWidth(gDisp, scr);
    gHeight = DisplayHeight(gDisp, scr);
    if (gWidth <= 0 || gHeight <= 0) {
        x11_cleanup();
        return 2;
    }

    int major, minor, pixmaps;
    if (!XShmQueryVersion(gDisp, &major, &minor, &pixmaps)) {
        gUseShm = 0;
        gImage = XGetImage(gDisp, gRoot, 0, 0, gWidth, gHeight, AllPlanes, ZPixmap);
        if (!gImage) {
            x11_cleanup();
            return 2;
        }
        return 0;
    }

    gUseShm = 1;
    gImage = XShmCreateImage(gDisp, DefaultVisual(gDisp, scr), DefaultDepth(gDisp, scr),
                             ZPixmap, NULL, &gShm, gWidth, gHeight);
    if (!gImage) {
        x11_cleanup();
        return 2;
    }

    size_t shmsz = (size_t)gImage->bytes_per_line * (size_t)gImage->height;
    gShm.shmid = shmget(IPC_PRIVATE, shmsz, IPC_CREAT | 0600);
    if (gShm.shmid < 0) {
        XDestroyImage(gImage);
        gImage = NULL;
        x11_cleanup();
        return 2;
    }
    gShm.shmaddr = (char *)shmat(gShm.shmid, NULL, 0);
    if (gShm.shmaddr == (char *)-1) {
        shmctl(gShm.shmid, IPC_RMID, NULL);
        gShm.shmid = -1;
        gShm.shmaddr = NULL;
        XDestroyImage(gImage);
        gImage = NULL;
        x11_cleanup();
        return 2;
    }
    gShm.readOnly = False;
    gImage->data = gShm.shmaddr;
    if (!XShmAttach(gDisp, &gShm)) {
        x11_cleanup();
        return 2;
    }
    XSync(gDisp, False);
    return 0;
}

/* Convert one row of XImage pixels into tightly packed BGRA (4 bytes/pixel). */
static void x11_row_to_bgra(XImage *img, const uint8_t *src, uint8_t *dst, int width) {
    int bpp = img->bits_per_pixel;
    unsigned long rmask = img->red_mask;
    unsigned long gmask = img->green_mask;
    unsigned long bmask = img->blue_mask;
    int rshift = 0, gshift = 0, bshift = 0;
    unsigned long m;
    for (m = rmask; m && !(m & 1); m >>= 1) rshift++;
    for (m = gmask; m && !(m & 1); m >>= 1) gshift++;
    for (m = bmask; m && !(m & 1); m >>= 1) bshift++;

    if (bpp == 32 && bmask == 0xff && rmask == 0xff0000) {
        /* Already BGRA / BGRX little-endian — copy 4*width. */
        memcpy(dst, src, (size_t)width * 4);
        return;
    }
    if (bpp == 32 && rmask == 0xff && bmask == 0xff0000) {
        /* RGBA → BGRA */
        for (int x = 0; x < width; x++) {
            dst[x * 4 + 0] = src[x * 4 + 2];
            dst[x * 4 + 1] = src[x * 4 + 1];
            dst[x * 4 + 2] = src[x * 4 + 0];
            dst[x * 4 + 3] = 255;
        }
        return;
    }

    int bytes = bpp / 8;
    if (bytes <= 0) bytes = 4;
    for (int x = 0; x < width; x++) {
        unsigned long pixel = 0;
        const uint8_t *p = src + x * bytes;
        for (int i = 0; i < bytes && i < (int)sizeof(pixel); i++) {
            pixel |= (unsigned long)p[i] << (8 * i);
        }
        uint8_t r = (uint8_t)((pixel & rmask) >> rshift);
        uint8_t g = (uint8_t)((pixel & gmask) >> gshift);
        uint8_t b = (uint8_t)((pixel & bmask) >> bshift);
        dst[x * 4 + 0] = b;
        dst[x * 4 + 1] = g;
        dst[x * 4 + 2] = r;
        dst[x * 4 + 3] = 255;
    }
}

int ss_x11_capture(uint8_t *dst, int dst_len, ss_frame_info *info) {
    if (!gDisp || !gImage || !dst || !info || dst_len <= 0) return 3;

    if (gUseShm) {
        if (!XShmGetImage(gDisp, gRoot, gImage, 0, 0, AllPlanes)) return 2;
    } else {
        XDestroyImage(gImage);
        gImage = XGetImage(gDisp, gRoot, 0, 0, gWidth, gHeight, AllPlanes, ZPixmap);
        if (!gImage) return 2;
    }

    int out_stride = gWidth * 4;
    if (out_stride <= 0 || gHeight <= 0) return 3;
    if ((size_t)out_stride > SIZE_MAX / (size_t)gHeight) return 3;
    size_t need = (size_t)out_stride * (size_t)gHeight;
    if (need > (size_t)dst_len) return 3;

    int src_stride = gImage->bytes_per_line;
    for (int y = 0; y < gHeight; y++) {
        const uint8_t *src = (const uint8_t *)gImage->data + (size_t)y * (size_t)src_stride;
        uint8_t *row = dst + (size_t)y * (size_t)out_stride;
        x11_row_to_bgra(gImage, src, row, gWidth);
    }
    info->width = gWidth;
    info->height = gHeight;
    info->stride = out_stride;
    return 0;
}

int ss_x11_output_size(int *out_w, int *out_h, int *out_stride) {
    if (!out_w || !out_h || !out_stride) return 3;
    if (!gDisp || gWidth <= 0 || gHeight <= 0) return 2;
    *out_w = gWidth;
    *out_h = gHeight;
    *out_stride = gWidth * 4; /* capture always emits packed BGRA */
    return 0;
}

int ss_x11_stop(void) {
    x11_cleanup();
    return 0;
}

#else

#include "xshm_linux.h"

int ss_x11_available(void) { return 0; }
int ss_x11_start(int screen_index) { (void)screen_index; return 2; }
int ss_x11_output_size(int *out_w, int *out_h, int *out_stride) {
    (void)out_w; (void)out_h; (void)out_stride;
    return 2;
}
int ss_x11_stop(void) { return 0; }
int ss_x11_capture(uint8_t *dst, int dst_len, ss_frame_info *info) {
    (void)dst; (void)dst_len; (void)info;
    return 2;
}

#endif
