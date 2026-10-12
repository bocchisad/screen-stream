/*
 * Wayland capture via xdg-desktop-portal ScreenCast + PipeWire.
 *
 * Build with: -DSS_HAVE_PIPEWIRE=1 and link -lpipewire-0.3 -lgio-2.0 -lgobject-2.0 -lgthread-2.0
 * Default builds keep a skeleton that returns code 2 (unavailable).
 */
#include "portal_pipewire_linux.h"
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <stdint.h>

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
#include <spa/param/video/format-utils.h>
#include <spa/param/video/type-info.h>
#include <spa/debug/types.h>
#include <pthread.h>
#include <errno.h>
#include <unistd.h>
#include <fcntl.h>
#include <sys/mman.h>

static GDBusProxy *gPortal = NULL;
static GDBusConnection *gConn = NULL;
static char gSessionHandle[256];
static char gRequestPath[256];
static guint gResponseSub = 0;

static struct pw_main_loop *gLoop = NULL;
static struct pw_context *gCtx = NULL;
static struct pw_core *gCore = NULL;
static struct pw_stream *gStream = NULL;
static pthread_t gLoopThread;
static int gLoopRunning = 0;

static pthread_mutex_t gFrameMu = PTHREAD_MUTEX_INITIALIZER;
static uint8_t *gFrameBGRA = NULL;
static size_t gFrameCap = 0;
static size_t gFrameLen = 0;
static int gFrameReady = 0;
static int gFormat = SPA_VIDEO_FORMAT_BGRA;
/* 0 = cpu: memcpy PW→gFrameBGRA then capture memcpy→dst; 1 = hold mapped buf, one copy→dst */
static int gCaptureMode = 0;
static struct pw_buffer *gHeldBuf = NULL;
static const uint8_t *gHeldData = NULL;
static size_t gHeldSize = 0;

static void release_held_locked(void) {
    if (gHeldBuf && gStream) {
        pw_stream_queue_buffer(gStream, gHeldBuf);
    }
    gHeldBuf = NULL;
    gHeldData = NULL;
    gHeldSize = 0;
}

/* Copy/convert SPA raw pixels into tightly packed BGRA dst (need = w*h*4). */
static void copy_spa_to_bgra(uint8_t *dst, size_t need, const uint8_t *src, size_t src_sz, int format) {
    if (!dst || !src || need == 0) return;
    if (format == SPA_VIDEO_FORMAT_BGRA || format == SPA_VIDEO_FORMAT_BGRx) {
        size_t n = src_sz < need ? src_sz : need;
        memcpy(dst, src, n);
        if (n < need) memset(dst + n, 0, need - n);
    } else if (format == SPA_VIDEO_FORMAT_RGBA || format == SPA_VIDEO_FORMAT_RGBx) {
        size_t px = need / 4;
        size_t avail = src_sz / 4;
        if (avail < px) px = avail;
        for (size_t i = 0; i < px; i++) {
            dst[i * 4 + 0] = src[i * 4 + 2];
            dst[i * 4 + 1] = src[i * 4 + 1];
            dst[i * 4 + 2] = src[i * 4 + 0];
            dst[i * 4 + 3] = 255;
        }
        if (px * 4 < need) memset(dst + px * 4, 0, need - px * 4);
    } else {
        memset(dst, 0, need);
    }
}

int ss_pw_set_capture_mode(int mode) {
    pthread_mutex_lock(&gFrameMu);
    gCaptureMode = (mode == 1) ? 1 : 0;
    if (gCaptureMode == 0) {
        release_held_locked();
    }
    pthread_mutex_unlock(&gFrameMu);
    return 0;
}

typedef struct {
    GMainLoop *loop;
    int done;
    int code;
    GVariant *results; /* floating ownership transferred */
} portal_wait_t;

static void on_response(GDBusConnection *conn, const gchar *sender, const gchar *path,
                        const gchar *iface, const gchar *signal, GVariant *params, gpointer user) {
    (void)conn; (void)sender; (void)path; (void)iface; (void)signal;
    portal_wait_t *w = (portal_wait_t *)user;
    guint32 response = 1;
    GVariant *results = NULL;
    g_variant_get(params, "(u@a{sv})", &response, &results);
    w->code = (int)response;
    w->results = results;
    w->done = 1;
    if (w->loop) g_main_loop_quit(w->loop);
}

static int portal_wait_response(const char *request_path, int timeout_ms, GVariant **out_results) {
    portal_wait_t w = {0};
    w.loop = g_main_loop_new(NULL, FALSE);
    gResponseSub = g_dbus_connection_signal_subscribe(
        gConn, "org.freedesktop.portal.Desktop", "org.freedesktop.portal.Request",
        "Response", request_path, NULL, G_DBUS_SIGNAL_FLAGS_NONE, on_response, &w, NULL);

    guint tid = g_timeout_add(timeout_ms, (GSourceFunc)g_main_loop_quit, w.loop);
    g_main_loop_run(w.loop);
    g_source_remove(tid);
    g_dbus_connection_signal_unsubscribe(gConn, gResponseSub);
    gResponseSub = 0;
    g_main_loop_unref(w.loop);

    if (!w.done) {
        set_err("portal Response timeout");
        return 2;
    }
    if (w.code != 0) {
        set_err("portal request cancelled/denied");
        if (w.results) g_variant_unref(w.results);
        return 1;
    }
    if (out_results) *out_results = w.results;
    else if (w.results) g_variant_unref(w.results);
    return 0;
}

static int portal_ensure(void) {
    GError *err = NULL;
    if (!gConn) {
        gConn = g_bus_get_sync(G_BUS_TYPE_SESSION, NULL, &err);
        if (!gConn) {
            set_err(err ? err->message : "session bus");
            if (err) g_error_free(err);
            return 2;
        }
    }
    if (!gPortal) {
        gPortal = g_dbus_proxy_new_sync(
            gConn, G_DBUS_PROXY_FLAGS_NONE, NULL,
            "org.freedesktop.portal.Desktop",
            "/org/freedesktop/portal/desktop",
            "org.freedesktop.portal.ScreenCast",
            NULL, &err);
        if (!gPortal) {
            set_err(err ? err->message : "ScreenCast proxy");
            if (err) g_error_free(err);
            return 2;
        }
    }
    return 0;
}

static int portal_create_session(void) {
    if (portal_ensure() != 0) return 2;
    GError *err = NULL;
    char token[64];
    snprintf(token, sizeof(token), "ss%d", (int)getpid());

    GVariantBuilder opt;
    g_variant_builder_init(&opt, G_VARIANT_TYPE("a{sv}"));
    g_variant_builder_add(&opt, "{sv}", "handle_token", g_variant_new_string(token));
    g_variant_builder_add(&opt, "{sv}", "session_handle_token", g_variant_new_string(token));

    GVariant *ret = g_dbus_proxy_call_sync(
        gPortal, "CreateSession", g_variant_new("(a{sv})", &opt),
        G_DBUS_CALL_FLAGS_NONE, 5000, NULL, &err);
    if (!ret) {
        set_err(err ? err->message : "CreateSession");
        if (err) g_error_free(err);
        return 2;
    }
    const char *req = NULL;
    g_variant_get(ret, "(o)", &req);
    snprintf(gRequestPath, sizeof(gRequestPath), "%s", req ? req : "");
    g_variant_unref(ret);

    GVariant *results = NULL;
    int rc = portal_wait_response(gRequestPath, 15000, &results);
    if (rc != 0) return rc;
    const char *session = NULL;
    if (results) {
        g_variant_lookup(results, "session_handle", "&s", &session);
        if (session) snprintf(gSessionHandle, sizeof(gSessionHandle), "%s", session);
        g_variant_unref(results);
    }
    if (!gSessionHandle[0]) {
        set_err("no session_handle");
        return 2;
    }
    gState = PW_STATE_SESSION;
    set_err(NULL);
    return 0;
}

static int portal_select_sources(void) {
    if (gState != PW_STATE_SESSION) return 2;
    GError *err = NULL;
    char token[64];
    snprintf(token, sizeof(token), "sssrc%d", (int)getpid());

    GVariantBuilder opt;
    g_variant_builder_init(&opt, G_VARIANT_TYPE("a{sv}"));
    g_variant_builder_add(&opt, "{sv}", "handle_token", g_variant_new_string(token));
    g_variant_builder_add(&opt, "{sv}", "types", g_variant_new_uint32(1)); /* MONITOR */
    g_variant_builder_add(&opt, "{sv}", "multiple", g_variant_new_boolean(FALSE));
    g_variant_builder_add(&opt, "{sv}", "cursor_mode", g_variant_new_uint32(2)); /* embedded */

    GVariant *ret = g_dbus_proxy_call_sync(
        gPortal, "SelectSources",
        g_variant_new("(oa{sv})", gSessionHandle, &opt),
        G_DBUS_CALL_FLAGS_NONE, 5000, NULL, &err);
    if (!ret) {
        set_err(err ? err->message : "SelectSources");
        if (err) g_error_free(err);
        return 2;
    }
    const char *req = NULL;
    g_variant_get(ret, "(o)", &req);
    snprintf(gRequestPath, sizeof(gRequestPath), "%s", req ? req : "");
    g_variant_unref(ret);

    GVariant *results = NULL;
    int rc = portal_wait_response(gRequestPath, 60000, &results);
    if (results) g_variant_unref(results);
    if (rc != 0) return rc;
    gState = PW_STATE_SOURCES;
    return 0;
}

static int portal_start(void) {
    if (gState != PW_STATE_SOURCES) return 2;
    GError *err = NULL;
    char token[64];
    snprintf(token, sizeof(token), "ssstart%d", (int)getpid());

    GVariantBuilder opt;
    g_variant_builder_init(&opt, G_VARIANT_TYPE("a{sv}"));
    g_variant_builder_add(&opt, "{sv}", "handle_token", g_variant_new_string(token));

    GVariant *ret = g_dbus_proxy_call_sync(
        gPortal, "Start",
        g_variant_new("(osa{sv})", gSessionHandle, "", &opt),
        G_DBUS_CALL_FLAGS_NONE, 5000, NULL, &err);
    if (!ret) {
        set_err(err ? err->message : "Start");
        if (err) g_error_free(err);
        return 2;
    }
    const char *req = NULL;
    g_variant_get(ret, "(o)", &req);
    snprintf(gRequestPath, sizeof(gRequestPath), "%s", req ? req : "");
    g_variant_unref(ret);

    GVariant *results = NULL;
    int rc = portal_wait_response(gRequestPath, 120000, &results);
    if (rc != 0) return rc;

    gNodeID = 0;
    gWidth = gHeight = 0;
    if (results) {
        GVariant *streams = g_variant_lookup_value(results, "streams", G_VARIANT_TYPE("a(ua{sv})"));
        if (streams && g_variant_n_children(streams) > 0) {
            GVariant *first = g_variant_get_child_value(streams, 0);
            GVariant *props = NULL;
            guint32 node = 0;
            g_variant_get(first, "(u@a{sv})", &node, &props);
            gNodeID = (int)node;
            if (props) {
                GVariant *size = g_variant_lookup_value(props, "size", G_VARIANT_TYPE("(ii)"));
                if (size) {
                    gint w = 0, h = 0;
                    g_variant_get(size, "(ii)", &w, &h);
                    gWidth = w;
                    gHeight = h;
                    g_variant_unref(size);
                }
                g_variant_unref(props);
            }
            g_variant_unref(first);
            g_variant_unref(streams);
        }
        g_variant_unref(results);
    }
    if (gNodeID <= 0) {
        set_err("Start: no PipeWire node_id (share grant?)");
        return 2;
    }
    if (gWidth <= 0) gWidth = 1920;
    if (gHeight <= 0) gHeight = 1080;
    /* Preallocate BGRA staging once (steady-state frames reuse). */
    {
        size_t need = (size_t)gWidth * (size_t)gHeight * 4;
        pthread_mutex_lock(&gFrameMu);
        free(gFrameBGRA);
        gFrameBGRA = need ? (uint8_t *)malloc(need) : NULL;
        gFrameCap = gFrameBGRA ? need : 0;
        gFrameLen = 0;
        gFrameReady = 0;
        pthread_mutex_unlock(&gFrameMu);
        if (need && !gFrameBGRA) {
            set_err("frame buffer alloc");
            return 2;
        }
    }
    gState = PW_STATE_STARTED;
    set_err(NULL);
    return 0;
}

static void on_stream_process(void *data) {
    (void)data;
    struct pw_buffer *b = pw_stream_dequeue_buffer(gStream);
    if (!b || !b->buffer || b->buffer->n_datas < 1) {
        if (b) pw_stream_queue_buffer(gStream, b);
        return;
    }
    struct spa_data *d = &b->buffer->datas[0];
    if (!d->data || d->chunk->size == 0) {
        pw_stream_queue_buffer(gStream, b);
        return;
    }
    int w = gWidth, h = gHeight;
    size_t need = (size_t)w * (size_t)h * 4;
    if (need == 0) {
        pw_stream_queue_buffer(gStream, b);
        return;
    }

    pthread_mutex_lock(&gFrameMu);
    if (gCaptureMode == 1) {
        /* gpu-surface: retain mapped PW buffer; one copy happens in ss_pw_capture. */
        release_held_locked();
        gHeldBuf = b;
        gHeldData = (const uint8_t *)d->data;
        gHeldSize = d->chunk->size;
        gFrameLen = need;
        gFrameReady = 1;
        pthread_mutex_unlock(&gFrameMu);
        return; /* do not queue until next frame or stop */
    }
    if (gFrameCap < need) {
        uint8_t *nb = (uint8_t *)realloc(gFrameBGRA, need);
        if (nb) {
            gFrameBGRA = nb;
            gFrameCap = need;
        }
    }
    if (gFrameBGRA && gFrameCap >= need) {
        copy_spa_to_bgra(gFrameBGRA, need, (const uint8_t *)d->data, d->chunk->size, gFormat);
        gFrameLen = need;
        gFrameReady = 1;
    }
    pthread_mutex_unlock(&gFrameMu);
    pw_stream_queue_buffer(gStream, b);
}

static void on_stream_param_changed(void *data, uint32_t id, const struct spa_pod *param) {
    (void)data;
    if (param == NULL || id != SPA_PARAM_Format) return;
    struct spa_video_info_raw info;
    spa_zero(info);
    if (spa_format_video_raw_parse(param, &info) < 0) return;
    gFormat = info.format;
    if (info.size.width > 0) gWidth = (int)info.size.width;
    if (info.size.height > 0) gHeight = (int)info.size.height;
    size_t need = (size_t)gWidth * (size_t)gHeight * 4;
    if (need == 0) return;
    pthread_mutex_lock(&gFrameMu);
    if (gFrameCap < need) {
        uint8_t *nb = (uint8_t *)realloc(gFrameBGRA, need);
        if (nb) {
            gFrameBGRA = nb;
            gFrameCap = need;
        }
    }
    pthread_mutex_unlock(&gFrameMu);
}

static const struct pw_stream_events stream_events = {
    PW_VERSION_STREAM_EVENTS,
    .param_changed = on_stream_param_changed,
    .process = on_stream_process,
};

static void *loop_thread_main(void *arg) {
    (void)arg;
    if (gLoop) pw_main_loop_run(gLoop);
    return NULL;
}

static int pw_open_stream(void) {
    if (gState != PW_STATE_STARTED || gNodeID <= 0) return 2;
    pw_init(NULL, NULL);
    gLoop = pw_main_loop_new(NULL);
    if (!gLoop) {
        set_err("pw_main_loop_new");
        return 2;
    }
    gCtx = pw_context_new(pw_main_loop_get_loop(gLoop), NULL, 0);
    if (!gCtx) {
        set_err("pw_context_new");
        return 2;
    }
    gCore = pw_context_connect(gCtx, NULL, 0);
    if (!gCore) {
        set_err("pw_context_connect");
        return 2;
    }

    gStream = pw_stream_new_simple(
        pw_main_loop_get_loop(gLoop),
        "screen-stream-capture",
        pw_properties_new(
            PW_KEY_MEDIA_TYPE, "Video",
            PW_KEY_MEDIA_CATEGORY, "Capture",
            PW_KEY_MEDIA_ROLE, "Screen",
            NULL),
        &stream_events, NULL);
    if (!gStream) {
        set_err("pw_stream_new_simple");
        return 2;
    }

    uint8_t buffer[1024];
    struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buffer, sizeof(buffer));
    const struct spa_pod *params[1];
    params[0] = spa_pod_builder_add_object(&b,
        SPA_TYPE_OBJECT_Format, SPA_PARAM_EnumFormat,
        SPA_FORMAT_mediaType, SPA_POD_Id(SPA_MEDIA_TYPE_video),
        SPA_FORMAT_mediaSubtype, SPA_POD_Id(SPA_MEDIA_SUBTYPE_raw),
        SPA_FORMAT_VIDEO_format, SPA_POD_CHOICE_ENUM_Id(4,
            SPA_VIDEO_FORMAT_BGRA, SPA_VIDEO_FORMAT_BGRx,
            SPA_VIDEO_FORMAT_RGBA, SPA_VIDEO_FORMAT_RGBx),
        SPA_FORMAT_VIDEO_size, SPA_POD_CHOICE_RANGE_Rectangle(
            &SPA_RECTANGLE((uint32_t)gWidth, (uint32_t)gHeight),
            &SPA_RECTANGLE(16, 16),
            &SPA_RECTANGLE(8192, 8192)),
        SPA_FORMAT_VIDEO_framerate, SPA_POD_CHOICE_RANGE_Fraction(
            &SPA_FRACTION((uint32_t)gFPS, 1),
            &SPA_FRACTION(0, 1),
            &SPA_FRACTION(240, 1)));

    if (pw_stream_connect(gStream, PW_DIRECTION_INPUT, (uint32_t)gNodeID,
                          PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_MAP_BUFFERS,
                          params, 1) < 0) {
        set_err("pw_stream_connect");
        return 2;
    }

    gLoopRunning = 1;
    if (pthread_create(&gLoopThread, NULL, loop_thread_main, NULL) != 0) {
        set_err("loop thread");
        gLoopRunning = 0;
        return 2;
    }
    gState = PW_STATE_STREAMING;
    set_err(NULL);
    return 0;
}

int ss_pw_start(int fps) {
    if (fps > 0) gFPS = fps;
    gState = PW_STATE_IDLE;
    gNodeID = 0;
    gWidth = gHeight = 0;
    gSessionHandle[0] = 0;
    if (portal_create_session() != 0) { gState = PW_STATE_ERROR; return gLastError[0] ? 2 : 2; }
    if (portal_select_sources() != 0) { gState = PW_STATE_ERROR; return 2; }
    if (portal_start() != 0) { gState = PW_STATE_ERROR; return 2; }
    if (pw_open_stream() != 0) { gState = PW_STATE_ERROR; return 2; }
    return 0;
}

int ss_pw_stop(void) {
    if (gLoop && gLoopRunning) {
        pw_main_loop_quit(gLoop);
        pthread_join(gLoopThread, NULL);
        gLoopRunning = 0;
    }
    if (gStream) {
        pw_stream_destroy(gStream);
        gStream = NULL;
    }
    if (gCore) {
        pw_core_disconnect(gCore);
        gCore = NULL;
    }
    if (gCtx) {
        pw_context_destroy(gCtx);
        gCtx = NULL;
    }
    if (gLoop) {
        pw_main_loop_destroy(gLoop);
        gLoop = NULL;
    }
    if (gPortal) {
        g_object_unref(gPortal);
        gPortal = NULL;
    }
    if (gConn) {
        g_object_unref(gConn);
        gConn = NULL;
    }
    pthread_mutex_lock(&gFrameMu);
    release_held_locked();
    free(gFrameBGRA);
    gFrameBGRA = NULL;
    gFrameCap = gFrameLen = 0;
    gFrameReady = 0;
    pthread_mutex_unlock(&gFrameMu);
    gState = PW_STATE_IDLE;
    gNodeID = 0;
    gWidth = gHeight = 0;
    set_err(NULL);
    return 0;
}

int ss_pw_capture(uint8_t *dst, int dst_len, ss_frame_info *info) {
    if (gState != PW_STATE_STREAMING || !dst || !info) return 3;
    pthread_mutex_lock(&gFrameMu);
    if (!gFrameReady || gFrameLen == 0) {
        pthread_mutex_unlock(&gFrameMu);
        return 4; /* no new frame */
    }
    if ((int)gFrameLen > dst_len) {
        pthread_mutex_unlock(&gFrameMu);
        return 3;
    }
    if (gCaptureMode == 1) {
        if (!gHeldData) {
            pthread_mutex_unlock(&gFrameMu);
            return 4;
        }
        copy_spa_to_bgra(dst, gFrameLen, gHeldData, gHeldSize, gFormat);
        /* keep held until next process replaces it (same as mac CVPixelBuffer retain) */
    } else {
        if (!gFrameBGRA) {
            pthread_mutex_unlock(&gFrameMu);
            return 4;
        }
        memcpy(dst, gFrameBGRA, gFrameLen);
    }
    info->width = gWidth;
    info->height = gHeight;
    info->stride = gWidth * 4;
    gFrameReady = 0;
    pthread_mutex_unlock(&gFrameMu);
    return 0;
}

int ss_pw_output_size(int *out_w, int *out_h, int *out_stride) {
    if (!out_w || !out_h || !out_stride) return 3;
    if (gWidth <= 0 || gHeight <= 0) return 2;
    *out_w = gWidth;
    *out_h = gHeight;
    *out_stride = gWidth * 4;
    return 0;
}

int ss_pw_last_error(char *buf, int buflen) {
    if (!buf || buflen <= 0) return 0;
    size_t n = strlen(gLastError);
    if ((int)n >= buflen) n = (size_t)buflen - 1;
    memcpy(buf, gLastError, n);
    buf[n] = 0;
    return (int)n;
}

#else

int ss_pw_set_capture_mode(int mode) {
    (void)mode;
    return 0;
}

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

int ss_pw_output_size(int *out_w, int *out_h, int *out_stride) {
    (void)out_w; (void)out_h; (void)out_stride;
    return 2;
}

int ss_pw_last_error(char *buf, int buflen) {
    if (!buf || buflen <= 0) return 0;
    size_t n = strlen(gLastError);
    if ((int)n >= buflen) n = (size_t)buflen - 1;
    memcpy(buf, gLastError, n);
    buf[n] = 0;
    return (int)n;
}

#endif
