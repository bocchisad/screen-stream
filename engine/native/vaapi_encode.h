#ifndef SCREEN_STREAM_VAAPI_ENCODE_H
#define SCREEN_STREAM_VAAPI_ENCODE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct ss_va_enc ss_va_enc;
typedef struct ss_va_dec ss_va_dec;

typedef struct {
    uint8_t *data;
    int len;
    int keyframe;
    int64_t pts_us;
} ss_va_au;

/* Hardware H.264 via libva (VAEntrypointEncSlice). No software fallback. */
ss_va_enc *ss_va_enc_create(int width, int height, int fps, int bitrate_bps, int keyframe_every);
void ss_va_enc_destroy(ss_va_enc *e);
int ss_va_enc_encode(ss_va_enc *e, const uint8_t *bgra, int stride, int64_t pts_us,
                     ss_va_au *out, int out_cap, int *out_n);
void ss_va_au_free(ss_va_au *au);

ss_va_dec *ss_va_dec_create(int max_w, int max_h);
void ss_va_dec_destroy(ss_va_dec *d);
int ss_va_dec_decode(ss_va_dec *d, const uint8_t *annex_b, int len,
                     uint8_t **out_bgra, int *out_w, int *out_h, int *out_stride);
void ss_va_buf_free(uint8_t *p);

#ifdef __cplusplus
}
#endif

#endif
