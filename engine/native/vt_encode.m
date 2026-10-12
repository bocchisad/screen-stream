/*
 * VideoToolbox hardware H.264 encode/decode (BGRA ↔ Annex-B).
 * Requires macOS + VideoToolbox; fails closed if HW encoder unavailable.
 */
#import <Foundation/Foundation.h>
#import <VideoToolbox/VideoToolbox.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#include "vt_encode.h"
#include <stdlib.h>
#include <string.h>
#include <pthread.h>
#include <stdio.h>

struct ss_vt_enc {
    VTCompressionSessionRef session;
    int width, height, fps;
    pthread_mutex_t mu;
    /* callback-produced AUs waiting for Go pull */
    ss_vt_au pending[8];
    int pending_n;
    CMFormatDescriptionRef fmt;
    int force_key;
};

struct ss_vt_dec {
    VTDecompressionSessionRef session;
    CMVideoFormatDescriptionRef fmt;
    int max_w, max_h;
    uint8_t *ps[4];
    int ps_len[4];
    size_t ps_n;
    pthread_mutex_t mu;
    uint8_t *last_bgra;
    int last_w, last_h, last_stride;
    int got_frame;
};

static int64_t g_dec_pts = 0;

/* Contiguous copy — GetDataPointer alone is not safe when total > lengthAtOffset. */
static uint8_t *bb_copy_contiguous(CMBlockBufferRef bb, size_t *out_len) {
    if (!bb || !out_len) return NULL;
    size_t total = CMBlockBufferGetDataLength(bb);
    if (total == 0) return NULL;
    uint8_t *buf = (uint8_t *)malloc(total);
    if (!buf) return NULL;
    OSStatus st = CMBlockBufferCopyDataBytes(bb, 0, total, buf);
    if (st != noErr) {
        free(buf);
        return NULL;
    }
    *out_len = total;
    return buf;
}

static void annexb_append(uint8_t **buf, int *len, int *cap, const uint8_t *nal, int nal_len) {
    static const uint8_t start[4] = {0, 0, 0, 1};
    int need = *len + 4 + nal_len;
    if (need > *cap) {
        int nc = *cap ? *cap * 2 : 65536;
        while (nc < need) nc *= 2;
        uint8_t *nb = (uint8_t *)realloc(*buf, (size_t)nc);
        if (!nb) return;
        *buf = nb;
        *cap = nc;
    }
    memcpy(*buf + *len, start, 4);
    *len += 4;
    memcpy(*buf + *len, nal, (size_t)nal_len);
    *len += nal_len;
}

static void enc_callback(void *refcon, void *sourceFrameRefCon, OSStatus status,
                         VTEncodeInfoFlags infoFlags, CMSampleBufferRef sampleBuffer) {
    (void)sourceFrameRefCon;
    (void)infoFlags;
    ss_vt_enc *e = (ss_vt_enc *)refcon;
    if (status != noErr || !sampleBuffer) return;

    CFArrayRef attachments = CMSampleBufferGetSampleAttachmentsArray(sampleBuffer, true);
    int keyframe = 0;
    if (attachments && CFArrayGetCount(attachments) > 0) {
        CFDictionaryRef dict = (CFDictionaryRef)CFArrayGetValueAtIndex(attachments, 0);
        CFBooleanRef notSync = (CFBooleanRef)CFDictionaryGetValue(dict, kCMSampleAttachmentKey_NotSync);
        keyframe = !(notSync && CFBooleanGetValue(notSync));
    }

    uint8_t *out = NULL;
    int out_len = 0, out_cap = 0;

    CMFormatDescriptionRef fmt = CMSampleBufferGetFormatDescription(sampleBuffer);
    if (keyframe && fmt) {
        size_t psCnt = 0;
        int nalLenSize = 0;
        if (CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, 0, NULL, NULL, &psCnt, &nalLenSize) == noErr) {
            for (size_t i = 0; i < psCnt; i++) {
                const uint8_t *ps = NULL;
                size_t psSize = 0;
                if (CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, i, &ps, &psSize, NULL, NULL) == noErr && ps && psSize > 0) {
                    annexb_append(&out, &out_len, &out_cap, ps, (int)psSize);
                }
            }
        }
    }

    CMBlockBufferRef bb = CMSampleBufferGetDataBuffer(sampleBuffer);
    if (!bb) {
        free(out);
        return;
    }
    size_t total = 0;
    uint8_t *contig = bb_copy_contiguous(bb, &total);
    if (!contig) {
        free(out);
        return;
    }
    int lenSize = 4;
    if (fmt) {
        size_t dummyCnt = 0;
        int nls = 0;
        if (CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, 0, NULL, NULL, &dummyCnt, &nls) == noErr && nls > 0 && nls <= 4) {
            lenSize = nls;
        }
    }
    size_t offset = 0;
    while (offset + (size_t)lenSize <= total) {
        uint32_t nalSize = 0;
        for (int i = 0; i < lenSize; i++) {
            nalSize = (nalSize << 8) | contig[offset + (size_t)i];
        }
        offset += (size_t)lenSize;
        if (offset + nalSize > total) break;
        annexb_append(&out, &out_len, &out_cap, contig + offset, (int)nalSize);
        offset += nalSize;
    }
    free(contig);

    CMTime pts = CMSampleBufferGetPresentationTimeStamp(sampleBuffer);
    int64_t pts_us = (int64_t)(CMTimeGetSeconds(pts) * 1e6);

    pthread_mutex_lock(&e->mu);
    if (e->pending_n < 8 && out && out_len > 0) {
        e->pending[e->pending_n].data = out;
        e->pending[e->pending_n].len = out_len;
        e->pending[e->pending_n].keyframe = keyframe;
        e->pending[e->pending_n].pts_us = pts_us;
        e->pending_n++;
        out = NULL;
    }
    pthread_mutex_unlock(&e->mu);
    free(out);
}

ss_vt_enc *ss_vt_enc_create(int width, int height, int fps, int bitrate_bps, int keyframe_every) {
    if (width < 16 || height < 16) return NULL;
    if (fps <= 0) fps = 30;
    if (bitrate_bps <= 0) bitrate_bps = width * height * fps / 10;

    ss_vt_enc *e = (ss_vt_enc *)calloc(1, sizeof(*e));
    if (!e) return NULL;
    e->width = width;
    e->height = height;
    e->fps = fps;
    pthread_mutex_init(&e->mu, NULL);

    CFMutableDictionaryRef encoderSpec = CFDictionaryCreateMutable(NULL, 0,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    // Prefer hardware; do not silently fall back to software.
    CFDictionarySetValue(encoderSpec, kVTVideoEncoderSpecification_EnableHardwareAcceleratedVideoEncoder, kCFBooleanTrue);
    CFDictionarySetValue(encoderSpec, kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder, kCFBooleanTrue);

    CFMutableDictionaryRef srcAttrs = CFDictionaryCreateMutable(NULL, 0,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFDictionarySetValue(srcAttrs, kCVPixelBufferPixelFormatTypeKey,
                         (__bridge CFNumberRef)@(kCVPixelFormatType_32BGRA));
    CFDictionarySetValue(srcAttrs, kCVPixelBufferWidthKey, (__bridge CFNumberRef)@(width));
    CFDictionarySetValue(srcAttrs, kCVPixelBufferHeightKey, (__bridge CFNumberRef)@(height));
    CFDictionarySetValue(srcAttrs, kCVPixelBufferIOSurfacePropertiesKey, (__bridge CFDictionaryRef)@{});

    OSStatus st = VTCompressionSessionCreate(NULL, width, height, kCMVideoCodecType_H264,
                                             encoderSpec, srcAttrs, NULL, enc_callback, e, &e->session);
    CFRelease(encoderSpec);
    CFRelease(srcAttrs);
    if (st != noErr || !e->session) {
        ss_vt_enc_destroy(e);
        return NULL;
    }

    VTSessionSetProperty(e->session, kVTCompressionPropertyKey_RealTime, kCFBooleanTrue);
    /* Baseline is the most interoperable for WebRTC + VT decode. */
    VTSessionSetProperty(e->session, kVTCompressionPropertyKey_ProfileLevel, kVTProfileLevel_H264_Baseline_AutoLevel);
    VTSessionSetProperty(e->session, kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse);
    int64_t br = bitrate_bps;
    CFNumberRef brN = CFNumberCreate(NULL, kCFNumberSInt64Type, &br);
    VTSessionSetProperty(e->session, kVTCompressionPropertyKey_AverageBitRate, brN);
    CFRelease(brN);
    int expected = fps;
    CFNumberRef expN = CFNumberCreate(NULL, kCFNumberIntType, &expected);
    VTSessionSetProperty(e->session, kVTCompressionPropertyKey_ExpectedFrameRate, expN);
    CFRelease(expN);
    if (keyframe_every > 0) {
        int gop = keyframe_every;
        CFNumberRef gopN = CFNumberCreate(NULL, kCFNumberIntType, &gop);
        VTSessionSetProperty(e->session, kVTCompressionPropertyKey_MaxKeyFrameInterval, gopN);
        CFRelease(gopN);
    }
    VTCompressionSessionPrepareToEncodeFrames(e->session);
    e->force_key = 1; /* first AU must be IDR for decoders that reject leading P-frames */
    return e;
}

void ss_vt_enc_destroy(ss_vt_enc *e) {
    if (!e) return;
    if (e->session) {
        VTCompressionSessionCompleteFrames(e->session, kCMTimeInvalid);
        VTCompressionSessionInvalidate(e->session);
        CFRelease(e->session);
        e->session = NULL;
    }
    pthread_mutex_lock(&e->mu);
    for (int i = 0; i < e->pending_n; i++) free(e->pending[i].data);
    e->pending_n = 0;
    pthread_mutex_unlock(&e->mu);
    pthread_mutex_destroy(&e->mu);
    free(e);
}

static int drain_pending(ss_vt_enc *e, ss_vt_au *out, int out_cap, int *out_n) {
    pthread_mutex_lock(&e->mu);
    int n = e->pending_n;
    if (n > out_cap) n = out_cap;
    for (int i = 0; i < n; i++) {
        out[i] = e->pending[i];
    }
    if (n < e->pending_n) {
        memmove(&e->pending[0], &e->pending[n], (size_t)(e->pending_n - n) * sizeof(ss_vt_au));
    }
    e->pending_n -= n;
    pthread_mutex_unlock(&e->mu);
    *out_n = n;
    return n > 0 ? 0 : 1;
}

int ss_vt_enc_encode(ss_vt_enc *e, const uint8_t *bgra, int stride, int64_t pts_us,
                     ss_vt_au *out, int out_cap, int *out_n) {
    if (!e || !e->session || !bgra || !out || !out_n || out_cap < 1) return -1;
    *out_n = 0;
    CVPixelBufferRef pb = NULL;
    NSDictionary *attrs = @{
        (id)kCVPixelBufferIOSurfacePropertiesKey : @{},
    };
    CVReturn cvr = CVPixelBufferCreate(NULL, e->width, e->height, kCVPixelFormatType_32BGRA,
                                       (__bridge CFDictionaryRef)attrs, &pb);
    if (cvr != kCVReturnSuccess || !pb) return -2;
    CVPixelBufferLockBaseAddress(pb, 0);
    uint8_t *dst = (uint8_t *)CVPixelBufferGetBaseAddress(pb);
    size_t dstStride = CVPixelBufferGetBytesPerRow(pb);
    int row = e->width * 4;
    if (stride <= 0) stride = row;
    for (int y = 0; y < e->height; y++) {
        memcpy(dst + y * dstStride, bgra + y * stride, (size_t)row);
    }
    CVPixelBufferUnlockBaseAddress(pb, 0);

    CMTime pts = CMTimeMake(pts_us, 1000000);
    CMTime dur = CMTimeMake(1, e->fps > 0 ? e->fps : 30);
    CFMutableDictionaryRef frameProps = NULL;
    if (e->force_key) {
        frameProps = CFDictionaryCreateMutable(NULL, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
        CFDictionarySetValue(frameProps, kVTEncodeFrameOptionKey_ForceKeyFrame, kCFBooleanTrue);
        e->force_key = 0;
    }
    VTEncodeInfoFlags flags = 0;
    OSStatus st = VTCompressionSessionEncodeFrame(e->session, pb, pts, dur, frameProps, NULL, &flags);
    if (frameProps) CFRelease(frameProps);
    CFRelease(pb);
    if (st != noErr) return -3;
    return drain_pending(e, out, out_cap, out_n);
}

int ss_vt_enc_flush(ss_vt_enc *e, ss_vt_au *out, int out_cap, int *out_n) {
    if (!e || !e->session) return -1;
    VTCompressionSessionCompleteFrames(e->session, kCMTimeInvalid);
    return drain_pending(e, out, out_cap, out_n);
}

void ss_vt_au_free(ss_vt_au *au) {
    if (au && au->data) {
        free(au->data);
        au->data = NULL;
        au->len = 0;
    }
}

/* ---- decoder ---- */

static void dec_callback(void *decompressionOutputRefCon, void *sourceFrameRefCon, OSStatus status,
                         VTDecodeInfoFlags infoFlags, CVImageBufferRef imageBuffer,
                         CMTime presentationTimeStamp, CMTime presentationDuration) {
    (void)sourceFrameRefCon;
    (void)infoFlags;
    (void)presentationTimeStamp;
    (void)presentationDuration;
    ss_vt_dec *d = (ss_vt_dec *)decompressionOutputRefCon;
    if (status != noErr || !imageBuffer) {
        return;
    }
    CVPixelBufferRef pb = (CVPixelBufferRef)imageBuffer;
    CVPixelBufferLockBaseAddress(pb, kCVPixelBufferLock_ReadOnly);
    size_t w = CVPixelBufferGetWidth(pb);
    size_t h = CVPixelBufferGetHeight(pb);
    size_t stride = CVPixelBufferGetBytesPerRow(pb);
    uint8_t *base = (uint8_t *)CVPixelBufferGetBaseAddress(pb);
    size_t need = stride * h;
    OSType pf = CVPixelBufferGetPixelFormatType(pb);
    size_t out_stride = w * 4;
    size_t out_need = out_stride * h;
    pthread_mutex_lock(&d->mu);
    free(d->last_bgra);
    d->last_bgra = (uint8_t *)malloc(out_need);
    d->got_frame = 0;
    if (d->last_bgra && base) {
        if (pf == kCVPixelFormatType_32BGRA) {
            for (size_t y = 0; y < h; y++) {
                memcpy(d->last_bgra + y * out_stride, base + y * stride, out_stride);
            }
            d->got_frame = 1;
        } else if (pf == kCVPixelFormatType_32RGBA) {
            for (size_t y = 0; y < h; y++) {
                const uint8_t *src = base + y * stride;
                uint8_t *dst = d->last_bgra + y * out_stride;
                for (size_t x = 0; x < w; x++) {
                    dst[x * 4 + 0] = src[x * 4 + 2];
                    dst[x * 4 + 1] = src[x * 4 + 1];
                    dst[x * 4 + 2] = src[x * 4 + 0];
                    dst[x * 4 + 3] = src[x * 4 + 3];
                }
            }
            d->got_frame = 1;
        } else if (pf == kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange ||
                   pf == kCVPixelFormatType_420YpCbCr8BiPlanarFullRange) {
            /* NV12 → BGRA (BT.601 full/video range approximate). */
            uint8_t *yP = (uint8_t *)CVPixelBufferGetBaseAddressOfPlane(pb, 0);
            uint8_t *uvP = (uint8_t *)CVPixelBufferGetBaseAddressOfPlane(pb, 1);
            size_t yStride = CVPixelBufferGetBytesPerRowOfPlane(pb, 0);
            size_t uvStride = CVPixelBufferGetBytesPerRowOfPlane(pb, 1);
            for (size_t yy = 0; yy < h; yy++) {
                for (size_t x = 0; x < w; x++) {
                    int Y = yP[yy * yStride + x];
                    int U = uvP[(yy / 2) * uvStride + (x & ~1u)] - 128;
                    int V = uvP[(yy / 2) * uvStride + (x & ~1u) + 1] - 128;
                    int C = Y - 16;
                    int R = (298 * C + 409 * V + 128) >> 8;
                    int G = (298 * C - 100 * U - 208 * V + 128) >> 8;
                    int B = (298 * C + 516 * U + 128) >> 8;
                    if (R < 0) R = 0; if (R > 255) R = 255;
                    if (G < 0) G = 0; if (G > 255) G = 255;
                    if (B < 0) B = 0; if (B > 255) B = 255;
                    size_t di = yy * out_stride + x * 4;
                    d->last_bgra[di + 0] = (uint8_t)B;
                    d->last_bgra[di + 1] = (uint8_t)G;
                    d->last_bgra[di + 2] = (uint8_t)R;
                    d->last_bgra[di + 3] = 255;
                }
            }
            d->got_frame = 1;
        } else {
            fprintf(stderr, "vt_dec_cb unsupported pixfmt=%u\n", (unsigned)pf);
        }
        if (d->got_frame) {
            d->last_w = (int)w;
            d->last_h = (int)h;
            d->last_stride = (int)out_stride;
        }
    }
    pthread_mutex_unlock(&d->mu);
    CVPixelBufferUnlockBaseAddress(pb, kCVPixelBufferLock_ReadOnly);
}

static int parse_param_sets(const uint8_t *data, int len, uint8_t **ps_out, int *ps_len_out, size_t *ps_n_out, size_t ps_cap) {
    *ps_n_out = 0;
    int i = 0;
    while (i + 3 <= len && *ps_n_out < ps_cap) {
        int start = -1;
        if (i + 4 <= len && data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 0 && data[i + 3] == 1) {
            start = i + 4;
            i += 4;
        } else if (data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 1) {
            start = i + 3;
            i += 3;
        } else {
            i++;
            continue;
        }
        int j = start;
        int found_sc = 0;
        while (j + 3 <= len) {
            if (data[j] == 0 && data[j + 1] == 0 &&
                (data[j + 2] == 1 || (j + 3 < len && data[j + 2] == 0 && data[j + 3] == 1))) {
                found_sc = 1;
                break;
            }
            j++;
        }
        /* Trailing <3 bytes cannot be a start code — include them in this NAL. */
        if (!found_sc) j = len;
        int nal_len = j - start;
        if (nal_len > 0) {
            int t = data[start] & 0x1f;
            if (t == 7 || t == 8) {
                uint8_t *copy = (uint8_t *)malloc((size_t)nal_len);
                if (!copy) return -1;
                memcpy(copy, data + start, (size_t)nal_len);
                ps_out[*ps_n_out] = copy;
                ps_len_out[*ps_n_out] = nal_len;
                (*ps_n_out)++;
            }
        }
        i = j;
    }
    /* Need at least SPS+PPS */
    return (*ps_n_out >= 2) ? 0 : -1;
}

ss_vt_dec *ss_vt_dec_create(int max_w, int max_h) {
    ss_vt_dec *d = (ss_vt_dec *)calloc(1, sizeof(*d));
    if (!d) return NULL;
    d->max_w = max_w > 0 ? max_w : 3840;
    d->max_h = max_h > 0 ? max_h : 2160;
    pthread_mutex_init(&d->mu, NULL);
    return d;
}

void ss_vt_dec_destroy(ss_vt_dec *d) {
    if (!d) return;
    if (d->session) {
        VTDecompressionSessionInvalidate(d->session);
        CFRelease(d->session);
    }
    if (d->fmt) CFRelease(d->fmt);
    for (size_t i = 0; i < d->ps_n; i++) free(d->ps[i]);
    free(d->last_bgra);
    pthread_mutex_destroy(&d->mu);
    free(d);
}

static int ensure_dec_session(ss_vt_dec *d, const uint8_t *annex_b, int len) {
    uint8_t *ps[4] = {0};
    int ps_len[4] = {0};
    size_t ps_n = 0;
    if (parse_param_sets(annex_b, len, ps, ps_len, &ps_n, 4) == 0) {
        int changed = (ps_n != d->ps_n);
        for (size_t i = 0; !changed && i < ps_n; i++) {
            if (ps_len[i] != d->ps_len[i] || memcmp(ps[i], d->ps[i], (size_t)ps_len[i]) != 0) changed = 1;
        }
        for (size_t i = 0; i < d->ps_n; i++) free(d->ps[i]);
        for (size_t i = 0; i < ps_n; i++) {
            d->ps[i] = ps[i];
            d->ps_len[i] = ps_len[i];
            ps[i] = NULL;
        }
        d->ps_n = ps_n;
        if (changed && d->session) {
            VTDecompressionSessionInvalidate(d->session);
            CFRelease(d->session);
            d->session = NULL;
            if (d->fmt) {
                CFRelease(d->fmt);
                d->fmt = NULL;
            }
        }
    }
    for (size_t i = 0; i < 4; i++) free(ps[i]);
    if (d->session) return 0;

    /* Format from in-band SPS/PPS only (wire-faithful; no encoder fmt shortcuts). */
    if (d->ps_n < 2) return -1;
    const uint8_t *sets[4];
    size_t sizes[4];
    for (size_t i = 0; i < d->ps_n; i++) {
        sets[i] = d->ps[i];
        sizes[i] = (size_t)d->ps_len[i];
    }
    OSStatus st = CMVideoFormatDescriptionCreateFromH264ParameterSets(NULL, d->ps_n, sets, sizes, 4, &d->fmt);
    if (st != noErr) {
        fprintf(stderr, "vt_dec CreateFromH264ParameterSets st=%d\n", (int)st);
        return -2;
    }

    VTDecompressionOutputCallbackRecord cb = {dec_callback, d};
    st = VTDecompressionSessionCreate(NULL, d->fmt, NULL, NULL, &cb, &d->session);
    if (st != noErr) {
        fprintf(stderr, "vt_dec session create st=%d\n", (int)st);
        return -3;
    }
    return 0;
}

static int dec_pull_frame(ss_vt_dec *d, uint8_t **out_bgra, int *out_w, int *out_h, int *out_stride) {
    pthread_mutex_lock(&d->mu);
    if (!d->got_frame || !d->last_bgra) {
        pthread_mutex_unlock(&d->mu);
        return 1;
    }
    size_t need = (size_t)d->last_stride * (size_t)d->last_h;
    uint8_t *copy = (uint8_t *)malloc(need);
    if (!copy) {
        pthread_mutex_unlock(&d->mu);
        return -7;
    }
    memcpy(copy, d->last_bgra, need);
    *out_bgra = copy;
    if (out_w) *out_w = d->last_w;
    if (out_h) *out_h = d->last_h;
    if (out_stride) *out_stride = d->last_stride;
    pthread_mutex_unlock(&d->mu);
    return 0;
}

static int dec_submit(ss_vt_dec *d, CMSampleBufferRef sb, int avcc_len) {
    pthread_mutex_lock(&d->mu);
    d->got_frame = 0;
    pthread_mutex_unlock(&d->mu);

    VTDecodeInfoFlags info = 0;
    OSStatus st = VTDecompressionSessionDecodeFrame(d->session, sb, 0 /* sync */, NULL, &info);
    if (st != noErr) {
        fprintf(stderr, "vt_dec DecodeFrame st=%d info=%u avcc_len=%d\n", (int)st, (unsigned)info, avcc_len);
        return -6;
    }
    return 0;
}

int ss_vt_dec_decode(ss_vt_dec *d, const uint8_t *annex_b, int len,
                     uint8_t **out_bgra, int *out_w, int *out_h, int *out_stride) {
    if (!d || !annex_b || len <= 0 || !out_bgra) return -1;
    *out_bgra = NULL;
    int es = ensure_dec_session(d, annex_b, len);
    if (es != 0) {
        fprintf(stderr, "vt_dec ensure_dec_session=%d len=%d\n", es, len);
        return -2;
    }

    /* Convert Annex-B to AVCC length-prefixed for CMBlockBuffer. */
    uint8_t *avcc = NULL;
    int avcc_len = 0, avcc_cap = 0;
    int i = 0;
    while (i + 3 <= len) {
        int start = -1;
        if (i + 4 <= len && annex_b[i] == 0 && annex_b[i + 1] == 0 && annex_b[i + 2] == 0 && annex_b[i + 3] == 1) {
            start = i + 4;
            i += 4;
        } else if (annex_b[i] == 0 && annex_b[i + 1] == 0 && annex_b[i + 2] == 1) {
            start = i + 3;
            i += 3;
        } else {
            i++;
            continue;
        }
        int j = start;
        int found_sc = 0;
        while (j + 3 <= len) {
            if (annex_b[j] == 0 && annex_b[j + 1] == 0 &&
                (annex_b[j + 2] == 1 || (j + 3 < len && annex_b[j + 2] == 0 && annex_b[j + 3] == 1))) {
                found_sc = 1;
                break;
            }
            j++;
        }
        /* Trailing <3 bytes cannot be a start code — include them in this NAL. */
        if (!found_sc) j = len;
        int nal_len = j - start;
        if (nal_len > 0) {
            int t = annex_b[start] & 0x1f;
            /* SPS/PPS live in format desc; drop AUD; keep VCL + SEI as in encoder sample. */
            if (t == 7 || t == 8 || t == 9) {
                i = j;
                continue;
            }
            int need = avcc_len + 4 + nal_len;
            if (need > avcc_cap) {
                int nc = avcc_cap ? avcc_cap * 2 : 65536;
                while (nc < need) nc *= 2;
                uint8_t *nb = (uint8_t *)realloc(avcc, (size_t)nc);
                if (!nb) {
                    free(avcc);
                    return -3;
                }
                avcc = nb;
                avcc_cap = nc;
            }
            avcc[avcc_len] = (uint8_t)((nal_len >> 24) & 0xff);
            avcc[avcc_len + 1] = (uint8_t)((nal_len >> 16) & 0xff);
            avcc[avcc_len + 2] = (uint8_t)((nal_len >> 8) & 0xff);
            avcc[avcc_len + 3] = (uint8_t)(nal_len & 0xff);
            memcpy(avcc + avcc_len + 4, annex_b + start, (size_t)nal_len);
            avcc_len += 4 + nal_len;
        }
        i = j;
    }
    if (avcc_len == 0) {
        free(avcc);
        return 1; /* only param sets */
    }

    CMBlockBufferRef bb = NULL;
    /* kCFAllocatorNull: we free avcc after DecodeFrame (WebRTC-style). */
    OSStatus st = CMBlockBufferCreateWithMemoryBlock(
        kCFAllocatorDefault, avcc, (size_t)avcc_len, kCFAllocatorNull, NULL,
        0, (size_t)avcc_len, 0, &bb);
    if (st != noErr || !bb) {
        free(avcc);
        return -4;
    }

    CMSampleBufferRef sb = NULL;
    CMSampleTimingInfo timing;
    timing.duration = CMTimeMake(1, 30);
    timing.presentationTimeStamp = CMTimeMake(++g_dec_pts, 30);
    timing.decodeTimeStamp = kCMTimeInvalid;
    size_t sampleSize = (size_t)avcc_len;
    st = CMSampleBufferCreateReady(NULL, bb, d->fmt, 1, 1, &timing, 1, &sampleSize, &sb);
    CFRelease(bb);
    if (st != noErr || !sb) {
        fprintf(stderr, "vt_dec CMSampleBufferCreateReady st=%d\n", (int)st);
        free(avcc);
        return -5;
    }
    CFArrayRef attachments = CMSampleBufferGetSampleAttachmentsArray(sb, true);
    if (attachments && CFArrayGetCount(attachments) > 0) {
        CFMutableDictionaryRef dict = (CFMutableDictionaryRef)CFArrayGetValueAtIndex(attachments, 0);
        CFDictionarySetValue(dict, kCMSampleAttachmentKey_DisplayImmediately, kCFBooleanTrue);
        CFDictionarySetValue(dict, kCMSampleAttachmentKey_DependsOnOthers, kCFBooleanFalse);
    }

    st = dec_submit(d, sb, avcc_len);
    CFRelease(sb);
    free(avcc);
    if (st != 0) return st;

    return dec_pull_frame(d, out_bgra, out_w, out_h, out_stride);
}

void ss_vt_buf_free(uint8_t *p) { free(p); }
