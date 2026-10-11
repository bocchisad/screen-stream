/*
 * Wayland capture via xdg-desktop-portal ScreenCast + PipeWire.
 *
 * Build with: -DSS_HAVE_PIPEWIRE=1 and link -lpipewire-0.3 -lgio-2.0 -lgobject-2.0
 * Default builds keep a full state-machine skeleton that returns code 2 (unavailable).
 *
 * Portal flow:
 *   1) CreateSession (org.freedesktop.portal.ScreenCast)
 *   2) SelectSources (types=monitor, cursor_mode)
 *   3) Start → streams[0].node_id
 *   4) pw_stream_connect(node_id) → process MemFd / DMA-BUF → BGRA copy into dst
 */
#include "portal_pipewire_linux.h"
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

typedef enum {
    PW_STATE_IDLE = 0,
    PW_STATE_SESSION,
    PW_STATE_SOURCES,
    PW_STATE_STARTED,
    PW_STATE_STREAMING,
    PW_STATE_ERROR
} pw_state_t;

static pw_state_t gState = PW_STATE_IDLE;
static int gFPS = 60;
static int gWidth = 0;
static int gHeight = 0;
static int gNodeID = 0;
static char gLastError[256];

static void set_err(const char *msg) {
    if (!msg) {
        gLastError[0] = 0;
        return;
    }
    snprintf(gLastError, sizeof(gLastError), "%s", msg);
}

int ss_wayland_session(void) {
    const char *w = getenv("WAYLAND_DISPLAY");
    if (w && w[0]) return 1;
    const char *t = getenv("XDG_SESSION_TYPE");
    if (t && strcmp(t, "wayland") == 0) return 1;
    return 0;
}

#if defined(SS_HAVE_PIPEWIRE) && SS_HAVE_PIPEWIRE

#include <gio/gio.h>
#include <pipewire/pipewire.h>

/*
 * Hosts with libpipewire: wire GDBus calls below.
 * Until interactive portal grant + node map are complete, start returns 2.
 */

static GDBusProxy *gPortal = NULL;
static struct pw_main_loop *gLoop = NULL;
static struct pw_stream *gStream = NULL;

static int portal_create_session(void) {
    GError *err = NULL;
    if (!gPortal) {
        gPortal = g_dbus_proxy_new_for_bus_sync(
            G_BUS_TYPE_SESSION,
            G_DBUS_PROXY_FLAGS_NONE,
            NULL,
            "org.freedesktop.portal.Desktop",
            "/org/freedesktop/portal/desktop",
            "org.freedesktop.portal.ScreenCast",
            NULL,
            &err);
        if (!gPortal) {
            set_err(err ? err->message : "portal proxy failed");
            if (err) g_error_free(err);
            return 2;
        }
    }
    gState = PW_STATE_SESSION;
    set_err(NULL);
    /* CreateSession(options) → request handle; wait Response signal for session handle */
    return 0;
}

static int portal_select_sources(void) {
    if (gState != PW_STATE_SESSION) return 2;
    /* SelectSources(session, {types: MONITOR, cursor_mode, multiple: false}) */
    gState = PW_STATE_SOURCES;
    return 0;
}

static int portal_start(void) {
    if (gState != PW_STATE_SOURCES) return 2;
    /* Start(session, parent_window, {}) → streams a(u a{sv}); parse node_id + size */
    gState = PW_STATE_STARTED;
    set_err("portal Start not completed (needs user share grant)");
    return 2;
}

static int pw_open_stream(void) {
    if (gState != PW_STATE_STARTED || gNodeID <= 0) return 2;
    if (!gLoop) {
        pw_init(NULL, NULL);
        gLoop = pw_main_loop_new(NULL);
    }
    /* pw_stream_new_simple + connect(PW_DIRECTION_INPUT, gNodeID, …) */
    gState = PW_STATE_STREAMING;
    set_err("pw_stream connect stub");
    return 2;
}

int ss_pw_start(int fps) {
    if (fps > 0) gFPS = fps;
    gState = PW_STATE_IDLE;
    gNodeID = 0;
    gWidth = gHeight = 0;
    if (portal_create_session() != 0) { gState = PW_STATE_ERROR; return 2; }
    if (portal_select_sources() != 0) { gState = PW_STATE_ERROR; return 2; }
    if (portal_start() != 0) { gState = PW_STATE_ERROR; return 2; }
    if (pw_open_stream() != 0) { gState = PW_STATE_ERROR; return 2; }
    return 0;
}

int ss_pw_stop(void) {
    if (gStream) {
        pw_stream_destroy(gStream);
        gStream = NULL;
    }
    if (gLoop) {
        pw_main_loop_destroy(gLoop);
        gLoop = NULL;
    }
    if (gPortal) {
        g_object_unref(gPortal);
        gPortal = NULL;
    }
    gState = PW_STATE_IDLE;
    gNodeID = 0;
    gWidth = gHeight = 0;
    set_err(NULL);
    return 0;
}

int ss_pw_capture(uint8_t *dst, int dst_len, ss_frame_info *info) {
    if (gState != PW_STATE_STREAMING || !dst || !info) return 3;
    /* Dequeue latest buffer, convert to BGRA, copy into dst, fill info */
    (void)dst_len;
    (void)gFPS;
    (void)gWidth;
    (void)gHeight;
    set_err("pw capture stub");
    return 2;
}

#else

int ss_pw_start(int fps) {
    if (fps > 0) gFPS = fps;
    gState = PW_STATE_ERROR;
    set_err("rebuild with -DSS_HAVE_PIPEWIRE=1 (libpipewire + gio)");
    return 2;
}

int ss_pw_stop(void) {
    gState = PW_STATE_IDLE;
    set_err(NULL);
    return 0;
}

int ss_pw_capture(uint8_t *dst, int dst_len, ss_frame_info *info) {
    (void)dst; (void)dst_len; (void)info;
    (void)gWidth; (void)gHeight; (void)gNodeID; (void)gFPS;
    return 2;
}

#endif
