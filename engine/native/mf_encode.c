/*
 * Windows Media Foundation hardware H.264 encode/decode.
 * Enumerates ONLY MFT_ENUM_FLAG_HARDWARE — no software MFT fallback.
 */
#define COBJMACROS
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <oleauto.h>
#include <mfapi.h>
#include <mftransform.h>
#include <mferror.h>
#include <codecapi.h>
#include <wmcodecdsp.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include "mf_encode.h"

struct ss_mf_enc {
    IMFTransform *xform;
    IMFMediaEventGenerator *events;
    ICodecAPI *codec;
    int width, height, fps;
    int input_id, output_id;
    int provides_samples;
    DWORD out_buf_size;
    UINT32 need_input;
    int force_key;
    int64_t frame_i;
    int started;
    uint8_t *nv12;
    size_t nv12_len;
};

struct ss_mf_dec {
    IMFTransform *xform;
    IMFMediaEventGenerator *events;
    int max_w, max_h;
    int input_id, output_id;
    int provides_samples;
    DWORD out_buf_size;
    UINT32 need_input;
    int started;
    int have_fmt;
    uint8_t *last_bgra;
    int last_w, last_h, last_stride;
};

static int g_mf_init = 0;

static int mf_ensure_init(void) {
    if (g_mf_init) return 0;
    HRESULT hr = CoInitializeEx(NULL, COINIT_MULTITHREADED);
    if (FAILED(hr) && hr != RPC_E_CHANGED_MODE) return -1;
    hr = MFStartup(MF_VERSION, MFSTARTUP_FULL);
    if (FAILED(hr)) return -1;
    g_mf_init = 1;
    return 0;
}

static void bgra_to_nv12(const uint8_t *bgra, int stride, int w, int h, uint8_t *nv12) {
    uint8_t *y = nv12;
    uint8_t *uv = nv12 + (size_t)w * (size_t)h;
    for (int j = 0; j < h; j++) {
        const uint8_t *row = bgra + (size_t)j * (size_t)stride;
        for (int i = 0; i < w; i++) {
            int B = row[i * 4 + 0], G = row[i * 4 + 1], R = row[i * 4 + 2];
            y[j * w + i] = (uint8_t)((66 * R + 129 * G + 25 * B + 128) >> 8) + 16;
        }
    }
    for (int j = 0; j < h; j += 2) {
        const uint8_t *r0 = bgra + (size_t)j * (size_t)stride;
        const uint8_t *r1 = bgra + (size_t)(j + 1) * (size_t)stride;
        for (int i = 0; i < w; i += 2) {
            int B = (r0[i * 4] + r0[(i + 1) * 4] + r1[i * 4] + r1[(i + 1) * 4]) / 4;
            int G = (r0[i * 4 + 1] + r0[(i + 1) * 4 + 1] + r1[i * 4 + 1] + r1[(i + 1) * 4 + 1]) / 4;
            int R = (r0[i * 4 + 2] + r0[(i + 1) * 4 + 2] + r1[i * 4 + 2] + r1[(i + 1) * 4 + 2]) / 4;
            int U = ((-38 * R - 74 * G + 112 * B + 128) >> 8) + 128;
            int V = ((112 * R - 94 * G - 18 * B + 128) >> 8) + 128;
            int off = (j / 2) * w + i;
            uv[off] = (uint8_t)U;
            uv[off + 1] = (uint8_t)V;
        }
    }
}

static HRESULT unlock_async(IMFTransform *xform) {
    IMFAttributes *attrs = NULL;
    HRESULT hr = IMFTransform_GetAttributes(xform, &attrs);
    if (FAILED(hr) || !attrs) return S_OK;
    UINT32 async = 0;
    IMFAttributes_GetUINT32(attrs, MF_TRANSFORM_ASYNC, &async);
    if (async) {
        hr = IMFAttributes_SetUINT32(attrs, MF_TRANSFORM_ASYNC_UNLOCK, TRUE);
    }
    IMFAttributes_Release(attrs);
    return hr;
}

static HRESULT activate_hw_encoder(IMFTransform **out) {
    *out = NULL;
    MFT_REGISTER_TYPE_INFO info = {MFMediaType_Video, MFVideoFormat_H264};
    IMFActivate **acts = NULL;
    UINT32 n = 0;
    HRESULT hr = MFTEnumEx(MFT_CATEGORY_VIDEO_ENCODER,
                           MFT_ENUM_FLAG_HARDWARE | MFT_ENUM_FLAG_SORTANDFILTER,
                           NULL, &info, &acts, &n);
    if (FAILED(hr) || n == 0) {
        if (acts) CoTaskMemFree(acts);
        return E_FAIL;
    }
    hr = IMFActivate_ActivateObject(acts[0], &IID_IMFTransform, (void **)out);
    for (UINT32 i = 0; i < n; i++) IMFActivate_Release(acts[i]);
    CoTaskMemFree(acts);
    return hr;
}

static HRESULT activate_hw_decoder(IMFTransform **out) {
    *out = NULL;
    MFT_REGISTER_TYPE_INFO info = {MFMediaType_Video, MFVideoFormat_H264};
    IMFActivate **acts = NULL;
    UINT32 n = 0;
    HRESULT hr = MFTEnumEx(MFT_CATEGORY_VIDEO_DECODER,
                           MFT_ENUM_FLAG_HARDWARE | MFT_ENUM_FLAG_SORTANDFILTER,
                           &info, NULL, &acts, &n);
    if (FAILED(hr) || n == 0) {
        if (acts) CoTaskMemFree(acts);
        return E_FAIL;
    }
    hr = IMFActivate_ActivateObject(acts[0], &IID_IMFTransform, (void **)out);
    for (UINT32 i = 0; i < n; i++) IMFActivate_Release(acts[i]);
    CoTaskMemFree(acts);
    return hr;
}

static HRESULT set_enc_types(ss_mf_enc *e) {
    IMFMediaType *out_mt = NULL, *in_mt = NULL;
    HRESULT hr = MFCreateMediaType(&out_mt);
    if (FAILED(hr)) return hr;
    IMFMediaType_SetGUID(out_mt, MF_MT_MAJOR_TYPE, MFMediaType_Video);
    IMFMediaType_SetGUID(out_mt, MF_MT_SUBTYPE, MFVideoFormat_H264);
    IMFMediaType_SetUINT64(out_mt, MF_MT_FRAME_SIZE, ((UINT64)e->width << 32) | (UINT64)e->height);
    IMFMediaType_SetUINT64(out_mt, MF_MT_FRAME_RATE, ((UINT64)e->fps << 32) | 1ULL);
    IMFMediaType_SetUINT32(out_mt, MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
    IMFMediaType_SetUINT32(out_mt, MF_MT_MPEG2_PROFILE, eAVEncH264VProfile_Base);

    DWORD in_id = 0, out_id = 0;
    hr = IMFTransform_GetStreamIDs(e->xform, 1, &in_id, 1, &out_id);
    if (hr == E_NOTIMPL) {
        in_id = 0;
        out_id = 0;
    } else if (FAILED(hr)) {
        IMFMediaType_Release(out_mt);
        return hr;
    }
    e->input_id = (int)in_id;
    e->output_id = (int)out_id;

    hr = IMFTransform_SetOutputType(e->xform, out_id, out_mt, 0);
    IMFMediaType_Release(out_mt);
    if (FAILED(hr)) return hr;

    hr = MFCreateMediaType(&in_mt);
    if (FAILED(hr)) return hr;
    IMFMediaType_SetGUID(in_mt, MF_MT_MAJOR_TYPE, MFMediaType_Video);
    IMFMediaType_SetGUID(in_mt, MF_MT_SUBTYPE, MFVideoFormat_NV12);
    IMFMediaType_SetUINT64(in_mt, MF_MT_FRAME_SIZE, ((UINT64)e->width << 32) | (UINT64)e->height);
    IMFMediaType_SetUINT64(in_mt, MF_MT_FRAME_RATE, ((UINT64)e->fps << 32) | 1ULL);
    IMFMediaType_SetUINT32(in_mt, MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
    IMFMediaType_SetUINT32(in_mt, MF_MT_DEFAULT_STRIDE, (UINT32)e->width);
    hr = IMFTransform_SetInputType(e->xform, in_id, in_mt, 0);
    IMFMediaType_Release(in_mt);
    return hr;
}

static int annexb_is_key(const uint8_t *data, int len) {
    int i = 0;
    while (i + 4 < len) {
        int start = -1;
        if (data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 0 && data[i + 3] == 1) {
            start = i + 4;
        } else if (data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 1) {
            start = i + 3;
        }
        if (start > 0 && start < len) {
            int t = data[start] & 0x1f;
            if (t == 5 || t == 7) return 1;
            i = start;
            continue;
        }
        i++;
    }
    return 0;
}

static HRESULT drain_events(ss_mf_enc *e, ss_mf_au *out, int out_cap, int *out_n, int wait_ms) {
    if (!e->events) return S_OK;
    DWORD deadline = GetTickCount() + (DWORD)(wait_ms > 0 ? wait_ms : 0);
    for (;;) {
        IMFMediaEvent *ev = NULL;
        HRESULT hr = IMFMediaEventGenerator_GetEvent(e->events, wait_ms > 0 ? 0 : MF_EVENT_FLAG_NO_WAIT, &ev);
        if (hr == MF_E_NO_EVENTS_AVAILABLE) {
            if (wait_ms <= 0 || GetTickCount() >= deadline) break;
            Sleep(1);
            continue;
        }
        if (FAILED(hr) || !ev) break;
        MediaEventType t = 0;
        IMFMediaEvent_GetType(ev, &t);
        if (t == METransformNeedInput) {
            e->need_input++;
        } else if (t == METransformHaveOutput) {
            MFT_OUTPUT_DATA_BUFFER odb;
            memset(&odb, 0, sizeof(odb));
            odb.dwStreamID = (DWORD)e->output_id;
            IMFSample *sample = NULL;
            if (!e->provides_samples) {
                IMFMediaBuffer *buf = NULL;
                MFCreateMemoryBuffer(e->out_buf_size ? e->out_buf_size : 1024 * 1024, &buf);
                MFCreateSample(&sample);
                if (sample && buf) {
                    IMFSample_AddBuffer(sample, buf);
                    IMFMediaBuffer_Release(buf);
                }
                odb.pSample = sample;
            }
            DWORD status = 0;
            hr = IMFTransform_ProcessOutput(e->xform, 0, 1, &odb, &status);
            sample = odb.pSample;
            if (SUCCEEDED(hr) && sample && out && out_n && *out_n < out_cap) {
                IMFMediaBuffer *buf = NULL;
                if (SUCCEEDED(IMFSample_ConvertToContiguousBuffer(sample, &buf)) && buf) {
                    BYTE *p = NULL;
                    DWORD max_len = 0, cur = 0;
                    if (SUCCEEDED(IMFMediaBuffer_Lock(buf, &p, &max_len, &cur)) && p && cur > 0) {
                        uint8_t *copy = (uint8_t *)malloc(cur);
                        if (copy) {
                            memcpy(copy, p, cur);
                            out[*out_n].data = copy;
                            out[*out_n].len = (int)cur;
                            out[*out_n].keyframe = annexb_is_key(copy, (int)cur);
                            LONGLONG pts = 0;
                            IMFSample_GetSampleTime(sample, &pts);
                            out[*out_n].pts_us = pts / 10; /* 100ns → µs */
                            (*out_n)++;
                        }
                        IMFMediaBuffer_Unlock(buf);
                    }
                    IMFMediaBuffer_Release(buf);
                }
            }
            if (sample) IMFSample_Release(sample);
            if (odb.pEvents) IMFCollection_Release(odb.pEvents);
        }
        IMFMediaEvent_Release(ev);
        wait_ms = 0; /* subsequent polls non-blocking within this drain */
    }
    return S_OK;
}

ss_mf_enc *ss_mf_enc_create(int width, int height, int fps, int bitrate_bps, int keyframe_every) {
    if (width < 16 || height < 16) return NULL;
    if (mf_ensure_init() != 0) return NULL;
    if (fps <= 0) fps = 30;
    if (bitrate_bps <= 0) bitrate_bps = width * height * fps / 10;

    ss_mf_enc *e = (ss_mf_enc *)calloc(1, sizeof(*e));
    if (!e) return NULL;
    e->width = width;
    e->height = height;
    e->fps = fps;
    e->nv12_len = (size_t)width * (size_t)height * 3 / 2;
    e->nv12 = (uint8_t *)malloc(e->nv12_len);
    if (!e->nv12) {
        free(e);
        return NULL;
    }

    if (FAILED(activate_hw_encoder(&e->xform)) || !e->xform) {
        ss_mf_enc_destroy(e);
        return NULL;
    }
    unlock_async(e->xform);
    IMFTransform_QueryInterface(e->xform, &IID_IMFMediaEventGenerator, (void **)&e->events);
    IMFTransform_QueryInterface(e->xform, &IID_ICodecAPI, (void **)&e->codec);

    if (FAILED(set_enc_types(e))) {
        ss_mf_enc_destroy(e);
        return NULL;
    }

    if (e->codec) {
        VARIANT v;
        VariantInit(&v);
        v.vt = VT_UI4;
        v.ulVal = eAVEncCommonRateControlMode_CBR;
        ICodecAPI_SetValue(e->codec, &CODECAPI_AVEncCommonRateControlMode, &v);
        v.ulVal = (ULONG)bitrate_bps;
        ICodecAPI_SetValue(e->codec, &CODECAPI_AVEncCommonMeanBitRate, &v);
        v.ulVal = keyframe_every > 0 ? (ULONG)keyframe_every : (ULONG)(fps * 2);
        ICodecAPI_SetValue(e->codec, &CODECAPI_AVEncMPVGOPSize, &v);
        v.vt = VT_BOOL;
        v.boolVal = VARIANT_TRUE;
        ICodecAPI_SetValue(e->codec, &CODECAPI_AVLowLatencyMode, &v);
        VariantClear(&v);
    }

    MFT_OUTPUT_STREAM_INFO osi = {0};
    IMFTransform_GetOutputStreamInfo(e->xform, (DWORD)e->output_id, &osi);
    e->provides_samples = (osi.dwFlags & (MFT_OUTPUT_STREAM_PROVIDES_SAMPLES | MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES)) != 0;
    e->out_buf_size = osi.cbSize ? osi.cbSize : 1024 * 1024;

    IMFTransform_ProcessMessage(e->xform, MFT_MESSAGE_COMMAND_FLUSH, 0);
    IMFTransform_ProcessMessage(e->xform, MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
    IMFTransform_ProcessMessage(e->xform, MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
    e->force_key = 1;
    e->started = 1;
    drain_events(e, NULL, 0, NULL, 50);
    return e;
}

void ss_mf_enc_destroy(ss_mf_enc *e) {
    if (!e) return;
    if (e->xform && e->started) {
        IMFTransform_ProcessMessage(e->xform, MFT_MESSAGE_NOTIFY_END_OF_STREAM, 0);
        IMFTransform_ProcessMessage(e->xform, MFT_MESSAGE_NOTIFY_END_STREAMING, 0);
    }
    if (e->codec) ICodecAPI_Release(e->codec);
    if (e->events) IMFMediaEventGenerator_Release(e->events);
    if (e->xform) IMFTransform_Release(e->xform);
    free(e->nv12);
    free(e);
}

void ss_mf_au_free(ss_mf_au *au) {
    if (au && au->data) {
        free(au->data);
        au->data = NULL;
        au->len = 0;
    }
}

int ss_mf_enc_encode(ss_mf_enc *e, const uint8_t *bgra, int stride, int64_t pts_us,
                     ss_mf_au *out, int out_cap, int *out_n) {
    if (!e || !e->xform || !bgra || !out || !out_n || out_cap < 1) return -1;
    *out_n = 0;
    if (stride <= 0) stride = e->width * 4;

    drain_events(e, out, out_cap, out_n, 0);

    DWORD wait_deadline = GetTickCount() + 500;
    while (e->events && e->need_input == 0 && GetTickCount() < wait_deadline) {
        drain_events(e, out, out_cap, out_n, 5);
    }
    if (e->events && e->need_input == 0) return 1;

    bgra_to_nv12(bgra, stride, e->width, e->height, e->nv12);

    if (e->force_key && e->codec) {
        VARIANT v;
        VariantInit(&v);
        v.vt = VT_UI4;
        v.ulVal = 1;
        ICodecAPI_SetValue(e->codec, &CODECAPI_AVEncVideoForceKeyFrame, &v);
        VariantClear(&v);
        e->force_key = 0;
    }

    IMFMediaBuffer *buf = NULL;
    IMFSample *sample = NULL;
    HRESULT hr = MFCreateMemoryBuffer((DWORD)e->nv12_len, &buf);
    if (FAILED(hr)) return -2;
    BYTE *p = NULL;
    DWORD max_len = 0;
    hr = IMFMediaBuffer_Lock(buf, &p, &max_len, NULL);
    if (FAILED(hr) || !p) {
        IMFMediaBuffer_Release(buf);
        return -2;
    }
    memcpy(p, e->nv12, e->nv12_len);
    IMFMediaBuffer_Unlock(buf);
    IMFMediaBuffer_SetCurrentLength(buf, (DWORD)e->nv12_len);
    hr = MFCreateSample(&sample);
    if (FAILED(hr)) {
        IMFMediaBuffer_Release(buf);
        return -2;
    }
    IMFSample_AddBuffer(sample, buf);
    IMFMediaBuffer_Release(buf);
    LONGLONG t = pts_us * 10; /* µs → 100ns */
    LONGLONG dur = 10000000LL / (e->fps > 0 ? e->fps : 30);
    IMFSample_SetSampleTime(sample, t);
    IMFSample_SetSampleDuration(sample, dur);

    hr = IMFTransform_ProcessInput(e->xform, (DWORD)e->input_id, sample, 0);
    IMFSample_Release(sample);
    if (e->need_input > 0) e->need_input--;
    if (FAILED(hr)) return -3;

    e->frame_i++;
    drain_events(e, out, out_cap, out_n, 15);
    return *out_n > 0 ? 0 : 1;
}

/* ---- decoder ---- */

static HRESULT set_dec_types(ss_mf_dec *d, int w, int h) {
    IMFMediaType *in_mt = NULL, *out_mt = NULL;
    HRESULT hr = MFCreateMediaType(&in_mt);
    if (FAILED(hr)) return hr;
    IMFMediaType_SetGUID(in_mt, MF_MT_MAJOR_TYPE, MFMediaType_Video);
    IMFMediaType_SetGUID(in_mt, MF_MT_SUBTYPE, MFVideoFormat_H264);
    IMFMediaType_SetUINT64(in_mt, MF_MT_FRAME_SIZE, ((UINT64)w << 32) | (UINT64)h);
    IMFMediaType_SetUINT32(in_mt, MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);

    DWORD in_id = 0, out_id = 0;
    hr = IMFTransform_GetStreamIDs(d->xform, 1, &in_id, 1, &out_id);
    if (hr == E_NOTIMPL) {
        in_id = 0;
        out_id = 0;
    } else if (FAILED(hr)) {
        IMFMediaType_Release(in_mt);
        return hr;
    }
    d->input_id = (int)in_id;
    d->output_id = (int)out_id;

    hr = IMFTransform_SetInputType(d->xform, in_id, in_mt, 0);
    IMFMediaType_Release(in_mt);
    if (FAILED(hr)) return hr;

    /* Prefer BGRA output if offered; else NV12. */
    for (DWORD i = 0;; i++) {
        hr = IMFTransform_GetOutputAvailableType(d->xform, out_id, i, &out_mt);
        if (FAILED(hr)) break;
        GUID sub = {0};
        IMFMediaType_GetGUID(out_mt, MF_MT_SUBTYPE, &sub);
        int want = IsEqualGUID(&sub, &MFVideoFormat_NV12) || IsEqualGUID(&sub, &MFVideoFormat_ARGB32);
        if (want) {
            IMFMediaType_SetUINT64(out_mt, MF_MT_FRAME_SIZE, ((UINT64)w << 32) | (UINT64)h);
            hr = IMFTransform_SetOutputType(d->xform, out_id, out_mt, 0);
            IMFMediaType_Release(out_mt);
            if (SUCCEEDED(hr)) return S_OK;
        } else {
            IMFMediaType_Release(out_mt);
        }
    }
    return E_FAIL;
}

ss_mf_dec *ss_mf_dec_create(int max_w, int max_h) {
    if (mf_ensure_init() != 0) return NULL;
    ss_mf_dec *d = (ss_mf_dec *)calloc(1, sizeof(*d));
    if (!d) return NULL;
    d->max_w = max_w > 0 ? max_w : 3840;
    d->max_h = max_h > 0 ? max_h : 2160;
    if (FAILED(activate_hw_decoder(&d->xform)) || !d->xform) {
        ss_mf_dec_destroy(d);
        return NULL;
    }
    unlock_async(d->xform);
    IMFTransform_QueryInterface(d->xform, &IID_IMFMediaEventGenerator, (void **)&d->events);
    return d;
}

void ss_mf_dec_destroy(ss_mf_dec *d) {
    if (!d) return;
    if (d->xform && d->started) {
        IMFTransform_ProcessMessage(d->xform, MFT_MESSAGE_NOTIFY_END_OF_STREAM, 0);
        IMFTransform_ProcessMessage(d->xform, MFT_MESSAGE_NOTIFY_END_STREAMING, 0);
    }
    if (d->events) IMFMediaEventGenerator_Release(d->events);
    if (d->xform) IMFTransform_Release(d->xform);
    free(d->last_bgra);
    free(d);
}

void ss_mf_buf_free(uint8_t *p) { free(p); }

static int nv12_to_bgra(const uint8_t *nv12, int w, int h, uint8_t **out, int *stride) {
    size_t need = (size_t)w * 4 * (size_t)h;
    uint8_t *bgra = (uint8_t *)malloc(need);
    if (!bgra) return -1;
    const uint8_t *y = nv12;
    const uint8_t *uv = nv12 + (size_t)w * (size_t)h;
    for (int j = 0; j < h; j++) {
        for (int i = 0; i < w; i++) {
            int Y = y[j * w + i] - 16;
            int U = uv[(j / 2) * w + (i & ~1)] - 128;
            int V = uv[(j / 2) * w + (i & ~1) + 1] - 128;
            int C = Y;
            int R = (298 * C + 409 * V + 128) >> 8;
            int G = (298 * C - 100 * U - 208 * V + 128) >> 8;
            int B = (298 * C + 516 * U + 128) >> 8;
            if (R < 0) R = 0; if (R > 255) R = 255;
            if (G < 0) G = 0; if (G > 255) G = 255;
            if (B < 0) B = 0; if (B > 255) B = 255;
            size_t di = (size_t)j * (size_t)w * 4 + (size_t)i * 4;
            bgra[di + 0] = (uint8_t)B;
            bgra[di + 1] = (uint8_t)G;
            bgra[di + 2] = (uint8_t)R;
            bgra[di + 3] = 255;
        }
    }
    *out = bgra;
    *stride = w * 4;
    return 0;
}

int ss_mf_dec_decode(ss_mf_dec *d, const uint8_t *annex_b, int len,
                     uint8_t **out_bgra, int *out_w, int *out_h, int *out_stride) {
    if (!d || !d->xform || !annex_b || len <= 0 || !out_bgra) return -1;
    *out_bgra = NULL;

    if (!d->have_fmt) {
        int w = d->max_w < 1920 ? d->max_w : 1920;
        int h = d->max_h < 1088 ? d->max_h : 1088;
        if (w < 16) w = 320;
        if (h < 16) h = 192;
        if (FAILED(set_dec_types(d, w, h))) return -2;
        MFT_OUTPUT_STREAM_INFO osi = {0};
        IMFTransform_GetOutputStreamInfo(d->xform, (DWORD)d->output_id, &osi);
        d->provides_samples = (osi.dwFlags & (MFT_OUTPUT_STREAM_PROVIDES_SAMPLES | MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES)) != 0;
        d->out_buf_size = osi.cbSize ? osi.cbSize : (DWORD)(w * h * 4);
        IMFTransform_ProcessMessage(d->xform, MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
        IMFTransform_ProcessMessage(d->xform, MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
        d->started = 1;
        d->have_fmt = 1;
    }

    IMFMediaBuffer *buf = NULL;
    IMFSample *sample = NULL;
    if (FAILED(MFCreateMemoryBuffer((DWORD)len, &buf))) return -3;
    BYTE *p = NULL;
    if (FAILED(IMFMediaBuffer_Lock(buf, &p, NULL, NULL)) || !p) {
        IMFMediaBuffer_Release(buf);
        return -3;
    }
    memcpy(p, annex_b, (size_t)len);
    IMFMediaBuffer_Unlock(buf);
    IMFMediaBuffer_SetCurrentLength(buf, (DWORD)len);
    if (FAILED(MFCreateSample(&sample))) {
        IMFMediaBuffer_Release(buf);
        return -3;
    }
    IMFSample_AddBuffer(sample, buf);
    IMFMediaBuffer_Release(buf);

    /* Async: wait for need-input if event generator present. */
    if (d->events) {
        DWORD deadline = GetTickCount() + 500;
        while (d->need_input == 0 && GetTickCount() < deadline) {
            IMFMediaEvent *ev = NULL;
            HRESULT ehr = IMFMediaEventGenerator_GetEvent(d->events, 0, &ev);
            if (SUCCEEDED(ehr) && ev) {
                MediaEventType t = 0;
                IMFMediaEvent_GetType(ev, &t);
                if (t == METransformNeedInput) d->need_input++;
                IMFMediaEvent_Release(ev);
            } else {
                Sleep(1);
            }
        }
    }

    HRESULT hr = IMFTransform_ProcessInput(d->xform, (DWORD)d->input_id, sample, 0);
    IMFSample_Release(sample);
    if (d->need_input > 0) d->need_input--;
    if (FAILED(hr)) return -4;

    /* Drain one output. */
    for (int attempt = 0; attempt < 32; attempt++) {
        if (d->events) {
            IMFMediaEvent *ev = NULL;
            HRESULT ehr = IMFMediaEventGenerator_GetEvent(d->events, MF_EVENT_FLAG_NO_WAIT, &ev);
            if (ehr == MF_E_NO_EVENTS_AVAILABLE) {
                Sleep(1);
                continue;
            }
            if (FAILED(ehr) || !ev) break;
            MediaEventType t = 0;
            IMFMediaEvent_GetType(ev, &t);
            IMFMediaEvent_Release(ev);
            if (t == METransformNeedInput) {
                d->need_input++;
                continue;
            }
            if (t != METransformHaveOutput) continue;
        }

        MFT_OUTPUT_DATA_BUFFER odb = {0};
        odb.dwStreamID = (DWORD)d->output_id;
        IMFSample *os = NULL;
        if (!d->provides_samples) {
            IMFMediaBuffer *ob = NULL;
            MFCreateMemoryBuffer(d->out_buf_size, &ob);
            MFCreateSample(&os);
            if (os && ob) {
                IMFSample_AddBuffer(os, ob);
                IMFMediaBuffer_Release(ob);
            }
            odb.pSample = os;
        }
        DWORD status = 0;
        hr = IMFTransform_ProcessOutput(d->xform, 0, 1, &odb, &status);
        os = odb.pSample;
        if (FAILED(hr) || !os) {
            if (os) IMFSample_Release(os);
            if (!d->events) break;
            continue;
        }
        IMFMediaBuffer *ob = NULL;
        if (SUCCEEDED(IMFSample_ConvertToContiguousBuffer(os, &ob)) && ob) {
            BYTE *bp = NULL;
            DWORD cur = 0;
            if (SUCCEEDED(IMFMediaBuffer_Lock(ob, &bp, NULL, &cur)) && bp && cur > 0) {
                /* Assume NV12 plane size = w*h*3/2; derive w/h from configured max for now. */
                int w = d->max_w < 1920 ? d->max_w : 1920;
                int h = d->max_h < 1088 ? d->max_h : 1088;
                IMFMediaType *cur_mt = NULL;
                if (SUCCEEDED(IMFTransform_GetOutputCurrentType(d->xform, (DWORD)d->output_id, &cur_mt)) && cur_mt) {
                    UINT64 fs = 0;
                    if (SUCCEEDED(IMFMediaType_GetUINT64(cur_mt, MF_MT_FRAME_SIZE, &fs)) && fs) {
                        w = (int)(fs >> 32);
                        h = (int)(fs & 0xffffffffu);
                    }
                    GUID sub = {0};
                    IMFMediaType_GetGUID(cur_mt, MF_MT_SUBTYPE, &sub);
                    IMFMediaType_Release(cur_mt);
                    if (IsEqualGUID(&sub, &MFVideoFormat_ARGB32)) {
                        uint8_t *copy = (uint8_t *)malloc(cur);
                        if (copy) {
                            /* RGB32 may be BGRA already on Windows. */
                            memcpy(copy, bp, cur);
                            *out_bgra = copy;
                            if (out_w) *out_w = w;
                            if (out_h) *out_h = h;
                            if (out_stride) *out_stride = w * 4;
                            IMFMediaBuffer_Unlock(ob);
                            IMFMediaBuffer_Release(ob);
                            IMFSample_Release(os);
                            return 0;
                        }
                    }
                }
                int stride = 0;
                uint8_t *bgra = NULL;
                if (nv12_to_bgra(bp, w, h, &bgra, &stride) == 0) {
                    *out_bgra = bgra;
                    if (out_w) *out_w = w;
                    if (out_h) *out_h = h;
                    if (out_stride) *out_stride = stride;
                    IMFMediaBuffer_Unlock(ob);
                    IMFMediaBuffer_Release(ob);
                    IMFSample_Release(os);
                    return 0;
                }
                IMFMediaBuffer_Unlock(ob);
            }
            IMFMediaBuffer_Release(ob);
        }
        IMFSample_Release(os);
        if (odb.pEvents) IMFCollection_Release(odb.pEvents);
        break;
    }
    return 1;
}
