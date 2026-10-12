#include "h264_bs.h"
#include <stdlib.h>
#include <string.h>

typedef struct {
    const uint8_t *data;
    int size;
    int bit;
} br_t;

static void br_init(br_t *b, const uint8_t *d, int n) {
    b->data = d;
    b->size = n;
    b->bit = 0;
}

static int br_bits_left(const br_t *b) {
    return b->size * 8 - b->bit;
}

static int br_u(br_t *b, int n) {
    int v = 0;
    for (int i = 0; i < n; i++) {
        if (b->bit >= b->size * 8) return 0;
        int byte = b->data[b->bit >> 3];
        int bit = 7 - (b->bit & 7);
        v = (v << 1) | ((byte >> bit) & 1);
        b->bit++;
    }
    return v;
}

static unsigned br_ue(br_t *b) {
    int z = 0;
    while (br_bits_left(b) > 0 && br_u(b, 1) == 0) z++;
    if (z > 31) return 0;
    return (unsigned)((1u << z) - 1u + (unsigned)br_u(b, z));
}

static int br_se(br_t *b) {
    unsigned c = br_ue(b);
    int v = (int)((c + 1) >> 1);
    return (c & 1) ? v : -v;
}

int h264_ebsp_to_rbsp(const uint8_t *ebsp, int len, uint8_t **out) {
    if (!ebsp || len < 0 || !out) return -1;
    uint8_t *rb = (uint8_t *)malloc((size_t)len + 1);
    if (!rb) return -1;
    int j = 0;
    for (int i = 0; i < len; i++) {
        if (i + 2 < len && ebsp[i] == 0 && ebsp[i + 1] == 0 && ebsp[i + 2] == 3) {
            rb[j++] = 0;
            rb[j++] = 0;
            i += 2; /* skip 00 00 03 */
            continue;
        }
        rb[j++] = ebsp[i];
    }
    *out = rb;
    return j;
}

static void skip_scaling_list(br_t *b, int size) {
    int last = 8, next = 8;
    for (int i = 0; i < size; i++) {
        if (next) next = (last + br_se(b) + 256) % 256;
        last = next ? next : last;
    }
}

int h264_parse_sps(const uint8_t *rbsp, int len, h264_sps *sps) {
    if (!rbsp || len < 4 || !sps) return -1;
    memset(sps, 0, sizeof(*sps));
    br_t b;
    br_init(&b, rbsp, len);
    sps->profile_idc = br_u(&b, 8);
    sps->constraint_set_flags = br_u(&b, 8);
    sps->level_idc = br_u(&b, 8);
    (void)br_ue(&b); /* sps_id */
    sps->chroma_format_idc = 1;
    if (sps->profile_idc == 100 || sps->profile_idc == 110 || sps->profile_idc == 122 ||
        sps->profile_idc == 244 || sps->profile_idc == 44 || sps->profile_idc == 83 ||
        sps->profile_idc == 86 || sps->profile_idc == 118 || sps->profile_idc == 128 ||
        sps->profile_idc == 138 || sps->profile_idc == 139 || sps->profile_idc == 134) {
        sps->chroma_format_idc = (int)br_ue(&b);
        if (sps->chroma_format_idc == 3) sps->separate_colour_plane_flag = br_u(&b, 1);
        sps->bit_depth_luma_minus8 = (int)br_ue(&b);
        sps->bit_depth_chroma_minus8 = (int)br_ue(&b);
        sps->qpprime_y_zero_transform_bypass_flag = br_u(&b, 1);
        sps->seq_scaling_matrix_present_flag = br_u(&b, 1);
        if (sps->seq_scaling_matrix_present_flag) {
            int n = (sps->chroma_format_idc != 3) ? 8 : 12;
            for (int i = 0; i < n; i++) {
                if (br_u(&b, 1)) skip_scaling_list(&b, i < 6 ? 16 : 64);
            }
        }
    }
    sps->log2_max_frame_num_minus4 = (int)br_ue(&b);
    sps->pic_order_cnt_type = (int)br_ue(&b);
    if (sps->pic_order_cnt_type == 0) {
        sps->log2_max_pic_order_cnt_lsb_minus4 = (int)br_ue(&b);
    } else if (sps->pic_order_cnt_type == 1) {
        sps->delta_pic_order_always_zero_flag = br_u(&b, 1);
        sps->offset_for_non_ref_pic = br_se(&b);
        sps->offset_for_top_to_bottom_field = br_se(&b);
        sps->num_ref_frames_in_pic_order_cnt_cycle = (int)br_ue(&b);
        for (int i = 0; i < sps->num_ref_frames_in_pic_order_cnt_cycle; i++) (void)br_se(&b);
    }
    sps->max_num_ref_frames = (int)br_ue(&b);
    sps->gaps_in_frame_num_value_allowed_flag = br_u(&b, 1);
    sps->pic_width_in_mbs_minus1 = (int)br_ue(&b);
    sps->pic_height_in_map_units_minus1 = (int)br_ue(&b);
    sps->frame_mbs_only_flag = br_u(&b, 1);
    if (!sps->frame_mbs_only_flag) sps->mb_adaptive_frame_field_flag = br_u(&b, 1);
    sps->direct_8x8_inference_flag = br_u(&b, 1);
    sps->frame_cropping_flag = br_u(&b, 1);
    if (sps->frame_cropping_flag) {
        sps->frame_crop_left = (int)br_ue(&b);
        sps->frame_crop_right = (int)br_ue(&b);
        sps->frame_crop_top = (int)br_ue(&b);
        sps->frame_crop_bottom = (int)br_ue(&b);
    }
    if (br_u(&b, 1)) {
        /* vui — skip remaining; dimensions already known */
    }
    int w = (sps->pic_width_in_mbs_minus1 + 1) * 16;
    int h = (sps->pic_height_in_map_units_minus1 + 1) * 16;
    if (!sps->frame_mbs_only_flag) h *= 2;
    if (sps->frame_cropping_flag) {
        int crop_unit_x = (sps->chroma_format_idc == 1 || sps->chroma_format_idc == 2) ? 2 : 1;
        int crop_unit_y = (sps->chroma_format_idc == 1) ? 2 : 1;
        if (!sps->frame_mbs_only_flag) crop_unit_y *= 2;
        w -= (sps->frame_crop_left + sps->frame_crop_right) * crop_unit_x;
        h -= (sps->frame_crop_top + sps->frame_crop_bottom) * crop_unit_y;
    }
    sps->width = w;
    sps->height = h;
    sps->valid = 1;
    return 0;
}

int h264_parse_pps(const uint8_t *rbsp, int len, h264_pps *pps) {
    if (!rbsp || len < 1 || !pps) return -1;
    memset(pps, 0, sizeof(*pps));
    br_t b;
    br_init(&b, rbsp, len);
    pps->pic_parameter_set_id = (int)br_ue(&b);
    pps->seq_parameter_set_id = (int)br_ue(&b);
    pps->entropy_coding_mode_flag = br_u(&b, 1);
    pps->bottom_field_pic_order_in_frame_present_flag = br_u(&b, 1);
    pps->num_slice_groups_minus1 = (int)br_ue(&b);
    if (pps->num_slice_groups_minus1 > 0) {
        /* FMO not supported for VA path */
        return -1;
    }
    pps->num_ref_idx_l0_default_active_minus1 = (int)br_ue(&b);
    pps->num_ref_idx_l1_default_active_minus1 = (int)br_ue(&b);
    pps->weighted_pred_flag = br_u(&b, 1);
    pps->weighted_bipred_idc = br_u(&b, 2);
    pps->pic_init_qp_minus26 = br_se(&b);
    pps->pic_init_qs_minus26 = br_se(&b);
    pps->chroma_qp_index_offset = br_se(&b);
    pps->deblocking_filter_control_present_flag = br_u(&b, 1);
    pps->constrained_intra_pred_flag = br_u(&b, 1);
    pps->redundant_pic_cnt_present_flag = br_u(&b, 1);
    if (br_bits_left(&b) > 0) {
        pps->transform_8x8_mode_flag = br_u(&b, 1);
        if (br_u(&b, 1)) {
            /* pic_scaling_matrix — skip */
            for (int i = 0; i < 8; i++) {
                if (br_u(&b, 1)) skip_scaling_list(&b, i < 6 ? 16 : 64);
            }
        }
        pps->second_chroma_qp_index_offset = br_se(&b);
    } else {
        pps->second_chroma_qp_index_offset = pps->chroma_qp_index_offset;
    }
    pps->valid = 1;
    return 0;
}

int h264_parse_slice_header(const uint8_t *rbsp, int len, uint8_t nal_hdr,
                            const h264_sps *sps, const h264_pps *pps, h264_slice_hdr *sh) {
    if (!rbsp || !sps || !pps || !sps->valid || !pps->valid || !sh) return -1;
    memset(sh, 0, sizeof(*sh));
    sh->nal_ref_idc = (nal_hdr >> 5) & 0x3;
    sh->nal_unit_type = nal_hdr & 0x1f;
    br_t b;
    br_init(&b, rbsp, len);
    sh->first_mb_in_slice = (int)br_ue(&b);
    sh->slice_type = (int)br_ue(&b);
    sh->pic_parameter_set_id = (int)br_ue(&b);
    if (sps->separate_colour_plane_flag) (void)br_u(&b, 2);
    int fn_bits = sps->log2_max_frame_num_minus4 + 4;
    sh->frame_num = br_u(&b, fn_bits);
    if (!sps->frame_mbs_only_flag) {
        sh->field_pic_flag = br_u(&b, 1);
        if (sh->field_pic_flag) sh->bottom_field_flag = br_u(&b, 1);
    }
    if (sh->nal_unit_type == 5) sh->idr_pic_id = (int)br_ue(&b);
    if (sps->pic_order_cnt_type == 0) {
        int poc_bits = sps->log2_max_pic_order_cnt_lsb_minus4 + 4;
        sh->pic_order_cnt_lsb = br_u(&b, poc_bits);
        if (pps->bottom_field_pic_order_in_frame_present_flag && !sh->field_pic_flag)
            sh->delta_pic_order_cnt_bottom = br_se(&b);
    } else if (sps->pic_order_cnt_type == 1 && !sps->delta_pic_order_always_zero_flag) {
        sh->delta_pic_order_cnt[0] = br_se(&b);
        if (pps->bottom_field_pic_order_in_frame_present_flag && !sh->field_pic_flag)
            sh->delta_pic_order_cnt[1] = br_se(&b);
    }
    if (pps->redundant_pic_cnt_present_flag) (void)br_ue(&b);
    int st = sh->slice_type % 5;
    if (st == 1) sh->direct_spatial_mv_pred_flag = br_u(&b, 1);
    sh->num_ref_idx_l0_active_minus1 = pps->num_ref_idx_l0_default_active_minus1;
    sh->num_ref_idx_l1_active_minus1 = pps->num_ref_idx_l1_default_active_minus1;
    if (st == 0 || st == 1 || st == 3) {
        sh->num_ref_idx_active_override_flag = br_u(&b, 1);
        if (sh->num_ref_idx_active_override_flag) {
            sh->num_ref_idx_l0_active_minus1 = (int)br_ue(&b);
            if (st == 1) sh->num_ref_idx_l1_active_minus1 = (int)br_ue(&b);
        }
    }
    /* ref_pic_list_modification — skip for Baseline I/P with no reordering */
    if (st != 2 && st != 4) {
        if (br_u(&b, 1)) {
            for (;;) {
                unsigned idc = br_ue(&b);
                if (idc == 3) break;
                if (idc == 0 || idc == 1) (void)br_ue(&b);
                else if (idc == 2) (void)br_ue(&b);
                else break;
            }
        }
    }
    if (st == 1) {
        if (br_u(&b, 1)) {
            for (;;) {
                unsigned idc = br_ue(&b);
                if (idc == 3) break;
                if (idc == 0 || idc == 1) (void)br_ue(&b);
                else if (idc == 2) (void)br_ue(&b);
                else break;
            }
        }
    }
    if ((pps->weighted_pred_flag && (st == 0 || st == 3)) ||
        (pps->weighted_bipred_idc == 1 && st == 1)) {
        /* pred_weight_table — unsupported for now */
        return -1;
    }
    if (sh->nal_ref_idc != 0) {
        if (sh->nal_unit_type == 5) {
            (void)br_u(&b, 1); /* no_output_of_prior_pics_flag */
            (void)br_u(&b, 1); /* long_term_reference_flag */
        } else {
            if (br_u(&b, 1)) { /* adaptive_ref_pic_marking_mode_flag */
                for (;;) {
                    unsigned mmco = br_ue(&b);
                    if (mmco == 0) break;
                    if (mmco == 1 || mmco == 3) (void)br_ue(&b);
                    if (mmco == 2) (void)br_ue(&b);
                    if (mmco == 3 || mmco == 6) (void)br_ue(&b);
                    if (mmco == 4) (void)br_ue(&b);
                    if (mmco == 5) { /* no more */
                    }
                }
            }
        }
    }
    if (pps->entropy_coding_mode_flag && st != 2 && st != 4)
        sh->cabac_init_idc = (int)br_ue(&b);
    sh->slice_qp_delta = br_se(&b);
    if (pps->deblocking_filter_control_present_flag) {
        sh->disable_deblocking_filter_idc = (int)br_ue(&b);
        if (sh->disable_deblocking_filter_idc != 1) {
            sh->slice_alpha_c0_offset_div2 = br_se(&b);
            sh->slice_beta_offset_div2 = br_se(&b);
        }
    }
    /* bit offset from start of NAL (= 8 header bits + rbsp bits consumed) */
    sh->slice_data_bit_offset = 8 + b.bit;
    sh->valid = 1;
    return 0;
}

static int find_start(const uint8_t *d, int len, int i, int *sc_len) {
    while (i + 3 <= len) {
        if (i + 4 <= len && d[i] == 0 && d[i + 1] == 0 && d[i + 2] == 0 && d[i + 3] == 1) {
            *sc_len = 4;
            return i;
        }
        if (d[i] == 0 && d[i + 1] == 0 && d[i + 2] == 1) {
            *sc_len = 3;
            return i;
        }
        i++;
    }
    return -1;
}

int h264_annexb_prepare(const uint8_t *annex, int len,
                        h264_sps *sps, h264_pps *pps, h264_slice_hdr *sh,
                        int *slice_off, int *slice_len) {
    if (!annex || len <= 0 || !sps || !pps || !sh || !slice_off || !slice_len) return -1;
    *slice_off = -1;
    *slice_len = 0;
    sh->valid = 0;
    int i = 0;
    while (i < len) {
        int sc = 0;
        int off = find_start(annex, len, i, &sc);
        if (off < 0) break;
        int nal_start = off + sc;
        if (nal_start >= len) break;
        int next_sc = 0;
        int next = find_start(annex, len, nal_start, &next_sc);
        int nal_end = (next < 0) ? len : next;
        /* include trailing bytes that can't be a start code */
        if (next < 0) nal_end = len;
        int nal_len = nal_end - nal_start;
        if (nal_len < 1) {
            i = nal_end;
            continue;
        }
        uint8_t nal_hdr = annex[nal_start];
        int ntype = nal_hdr & 0x1f;
        uint8_t *rbsp = NULL;
        int rlen = h264_ebsp_to_rbsp(annex + nal_start + 1, nal_len - 1, &rbsp);
        if (rlen < 0) {
            i = nal_end;
            continue;
        }
        if (ntype == 7) {
            h264_parse_sps(rbsp, rlen, sps);
        } else if (ntype == 8) {
            h264_parse_pps(rbsp, rlen, pps);
        } else if (ntype == 1 || ntype == 5) {
            if (sps->valid && pps->valid) {
                if (h264_parse_slice_header(rbsp, rlen, nal_hdr, sps, pps, sh) == 0) {
                    *slice_off = nal_start; /* NAL without start code */
                    *slice_len = nal_len;
                    free(rbsp);
                    return 0;
                }
            }
        }
        free(rbsp);
        i = nal_end;
    }
    return sps->valid && pps->valid ? 1 : -2;
}

/* ---- bit writer for Baseline param sets ---- */

typedef struct {
    uint8_t *data;
    int cap, bit;
} bw_t;

static int bw_ensure(bw_t *w, int extra_bits) {
    int need = (w->bit + extra_bits + 7) / 8;
    if (need <= w->cap) return 0;
    int nc = w->cap ? w->cap * 2 : 64;
    while (nc < need) nc *= 2;
    uint8_t *n = (uint8_t *)realloc(w->data, (size_t)nc);
    if (!n) return -1;
    memset(n + w->cap, 0, (size_t)(nc - w->cap));
    w->data = n;
    w->cap = nc;
    return 0;
}

static int bw_u(bw_t *w, unsigned v, int n) {
    if (bw_ensure(w, n) != 0) return -1;
    for (int i = n - 1; i >= 0; i--) {
        if (v & (1u << i)) w->data[w->bit >> 3] |= (uint8_t)(1u << (7 - (w->bit & 7)));
        w->bit++;
    }
    return 0;
}

static int bw_ue(bw_t *w, unsigned v) {
    unsigned x = v + 1;
    int z = 0;
    for (unsigned t = x; t > 1; t >>= 1) z++;
    if (bw_u(w, 0, z) != 0) return -1;
    return bw_u(w, x, z + 1);
}

static int bw_rbsp_trailing(bw_t *w) {
    if (bw_u(w, 1, 1) != 0) return -1;
    while (w->bit & 7) {
        if (bw_u(w, 0, 1) != 0) return -1;
    }
    return 0;
}

static int annexb_append_nal(uint8_t **buf, int *len, int *cap, const uint8_t *nal, int nlen) {
    static const uint8_t sc[4] = {0, 0, 0, 1};
    int need = *len + 4 + nlen;
    if (need > *cap) {
        int nc = *cap ? *cap * 2 : 256;
        while (nc < need) nc *= 2;
        uint8_t *n = (uint8_t *)realloc(*buf, (size_t)nc);
        if (!n) return -1;
        *buf = n;
        *cap = nc;
    }
    memcpy(*buf + *len, sc, 4);
    *len += 4;
    memcpy(*buf + *len, nal, (size_t)nlen);
    *len += nlen;
    return 0;
}

int h264_write_baseline_paramsets(int width, int height, int fps, int max_ref,
                                  uint8_t **out, int *out_len) {
    (void)fps;
    if (width < 16 || height < 16 || !out || !out_len) return -1;
    width = (width + 15) & ~15;
    height = (height + 15) & ~15;
    if (max_ref < 1) max_ref = 1;

    bw_t sps_w = {0};
    bw_u(&sps_w, 0x67, 8); /* nal: ref=3 type=7 */
    bw_u(&sps_w, 66, 8);   /* Baseline */
    bw_u(&sps_w, 0xe0, 8); /* constraint_set0|1|2 */
    bw_u(&sps_w, 31, 8);   /* level 3.1 */
    bw_ue(&sps_w, 0);      /* sps_id */
    bw_ue(&sps_w, 0);      /* log2_max_frame_num_minus4 */
    bw_ue(&sps_w, 2);      /* pic_order_cnt_type = 2 */
    bw_ue(&sps_w, (unsigned)max_ref);
    bw_u(&sps_w, 0, 1); /* gaps */
    bw_ue(&sps_w, (unsigned)(width / 16 - 1));
    bw_ue(&sps_w, (unsigned)(height / 16 - 1));
    bw_u(&sps_w, 1, 1); /* frame_mbs_only */
    bw_u(&sps_w, 1, 1); /* direct_8x8_inference */
    bw_u(&sps_w, 0, 1); /* cropping */
    bw_u(&sps_w, 0, 1); /* vui */
    bw_rbsp_trailing(&sps_w);

    bw_t pps_w = {0};
    bw_u(&pps_w, 0x68, 8); /* nal type 8 */
    bw_ue(&pps_w, 0);
    bw_ue(&pps_w, 0);
    bw_u(&pps_w, 0, 1); /* CAVLC */
    bw_u(&pps_w, 0, 1); /* bottom_field_pic_order */
    bw_ue(&pps_w, 0);   /* slice groups */
    bw_ue(&pps_w, 0);   /* ref idx l0 */
    bw_ue(&pps_w, 0);
    bw_u(&pps_w, 0, 1);
    bw_u(&pps_w, 0, 2);
    bw_ue(&pps_w, 0); /* pic_init_qp_minus26 = se(0) */
    bw_ue(&pps_w, 0); /* pic_init_qs_minus26 = se(0) */
    bw_ue(&pps_w, 0); /* chroma_qp_index_offset = se(0) */
    bw_u(&pps_w, 1, 1); /* deblocking present */
    bw_u(&pps_w, 0, 1);
    bw_u(&pps_w, 0, 1);
    bw_rbsp_trailing(&pps_w);

    uint8_t *buf = NULL;
    int len = 0, cap = 0;
    int sps_bytes = (sps_w.bit + 7) / 8;
    int pps_bytes = (pps_w.bit + 7) / 8;
    if (annexb_append_nal(&buf, &len, &cap, sps_w.data, sps_bytes) != 0 ||
        annexb_append_nal(&buf, &len, &cap, pps_w.data, pps_bytes) != 0) {
        free(sps_w.data);
        free(pps_w.data);
        free(buf);
        return -1;
    }
    free(sps_w.data);
    free(pps_w.data);
    *out = buf;
    *out_len = len;
    return 0;
}
