#ifndef SCREEN_STREAM_VT_ENCODE_H
#define SCREEN_STREAM_VT_ENCODE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Hardware H.264 via VideoToolbox. Returns 0 on success. */

typedef struct ss_vt_enc ss_vt_enc;
typedef struct ss_vt_dec ss_vt_dec;

typedef struct {
    uint8_t *data;
    int len;
    int keyframe;
    int64_t pts_us;
} ss_vt_au;

ss_vt_enc *ss_vt_enc_create(int width, int height, int fps, int bitrate_bps, int keyframe_every);
void ss_vt_enc_destroy(ss_vt_enc *e);
/* Feed tightly-packed or strided BGRA. On success, *out_n AUs are written into
 * caller-owned ring (copied). Returns 0, or 1 if no AU yet, or <0 on error. */
int ss_vt_enc_encode(ss_vt_enc *e, const uint8_t *bgra, int stride, int64_t pts_us,
                     ss_vt_au *out, int out_cap, int *out_n);
/* Force pending frames out. */
int ss_vt_enc_flush(ss_vt_enc *e, ss_vt_au *out, int out_cap, int *out_n);
void ss_vt_au_free(ss_vt_au *au); /* frees au->data */

ss_vt_dec *ss_vt_dec_create(int max_w, int max_h);
void ss_vt_dec_destroy(ss_vt_dec *d);
/* Decode Annex-B AU into packed BGRA (*out owned by caller via ss_vt_buf_free). */
int ss_vt_dec_decode(ss_vt_dec *d, const uint8_t *annex_b, int len,
                     uint8_t **out_bgra, int *out_w, int *out_h, int *out_stride);
void ss_vt_buf_free(uint8_t *p);

#ifdef __cplusplus
}
#endif

#endif
