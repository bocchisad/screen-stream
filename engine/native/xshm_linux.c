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

int ss_x11_capture(uint8_t *dst, int dst_len, ss_frame_info *info) {
    if (!gDisp || !gImage || !dst || !info || dst_len <= 0) return 3;

    if (gUseShm) {
        if (!XShmGetImage(gDisp, gRoot, gImage, 0, 0, AllPlanes)) return 2;
    } else {
        XDestroyImage(gImage);
        gImage = XGetImage(gDisp, gRoot, 0, 0, gWidth, gHeight, AllPlanes, ZPixmap);
        if (!gImage) return 2;
    }

    int stride = gImage->bytes_per_line;
    if (stride <= 0 || gHeight <= 0) return 3;
    if ((size_t)stride > SIZE_MAX / (size_t)gHeight) return 3;
    size_t need = (size_t)stride * (size_t)gHeight;
    if (need > (size_t)dst_len) return 3;

    memcpy(dst, gImage->data, need);
    info->width = gWidth;
    info->height = gHeight;
    info->stride = stride;
    return 0;
}

int ss_x11_output_size(int *out_w, int *out_h, int *out_stride) {
    if (!out_w || !out_h || !out_stride) return 3;
    if (!gDisp || gWidth <= 0 || gHeight <= 0) return 2;
    *out_w = gWidth;
    *out_h = gHeight;
    if (gImage) *out_stride = gImage->bytes_per_line;
    else *out_stride = gWidth * 4;
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
