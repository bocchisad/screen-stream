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

static Display *gDisp = NULL;
static Window gRoot = 0;
static XImage *gImage = NULL;
static XShmSegmentInfo gShm;
static int gUseShm = 0;
static int gWidth = 0;
static int gHeight = 0;

int ss_x11_available(void) {
    const char *d = getenv("DISPLAY");
    return (d && d[0]) ? 1 : 0;
}

int ss_x11_start(int screen_index) {
    if (!ss_x11_available()) return 2;
    gDisp = XOpenDisplay(NULL);
    if (!gDisp) return 2;

    int scr = screen_index;
    if (scr < 0 || scr >= ScreenCount(gDisp)) scr = DefaultScreen(gDisp);
    gRoot = RootWindow(gDisp, scr);
    gWidth = DisplayWidth(gDisp, scr);
    gHeight = DisplayHeight(gDisp, scr);

    int major, minor, pixmaps;
    if (!XShmQueryVersion(gDisp, &major, &minor, &pixmaps)) {
        gUseShm = 0;
        gImage = XGetImage(gDisp, gRoot, 0, 0, gWidth, gHeight, AllPlanes, ZPixmap);
        return gImage ? 0 : 2;
    }

    gUseShm = 1;
    gImage = XShmCreateImage(gDisp, DefaultVisual(gDisp, scr), DefaultDepth(gDisp, scr),
                             ZPixmap, NULL, &gShm, gWidth, gHeight);
    if (!gImage) return 2;

    gShm.shmid = shmget(IPC_PRIVATE, (size_t)gImage->bytes_per_line * (size_t)gImage->height, IPC_CREAT | 0600);
    if (gShm.shmid < 0) return 2;
    gShm.shmaddr = (char *)shmat(gShm.shmid, NULL, 0);
    if (gShm.shmaddr == (char *)-1) return 2;
    gShm.readOnly = False;
    gImage->data = gShm.shmaddr;
    if (!XShmAttach(gDisp, &gShm)) return 2;
    XSync(gDisp, False);
    return 0;
}

int ss_x11_capture(uint8_t *dst, int dst_len, ss_frame_info *info) {
    if (!gDisp || !gImage || !dst || !info) return 3;

    if (gUseShm) {
        if (!XShmGetImage(gDisp, gRoot, gImage, 0, 0, AllPlanes)) return 2;
    } else {
        XDestroyImage(gImage);
        gImage = XGetImage(gDisp, gRoot, 0, 0, gWidth, gHeight, AllPlanes, ZPixmap);
        if (!gImage) return 2;
    }

    int stride = gImage->bytes_per_line;
    int need = stride * gHeight;
    if (need > dst_len) return 3;

    /* Normalize to BGRA8; many X servers are BGRA/LSB already for 32-bit. */
    memcpy(dst, gImage->data, (size_t)need);
    info->width = gWidth;
    info->height = gHeight;
    info->stride = stride;
    return 0;
}

int ss_x11_stop(void) {
    if (gDisp && gUseShm && gImage) {
        XShmDetach(gDisp, &gShm);
        shmdt(gShm.shmaddr);
        shmctl(gShm.shmid, IPC_RMID, NULL);
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
    return 0;
}

#else

#include "xshm_linux.h"

int ss_x11_available(void) { return 0; }
int ss_x11_start(int screen_index) { (void)screen_index; return 2; }
int ss_x11_stop(void) { return 0; }
int ss_x11_capture(uint8_t *dst, int dst_len, ss_frame_info *info) {
    (void)dst; (void)dst_len; (void)info;
    return 2;
}

#endif
