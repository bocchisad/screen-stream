/*
 * Linux VAAPI hardware H.264 encode/decode (libva + DRM).
 * Requires VAEntrypointEncSlice / VAEntrypointVLD — no software fallback.
 */
#include "vaapi_encode.h"
#include "h264_bs.h"
#include <va/va.h>
#include <va/va_drm.h>
#include <va/va_enc_h264.h>
#include <va/va_dec_h264.h>
#include <fcntl.h>
#include <unistd.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <stdint.h>

#ifndef VA_INVALID_SURFACE
#define VA_INVALID_SURFACE 0xffffffff
#endif
#ifndef VA_PICTURE_H264_INVALID
#define VA_PICTURE_H264_INVALID 0x00000001
#endif
#ifndef VA_PICTURE_H264_SHORT_TERM_REFERENCE
#define VA_PICTURE_H264_SHORT_TERM_REFERENCE 0x00000002
#endif
#ifndef VA_SLICE_DATA_FLAG_ALL
#define VA_SLICE_DATA_FLAG_ALL 0x00
#endif

struct ss_va_enc {
    int drm_fd;
    VADisplay dpy;
    VAConfigID config;
    VAContextID ctx;
    VAProfile profile;
    int width, height, fps;
    int bitrate;
    int gop;
    int frame_i;
    int force_key;
    VASurfaceID surf;
    VABufferID coded_buf;
    size_t coded_size;
    uint8_t *nv12;
    size_t nv12_len;
    /* bitstream state */
    int idr_pic_id;
    int frame_num;
    int poc;
};

#define VA_DEC_SURFS 4

struct ss_va_dec {
    int drm_fd;
    VADisplay dpy;
    VAConfigID config;
    VAContextID ctx;
    int max_w, max_h;
    int ready;
    int width, height;
    VASurfaceID surfs[VA_DEC_SURFS];
    int cur_surf;
    int has_ref;
    VASurfaceID ref_surf;
    int ref_frame_idx;
    int ref_poc;
    h264_sps sps;
    h264_pps pps;
    uint8_t *last_bgra;
    int last_w, last_h, last_stride;
};

static int open_drm_render(void) {
    static const char *nodes[] = {
        "/dev/dri/renderD128",
        "/dev/dri/renderD129",
        "/dev/dri/card0",
        NULL,
    };
    for (int i = 0; nodes[i]; i++) {
        int fd = open(nodes[i], O_RDWR);
        if (fd >= 0) return fd;
    }
    return -1;
}

static VADisplay va_open(int *fd_out) {
    int fd = open_drm_render();
    if (fd < 0) return NULL;
    VADisplay dpy = vaGetDisplayDRM(fd);
    if (!dpy) {
        close(fd);
        return NULL;
    }
    int maj = 0, min = 0;
    if (vaInitialize(dpy, &maj, &min) != VA_STATUS_SUCCESS) {
        vaTerminate(dpy);
        close(fd);
        return NULL;
    }
    *fd_out = fd;
    return dpy;
}

static int find_h264_encode(VADisplay dpy, VAProfile *prof_out) {
    static const VAProfile cands[] = {
        VAProfileH264ConstrainedBaseline,
        VAProfileH264Main,
        VAProfileH264High,
        VAProfileH264Baseline,
    };
    int n = vaMaxNumProfiles(dpy);
    if (n <= 0) return -1;
    VAProfile *profs = (VAProfile *)calloc((size_t)n, sizeof(VAProfile));
    if (!profs) return -1;
    int got = 0;
    if (vaQueryConfigProfiles(dpy, profs, &got) != VA_STATUS_SUCCESS) {
        free(profs);
        return -1;
    }
    for (size_t c = 0; c < sizeof(cands) / sizeof(cands[0]); c++) {
        for (int i = 0; i < got; i++) {
            if (profs[i] != cands[c]) continue;
            int ne = vaMaxNumEntrypoints(dpy);
            VAEntrypoint *eps = (VAEntrypoint *)calloc((size_t)ne, sizeof(VAEntrypoint));
            if (!eps) continue;
            int neg = 0;
            if (vaQueryConfigEntrypoints(dpy, cands[c], eps, &neg) == VA_STATUS_SUCCESS) {
                for (int e = 0; e < neg; e++) {
                    if (eps[e] == VAEntrypointEncSlice || eps[e] == VAEntrypointEncSliceLP) {
                        *prof_out = cands[c];
                        free(eps);
                        free(profs);
                        return 0;
                    }
                }
            }
            free(eps);
        }
    }
    free(profs);
    return -1;
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

static int upload_nv12(VADisplay dpy, VASurfaceID surf, const uint8_t *nv12, int w, int h) {
    VAImage img;
    if (vaDeriveImage(dpy, surf, &img) != VA_STATUS_SUCCESS) return -1;
    void *p = NULL;
    if (vaMapBuffer(dpy, img.buf, &p) != VA_STATUS_SUCCESS) {
        vaDestroyImage(dpy, img.image_id);
        return -1;
    }
    uint8_t *dst = (uint8_t *)p;
    const uint8_t *ys = nv12;
    const uint8_t *uvs = nv12 + (size_t)w * (size_t)h;
    for (int j = 0; j < h; j++) {
        memcpy(dst + img.offsets[0] + (size_t)j * img.pitches[0], ys + (size_t)j * (size_t)w, (size_t)w);
    }
    for (int j = 0; j < h / 2; j++) {
        memcpy(dst + img.offsets[1] + (size_t)j * img.pitches[1], uvs + (size_t)j * (size_t)w, (size_t)w);
    }
    vaUnmapBuffer(dpy, img.buf);
    vaDestroyImage(dpy, img.image_id);
    return 0;
}

static int annexb_is_key(const uint8_t *data, int len) {
    for (int i = 0; i + 4 < len; i++) {
        int s = -1;
        if (data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 0 && data[i + 3] == 1) s = i + 4;
        else if (data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 1) s = i + 3;
        if (s > 0 && s < len) {
            int t = data[s] & 0x1f;
            if (t == 5 || t == 7) return 1;
        }
    }
    return 0;
}

ss_va_enc *ss_va_enc_create(int width, int height, int fps, int bitrate_bps, int keyframe_every) {
    if (width < 16 || height < 16) return NULL;
    width = (width + 15) & ~15;
    height = (height + 15) & ~15;
    if (fps <= 0) fps = 30;
    if (bitrate_bps <= 0) bitrate_bps = width * height * fps / 10;
    if (keyframe_every <= 0) keyframe_every = fps * 2;

    ss_va_enc *e = (ss_va_enc *)calloc(1, sizeof(*e));
    if (!e) return NULL;
    e->width = width;
    e->height = height;
    e->fps = fps;
    e->bitrate = bitrate_bps;
    e->gop = keyframe_every;
    e->force_key = 1;
    e->nv12_len = (size_t)width * (size_t)height * 3 / 2;
    e->nv12 = (uint8_t *)malloc(e->nv12_len);
    if (!e->nv12) {
        free(e);
        return NULL;
    }

    e->dpy = va_open(&e->drm_fd);
    if (!e->dpy) {
        ss_va_enc_destroy(e);
        return NULL;
    }
    if (find_h264_encode(e->dpy, &e->profile) != 0) {
        ss_va_enc_destroy(e);
        return NULL;
    }

    VAConfigAttrib attr[2];
    attr[0].type = VAConfigAttribRTFormat;
    attr[0].value = VA_RT_FORMAT_YUV420;
    attr[1].type = VAConfigAttribRateControl;
    attr[1].value = VA_RC_CBR;
    VAEntrypoint ep = VAEntrypointEncSlice;
    /* Prefer low-power if available. */
    {
        int ne = vaMaxNumEntrypoints(e->dpy);
        VAEntrypoint *eps = (VAEntrypoint *)calloc((size_t)ne, sizeof(VAEntrypoint));
        int neg = 0;
        if (eps && vaQueryConfigEntrypoints(e->dpy, e->profile, eps, &neg) == VA_STATUS_SUCCESS) {
            for (int i = 0; i < neg; i++) {
                if (eps[i] == VAEntrypointEncSliceLP) {
                    ep = VAEntrypointEncSliceLP;
                    break;
                }
            }
        }
        free(eps);
    }
    if (vaCreateConfig(e->dpy, e->profile, ep, attr, 2, &e->config) != VA_STATUS_SUCCESS) {
        /* Retry without explicit RC if driver rejects CBR. */
        if (vaCreateConfig(e->dpy, e->profile, ep, &attr[0], 1, &e->config) != VA_STATUS_SUCCESS) {
            ss_va_enc_destroy(e);
            return NULL;
        }
    }

    if (vaCreateSurfaces(e->dpy, VA_RT_FORMAT_YUV420, (unsigned)width, (unsigned)height, &e->surf, 1, NULL, 0) != VA_STATUS_SUCCESS) {
        ss_va_enc_destroy(e);
        return NULL;
    }
    if (vaCreateContext(e->dpy, e->config, width, height, VA_PROGRESSIVE, &e->surf, 1, &e->ctx) != VA_STATUS_SUCCESS) {
        ss_va_enc_destroy(e);
        return NULL;
    }

    e->coded_size = (size_t)width * (size_t)height;
    if (e->coded_size < 65536) e->coded_size = 65536;
    if (vaCreateBuffer(e->dpy, e->ctx, VAEncCodedBufferType, (unsigned)e->coded_size, 1, NULL, &e->coded_buf) != VA_STATUS_SUCCESS) {
        ss_va_enc_destroy(e);
        return NULL;
    }
    return e;
}

void ss_va_enc_destroy(ss_va_enc *e) {
    if (!e) return;
    if (e->dpy) {
        if (e->coded_buf) vaDestroyBuffer(e->dpy, e->coded_buf);
        if (e->ctx) vaDestroyContext(e->dpy, e->ctx);
        if (e->surf) vaDestroySurfaces(e->dpy, &e->surf, 1);
        if (e->config) vaDestroyConfig(e->dpy, e->config);
        vaTerminate(e->dpy);
    }
    if (e->drm_fd >= 0) close(e->drm_fd);
    free(e->nv12);
    free(e);
}

void ss_va_au_free(ss_va_au *au) {
    if (au && au->data) {
        free(au->data);
        au->data = NULL;
        au->len = 0;
    }
}

static int encode_one(ss_va_enc *e, int keyframe, int64_t pts_us, ss_va_au *out) {
    VAEncSequenceParameterBufferH264 seq;
    VAEncPictureParameterBufferH264 pic;
    VAEncSliceParameterBufferH264 slice;
    memset(&seq, 0, sizeof(seq));
    memset(&pic, 0, sizeof(pic));
    memset(&slice, 0, sizeof(slice));

    seq.level_idc = 31;
    seq.intra_period = (unsigned)e->gop;
    seq.intra_idr_period = (unsigned)e->gop;
    seq.ip_period = 1;
    seq.bits_per_second = (unsigned)e->bitrate;
    seq.max_num_ref_frames = 1;
    seq.picture_width_in_mbs = (unsigned)((e->width + 15) / 16);
    seq.picture_height_in_mbs = (unsigned)((e->height + 15) / 16);
    seq.seq_fields.bits.frame_mbs_only_flag = 1;
    seq.seq_fields.bits.chroma_format_idc = 1;
    seq.seq_fields.bits.direct_8x8_inference_flag = 1;
    seq.seq_fields.bits.log2_max_frame_num_minus4 = 0;
    seq.seq_fields.bits.pic_order_cnt_type = 2; /* match h264_write_baseline_paramsets */

    pic.CurrPic.picture_id = e->surf;
    pic.CurrPic.TopFieldOrderCnt = e->frame_num * 2;
    pic.CurrPic.BottomFieldOrderCnt = e->frame_num * 2;
    pic.CurrPic.flags = 0;
    pic.coded_buf = e->coded_buf;
    pic.frame_num = e->frame_num;
    pic.pic_fields.bits.idr_pic_flag = keyframe ? 1 : 0;
    pic.pic_fields.bits.reference_pic_flag = 1;
    pic.pic_fields.bits.entropy_coding_mode_flag = 0; /* CAVLC baseline */
    pic.pic_fields.bits.transform_8x8_mode_flag = 0;
    pic.pic_init_qp = 26;
    pic.num_ref_idx_l0_active_minus1 = 0;
    if (keyframe) {
        pic.idr_pic_id = e->idr_pic_id++;
        e->frame_num = 0;
        pic.frame_num = 0;
        pic.CurrPic.TopFieldOrderCnt = 0;
        pic.CurrPic.BottomFieldOrderCnt = 0;
    }

    slice.slice_type = keyframe ? 2 : 0; /* I : P */
    slice.num_macroblocks = seq.picture_width_in_mbs * seq.picture_height_in_mbs;
    slice.slice_qp_delta = 0;
    slice.slice_alpha_c0_offset_div2 = 0;
    slice.slice_beta_offset_div2 = 0;

    VABufferID seq_buf = 0, pic_buf = 0, slice_buf = 0;
    if (vaCreateBuffer(e->dpy, e->ctx, VAEncSequenceParameterBufferType, sizeof(seq), 1, &seq, &seq_buf) != VA_STATUS_SUCCESS)
        return -1;
    if (vaCreateBuffer(e->dpy, e->ctx, VAEncPictureParameterBufferType, sizeof(pic), 1, &pic, &pic_buf) != VA_STATUS_SUCCESS) {
        vaDestroyBuffer(e->dpy, seq_buf);
        return -1;
    }
    if (vaCreateBuffer(e->dpy, e->ctx, VAEncSliceParameterBufferType, sizeof(slice), 1, &slice, &slice_buf) != VA_STATUS_SUCCESS) {
        vaDestroyBuffer(e->dpy, seq_buf);
        vaDestroyBuffer(e->dpy, pic_buf);
        return -1;
    }

    if (vaBeginPicture(e->dpy, e->ctx, e->surf) != VA_STATUS_SUCCESS) {
        vaDestroyBuffer(e->dpy, seq_buf);
        vaDestroyBuffer(e->dpy, pic_buf);
        vaDestroyBuffer(e->dpy, slice_buf);
        return -1;
    }
    VABufferID bufs[3] = {seq_buf, pic_buf, slice_buf};
    if (vaRenderPicture(e->dpy, e->ctx, bufs, 3) != VA_STATUS_SUCCESS) {
        vaEndPicture(e->dpy, e->ctx);
        vaDestroyBuffer(e->dpy, seq_buf);
        vaDestroyBuffer(e->dpy, pic_buf);
        vaDestroyBuffer(e->dpy, slice_buf);
        return -1;
    }
    if (vaEndPicture(e->dpy, e->ctx) != VA_STATUS_SUCCESS) {
        vaDestroyBuffer(e->dpy, seq_buf);
        vaDestroyBuffer(e->dpy, pic_buf);
        vaDestroyBuffer(e->dpy, slice_buf);
        return -1;
    }
    vaDestroyBuffer(e->dpy, seq_buf);
    vaDestroyBuffer(e->dpy, pic_buf);
    vaDestroyBuffer(e->dpy, slice_buf);

    if (vaSyncSurface(e->dpy, e->surf) != VA_STATUS_SUCCESS) return -1;

    VACodedBufferSegment *seg = NULL;
    if (vaMapBuffer(e->dpy, e->coded_buf, (void **)&seg) != VA_STATUS_SUCCESS || !seg) return -1;
    size_t total = 0;
    for (VACodedBufferSegment *s = seg; s; s = (VACodedBufferSegment *)s->next) total += s->size;
    uint8_t *raw = (uint8_t *)malloc(total ? total : 1);
    if (!raw) {
        vaUnmapBuffer(e->dpy, e->coded_buf);
        return -1;
    }
    size_t off = 0;
    for (VACodedBufferSegment *s = seg; s; s = (VACodedBufferSegment *)s->next) {
        if (s->buf && s->size) {
            memcpy(raw + off, s->buf, s->size);
            off += s->size;
        }
    }
    vaUnmapBuffer(e->dpy, e->coded_buf);

    /* Guarantees in-band SPS/PPS on IDR when the driver omits them. */
    uint8_t *data = raw;
    size_t dlen = off;
    if (keyframe && !annexb_is_key(raw, (int)off)) {
        /* still may lack type-7; check explicitly */
    }
    int has_sps = 0;
    for (size_t i = 0; i + 4 < off; i++) {
        int s = -1;
        if (raw[i] == 0 && raw[i + 1] == 0 && raw[i + 2] == 0 && raw[i + 3] == 1) s = (int)i + 4;
        else if (raw[i] == 0 && raw[i + 1] == 0 && raw[i + 2] == 1) s = (int)i + 3;
        if (s > 0 && s < (int)off && (raw[s] & 0x1f) == 7) {
            has_sps = 1;
            break;
        }
    }
    if (keyframe && !has_sps) {
        uint8_t *ps = NULL;
        int ps_len = 0;
        if (h264_write_baseline_paramsets(e->width, e->height, e->fps, 1, &ps, &ps_len) == 0 && ps) {
            data = (uint8_t *)malloc((size_t)ps_len + off);
            if (data) {
                memcpy(data, ps, (size_t)ps_len);
                memcpy(data + ps_len, raw, off);
                dlen = (size_t)ps_len + off;
                free(raw);
            } else {
                data = raw;
            }
            free(ps);
        }
    }

    out->data = data;
    out->len = (int)dlen;
    out->keyframe = keyframe || annexb_is_key(data, (int)dlen);
    out->pts_us = pts_us;

    e->frame_num = (e->frame_num + 1) & 15; /* log2_max_frame_num_minus4=0 → 4 bits */
    return dlen > 0 ? 0 : -1;
}

int ss_va_enc_encode(ss_va_enc *e, const uint8_t *bgra, int stride, int64_t pts_us,
                     ss_va_au *out, int out_cap, int *out_n) {
    if (!e || !e->dpy || !bgra || !out || !out_n || out_cap < 1) return -1;
    *out_n = 0;
    if (stride <= 0) stride = e->width * 4;
    bgra_to_nv12(bgra, stride, e->width, e->height, e->nv12);
    if (upload_nv12(e->dpy, e->surf, e->nv12, e->width, e->height) != 0) return -2;

    int key = e->force_key || (e->gop > 0 && (e->frame_i % e->gop) == 0);
    e->force_key = 0;
    if (encode_one(e, key, pts_us, &out[0]) != 0) return -3;
    *out_n = 1;
    e->frame_i++;
    return 0;
}

/* ---- decoder (VAEntrypointVLD) ---- */

static int find_h264_decode(VADisplay dpy, VAProfile *prof_out) {
    static const VAProfile cands[] = {
        VAProfileH264ConstrainedBaseline,
        VAProfileH264Main,
        VAProfileH264High,
        VAProfileH264Baseline,
    };
    int n = vaMaxNumProfiles(dpy);
    VAProfile *profs = (VAProfile *)calloc((size_t)n, sizeof(VAProfile));
    int got = 0;
    if (!profs || vaQueryConfigProfiles(dpy, profs, &got) != VA_STATUS_SUCCESS) {
        free(profs);
        return -1;
    }
    for (size_t c = 0; c < sizeof(cands) / sizeof(cands[0]); c++) {
        for (int i = 0; i < got; i++) {
            if (profs[i] != cands[c]) continue;
            int ne = vaMaxNumEntrypoints(dpy);
            VAEntrypoint *eps = (VAEntrypoint *)calloc((size_t)ne, sizeof(VAEntrypoint));
            int neg = 0;
            if (eps && vaQueryConfigEntrypoints(dpy, cands[c], eps, &neg) == VA_STATUS_SUCCESS) {
                for (int e = 0; e < neg; e++) {
                    if (eps[e] == VAEntrypointVLD) {
                        *prof_out = cands[c];
                        free(eps);
                        free(profs);
                        return 0;
                    }
                }
            }
            free(eps);
        }
    }
    free(profs);
    return -1;
}

ss_va_dec *ss_va_dec_create(int max_w, int max_h) {
    ss_va_dec *d = (ss_va_dec *)calloc(1, sizeof(*d));
    if (!d) return NULL;
    d->max_w = max_w > 0 ? max_w : 3840;
    d->max_h = max_h > 0 ? max_h : 2160;
    d->drm_fd = -1;
    d->dpy = va_open(&d->drm_fd);
    if (!d->dpy) {
        ss_va_dec_destroy(d);
        return NULL;
    }
    VAProfile prof;
    if (find_h264_decode(d->dpy, &prof) != 0) {
        ss_va_dec_destroy(d);
        return NULL;
    }
    VAConfigAttrib attr;
    attr.type = VAConfigAttribRTFormat;
    attr.value = VA_RT_FORMAT_YUV420;
    if (vaCreateConfig(d->dpy, prof, VAEntrypointVLD, &attr, 1, &d->config) != VA_STATUS_SUCCESS) {
        ss_va_dec_destroy(d);
        return NULL;
    }
    for (int i = 0; i < VA_DEC_SURFS; i++) d->surfs[i] = VA_INVALID_SURFACE;
    return d;
}

void ss_va_dec_destroy(ss_va_dec *d) {
    if (!d) return;
    if (d->dpy) {
        if (d->ctx) vaDestroyContext(d->dpy, d->ctx);
        int n = 0;
        VASurfaceID live[VA_DEC_SURFS];
        for (int i = 0; i < VA_DEC_SURFS; i++) {
            if (d->surfs[i] != 0 && d->surfs[i] != VA_INVALID_SURFACE) live[n++] = d->surfs[i];
        }
        if (n > 0) vaDestroySurfaces(d->dpy, live, n);
        if (d->config) vaDestroyConfig(d->dpy, d->config);
        vaTerminate(d->dpy);
    }
    if (d->drm_fd >= 0) close(d->drm_fd);
    free(d->last_bgra);
    free(d);
}

void ss_va_buf_free(uint8_t *p) { free(p); }

static void va_pic_invalid(VAPictureH264 *p) {
    memset(p, 0, sizeof(*p));
    p->picture_id = VA_INVALID_SURFACE;
    p->flags = VA_PICTURE_H264_INVALID;
}

static int dec_ensure_surfaces(ss_va_dec *d, int w, int h) {
    if (d->ready && d->width == w && d->height == h) return 0;
    if (d->ctx) {
        vaDestroyContext(d->dpy, d->ctx);
        d->ctx = 0;
    }
    int n = 0;
    VASurfaceID live[VA_DEC_SURFS];
    for (int i = 0; i < VA_DEC_SURFS; i++) {
        if (d->surfs[i] != 0 && d->surfs[i] != VA_INVALID_SURFACE) live[n++] = d->surfs[i];
        d->surfs[i] = VA_INVALID_SURFACE;
    }
    if (n > 0) vaDestroySurfaces(d->dpy, live, n);
    if (vaCreateSurfaces(d->dpy, VA_RT_FORMAT_YUV420, (unsigned)w, (unsigned)h, d->surfs, VA_DEC_SURFS, NULL, 0) != VA_STATUS_SUCCESS)
        return -1;
    if (vaCreateContext(d->dpy, d->config, w, h, VA_PROGRESSIVE, d->surfs, VA_DEC_SURFS, &d->ctx) != VA_STATUS_SUCCESS)
        return -1;
    d->width = w;
    d->height = h;
    d->ready = 1;
    d->has_ref = 0;
    d->cur_surf = 0;
    return 0;
}

static int download_nv12_bgra(ss_va_dec *d, VASurfaceID surf, uint8_t **out_bgra, int *out_w, int *out_h, int *out_stride) {
    if (vaSyncSurface(d->dpy, surf) != VA_STATUS_SUCCESS) return -1;
    VAImage img;
    memset(&img, 0, sizeof(img));
    if (vaDeriveImage(d->dpy, surf, &img) != VA_STATUS_SUCCESS) return -1;
    void *p = NULL;
    if (vaMapBuffer(d->dpy, img.buf, &p) != VA_STATUS_SUCCESS) {
        vaDestroyImage(d->dpy, img.image_id);
        return -1;
    }
    int w = d->width, h = d->height;
    size_t stride = (size_t)w * 4;
    uint8_t *bgra = (uint8_t *)malloc(stride * (size_t)h);
    if (!bgra) {
        vaUnmapBuffer(d->dpy, img.buf);
        vaDestroyImage(d->dpy, img.image_id);
        return -1;
    }
    const uint8_t *base = (const uint8_t *)p;
    const uint8_t *yP = base + img.offsets[0];
    const uint8_t *uvP = base + img.offsets[1];
    size_t yStride = img.pitches[0];
    size_t uvStride = img.pitches[1];
    for (int j = 0; j < h; j++) {
        for (int i = 0; i < w; i++) {
            int Y = yP[j * (int)yStride + i] - 16;
            int U = uvP[(j / 2) * (int)uvStride + (i & ~1)] - 128;
            int V = uvP[(j / 2) * (int)uvStride + (i & ~1) + 1] - 128;
            int C = Y;
            int R = (298 * C + 409 * V + 128) >> 8;
            int G = (298 * C - 100 * U - 208 * V + 128) >> 8;
            int B = (298 * C + 516 * U + 128) >> 8;
            if (R < 0) R = 0; if (R > 255) R = 255;
            if (G < 0) G = 0; if (G > 255) G = 255;
            if (B < 0) B = 0; if (B > 255) B = 255;
            size_t di = (size_t)j * stride + (size_t)i * 4;
            bgra[di + 0] = (uint8_t)B;
            bgra[di + 1] = (uint8_t)G;
            bgra[di + 2] = (uint8_t)R;
            bgra[di + 3] = 255;
        }
    }
    vaUnmapBuffer(d->dpy, img.buf);
    vaDestroyImage(d->dpy, img.image_id);
    *out_bgra = bgra;
    if (out_w) *out_w = w;
    if (out_h) *out_h = h;
    if (out_stride) *out_stride = (int)stride;
    return 0;
}

int ss_va_dec_decode(ss_va_dec *d, const uint8_t *annex_b, int len,
                     uint8_t **out_bgra, int *out_w, int *out_h, int *out_stride) {
    if (!d || !d->dpy || !annex_b || len <= 0 || !out_bgra) return -1;
    *out_bgra = NULL;

    h264_slice_hdr sh;
    memset(&sh, 0, sizeof(sh));
    int slice_off = 0, slice_len = 0;
    int pr = h264_annexb_prepare(annex_b, len, &d->sps, &d->pps, &sh, &slice_off, &slice_len);
    if (pr < 0) return -2;
    if (pr > 0 || !sh.valid) return 1; /* params only / no VCL yet */
    if (d->sps.width <= 0 || d->sps.height <= 0) return -2;
    if (d->sps.width > d->max_w || d->sps.height > d->max_h) return -2;

    if (dec_ensure_surfaces(d, d->sps.width, d->sps.height) != 0) return -3;

    VASurfaceID target = d->surfs[d->cur_surf];
    d->cur_surf = (d->cur_surf + 1) % VA_DEC_SURFS;

    VAPictureParameterBufferH264 pic;
    memset(&pic, 0, sizeof(pic));
    pic.CurrPic.picture_id = target;
    pic.CurrPic.frame_idx = sh.frame_num;
    pic.CurrPic.flags = 0;
    pic.CurrPic.TopFieldOrderCnt = sh.pic_order_cnt_lsb;
    pic.CurrPic.BottomFieldOrderCnt = sh.pic_order_cnt_lsb;
    for (int i = 0; i < 16; i++) va_pic_invalid(&pic.ReferenceFrames[i]);
    if (d->has_ref && (sh.nal_unit_type != 5)) {
        pic.ReferenceFrames[0].picture_id = d->ref_surf;
        pic.ReferenceFrames[0].frame_idx = d->ref_frame_idx;
        pic.ReferenceFrames[0].flags = VA_PICTURE_H264_SHORT_TERM_REFERENCE;
        pic.ReferenceFrames[0].TopFieldOrderCnt = d->ref_poc;
        pic.ReferenceFrames[0].BottomFieldOrderCnt = d->ref_poc;
    }
    pic.picture_width_in_mbs_minus1 = (uint16_t)d->sps.pic_width_in_mbs_minus1;
    pic.picture_height_in_mbs_minus1 = (uint16_t)d->sps.pic_height_in_map_units_minus1;
    pic.bit_depth_luma_minus8 = (uint8_t)d->sps.bit_depth_luma_minus8;
    pic.bit_depth_chroma_minus8 = (uint8_t)d->sps.bit_depth_chroma_minus8;
    pic.num_ref_frames = (uint8_t)d->sps.max_num_ref_frames;
    pic.seq_fields.bits.chroma_format_idc = d->sps.chroma_format_idc;
    pic.seq_fields.bits.residual_colour_transform_flag = d->sps.separate_colour_plane_flag;
    pic.seq_fields.bits.gaps_in_frame_num_value_allowed_flag = d->sps.gaps_in_frame_num_value_allowed_flag;
    pic.seq_fields.bits.frame_mbs_only_flag = d->sps.frame_mbs_only_flag;
    pic.seq_fields.bits.mb_adaptive_frame_field_flag = d->sps.mb_adaptive_frame_field_flag;
    pic.seq_fields.bits.direct_8x8_inference_flag = d->sps.direct_8x8_inference_flag;
    pic.seq_fields.bits.MinLumaBiPredSize8x8 = (d->sps.level_idc >= 31) ? 1 : 0;
    pic.seq_fields.bits.log2_max_frame_num_minus4 = d->sps.log2_max_frame_num_minus4;
    pic.seq_fields.bits.pic_order_cnt_type = d->sps.pic_order_cnt_type;
    pic.seq_fields.bits.log2_max_pic_order_cnt_lsb_minus4 = d->sps.log2_max_pic_order_cnt_lsb_minus4;
    pic.seq_fields.bits.delta_pic_order_always_zero_flag = d->sps.delta_pic_order_always_zero_flag;
    pic.pic_init_qp_minus26 = (int8_t)d->pps.pic_init_qp_minus26;
    pic.pic_init_qs_minus26 = (int8_t)d->pps.pic_init_qs_minus26;
    pic.chroma_qp_index_offset = (int8_t)d->pps.chroma_qp_index_offset;
    pic.second_chroma_qp_index_offset = (int8_t)d->pps.second_chroma_qp_index_offset;
    pic.pic_fields.bits.entropy_coding_mode_flag = d->pps.entropy_coding_mode_flag;
    pic.pic_fields.bits.weighted_pred_flag = d->pps.weighted_pred_flag;
    pic.pic_fields.bits.weighted_bipred_idc = d->pps.weighted_bipred_idc;
    pic.pic_fields.bits.transform_8x8_mode_flag = d->pps.transform_8x8_mode_flag;
    pic.pic_fields.bits.field_pic_flag = sh.field_pic_flag;
    pic.pic_fields.bits.constrained_intra_pred_flag = d->pps.constrained_intra_pred_flag;
    pic.pic_fields.bits.pic_order_present_flag = d->pps.bottom_field_pic_order_in_frame_present_flag;
    pic.pic_fields.bits.deblocking_filter_control_present_flag = d->pps.deblocking_filter_control_present_flag;
    pic.pic_fields.bits.redundant_pic_cnt_present_flag = d->pps.redundant_pic_cnt_present_flag;
    pic.pic_fields.bits.reference_pic_flag = sh.nal_ref_idc != 0;
    pic.frame_num = (uint16_t)sh.frame_num;

    VAIQMatrixBufferH264 iq;
    memset(&iq, 0, sizeof(iq));
    for (int i = 0; i < 16; i++) {
        iq.ScalingList4x4[0][i] = 16;
        iq.ScalingList4x4[1][i] = 16;
        iq.ScalingList4x4[2][i] = 16;
        iq.ScalingList4x4[3][i] = 16;
        iq.ScalingList4x4[4][i] = 16;
        iq.ScalingList4x4[5][i] = 16;
    }
    for (int i = 0; i < 64; i++) {
        iq.ScalingList8x8[0][i] = 16;
        iq.ScalingList8x8[1][i] = 16;
    }

    VASliceParameterBufferH264 sl;
    memset(&sl, 0, sizeof(sl));
    sl.slice_data_size = (uint32_t)slice_len;
    sl.slice_data_offset = 0;
    sl.slice_data_flag = VA_SLICE_DATA_FLAG_ALL;
    sl.slice_data_bit_offset = (uint16_t)sh.slice_data_bit_offset;
    sl.first_mb_in_slice = (uint16_t)sh.first_mb_in_slice;
    sl.slice_type = (uint8_t)(sh.slice_type % 5);
    sl.direct_spatial_mv_pred_flag = (uint8_t)sh.direct_spatial_mv_pred_flag;
    sl.num_ref_idx_l0_active_minus1 = (uint8_t)sh.num_ref_idx_l0_active_minus1;
    sl.num_ref_idx_l1_active_minus1 = (uint8_t)sh.num_ref_idx_l1_active_minus1;
    sl.cabac_init_idc = (uint8_t)sh.cabac_init_idc;
    sl.slice_qp_delta = (int8_t)sh.slice_qp_delta;
    sl.disable_deblocking_filter_idc = (uint8_t)sh.disable_deblocking_filter_idc;
    sl.slice_alpha_c0_offset_div2 = (int8_t)sh.slice_alpha_c0_offset_div2;
    sl.slice_beta_offset_div2 = (int8_t)sh.slice_beta_offset_div2;
    for (int i = 0; i < 32; i++) {
        va_pic_invalid(&sl.RefPicList0[i]);
        va_pic_invalid(&sl.RefPicList1[i]);
    }
    if (d->has_ref && sl.slice_type != 2 && sl.slice_type != 4) {
        sl.RefPicList0[0] = pic.ReferenceFrames[0];
    }

    VABufferID pic_buf = 0, iq_buf = 0, sl_buf = 0, data_buf = 0;
    if (vaCreateBuffer(d->dpy, d->ctx, VAPictureParameterBufferType, sizeof(pic), 1, &pic, &pic_buf) != VA_STATUS_SUCCESS)
        return -4;
    if (vaCreateBuffer(d->dpy, d->ctx, VAIQMatrixBufferType, sizeof(iq), 1, &iq, &iq_buf) != VA_STATUS_SUCCESS) {
        vaDestroyBuffer(d->dpy, pic_buf);
        return -4;
    }
    if (vaCreateBuffer(d->dpy, d->ctx, VASliceParameterBufferType, sizeof(sl), 1, &sl, &sl_buf) != VA_STATUS_SUCCESS) {
        vaDestroyBuffer(d->dpy, pic_buf);
        vaDestroyBuffer(d->dpy, iq_buf);
        return -4;
    }
    if (vaCreateBuffer(d->dpy, d->ctx, VASliceDataBufferType, (unsigned)slice_len, 1, (void *)(annex_b + slice_off), &data_buf) != VA_STATUS_SUCCESS) {
        vaDestroyBuffer(d->dpy, pic_buf);
        vaDestroyBuffer(d->dpy, iq_buf);
        vaDestroyBuffer(d->dpy, sl_buf);
        return -4;
    }

    if (vaBeginPicture(d->dpy, d->ctx, target) != VA_STATUS_SUCCESS) {
        vaDestroyBuffer(d->dpy, pic_buf);
        vaDestroyBuffer(d->dpy, iq_buf);
        vaDestroyBuffer(d->dpy, sl_buf);
        vaDestroyBuffer(d->dpy, data_buf);
        return -5;
    }
    VABufferID bufs[4] = {pic_buf, iq_buf, sl_buf, data_buf};
    VAStatus st = vaRenderPicture(d->dpy, d->ctx, bufs, 4);
    VAStatus st2 = vaEndPicture(d->dpy, d->ctx);
    vaDestroyBuffer(d->dpy, pic_buf);
    vaDestroyBuffer(d->dpy, iq_buf);
    vaDestroyBuffer(d->dpy, sl_buf);
    vaDestroyBuffer(d->dpy, data_buf);
    if (st != VA_STATUS_SUCCESS || st2 != VA_STATUS_SUCCESS) return -5;

    if (download_nv12_bgra(d, target, out_bgra, out_w, out_h, out_stride) != 0) return 1;

    if (sh.nal_ref_idc != 0) {
        d->has_ref = 1;
        d->ref_surf = target;
        d->ref_frame_idx = sh.frame_num;
        d->ref_poc = sh.pic_order_cnt_lsb;
    }
    return 0;
}
