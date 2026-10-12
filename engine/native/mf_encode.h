#ifndef SCREEN_STREAM_MF_ENCODE_H
#define SCREEN_STREAM_MF_ENCODE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct ss_mf_enc ss_mf_enc;
typedef struct ss_mf_dec ss_mf_dec;

typedef struct {
    uint8_t *data;
    int len;
    int keyframe;
    int64_t pts_us;
} ss_mf_au;

/* Hardware H.264 via Media Foundation (MFT_ENUM_FLAG_HARDWARE only). */
ss_mf_enc *ss_mf_enc_create(int width, int height, int fps, int bitrate_bps, int keyframe_every);
void ss_mf_enc_destroy(ss_mf_enc *e);
int ss_mf_enc_encode(ss_mf_enc *e, const uint8_t *bgra, int stride, int64_t pts_us,
                     ss_mf_au *out, int out_cap, int *out_n);
void ss_mf_au_free(ss_mf_au *au);

ss_mf_dec *ss_mf_dec_create(int max_w, int max_h);
void ss_mf_dec_destroy(ss_mf_dec *d);
int ss_mf_dec_decode(ss_mf_dec *d, const uint8_t *annex_b, int len,
                     uint8_t **out_bgra, int *out_w, int *out_h, int *out_stride);
void ss_mf_buf_free(uint8_t *p);

#ifdef __cplusplus
}
#endif

#endif
