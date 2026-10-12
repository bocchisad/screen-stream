#ifndef SCREEN_STREAM_H264_BS_H
#define SCREEN_STREAM_H264_BS_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    int profile_idc, level_idc, constraint_set_flags;
    int chroma_format_idc;
    int separate_colour_plane_flag;
    int bit_depth_luma_minus8, bit_depth_chroma_minus8;
    int qpprime_y_zero_transform_bypass_flag;
    int seq_scaling_matrix_present_flag;
    int log2_max_frame_num_minus4;
    int pic_order_cnt_type;
    int log2_max_pic_order_cnt_lsb_minus4;
    int delta_pic_order_always_zero_flag;
    int offset_for_non_ref_pic;
    int offset_for_top_to_bottom_field;
    int num_ref_frames_in_pic_order_cnt_cycle;
    int max_num_ref_frames;
    int gaps_in_frame_num_value_allowed_flag;
    int pic_width_in_mbs_minus1;
    int pic_height_in_map_units_minus1;
    int frame_mbs_only_flag;
    int mb_adaptive_frame_field_flag;
    int direct_8x8_inference_flag;
    int frame_cropping_flag;
    int frame_crop_left, frame_crop_right, frame_crop_top, frame_crop_bottom;
    int width, height; /* cropped display size */
    int valid;
} h264_sps;

typedef struct {
    int pic_parameter_set_id;
    int seq_parameter_set_id;
    int entropy_coding_mode_flag;
    int bottom_field_pic_order_in_frame_present_flag;
    int num_slice_groups_minus1;
    int num_ref_idx_l0_default_active_minus1;
    int num_ref_idx_l1_default_active_minus1;
    int weighted_pred_flag;
    int weighted_bipred_idc;
    int pic_init_qp_minus26;
    int pic_init_qs_minus26;
    int chroma_qp_index_offset;
    int deblocking_filter_control_present_flag;
    int constrained_intra_pred_flag;
    int redundant_pic_cnt_present_flag;
    int transform_8x8_mode_flag;
    int second_chroma_qp_index_offset;
    int valid;
} h264_pps;

typedef struct {
    int first_mb_in_slice;
    int slice_type; /* 0-9 raw */
    int pic_parameter_set_id;
    int frame_num;
    int field_pic_flag;
    int bottom_field_flag;
    int idr_pic_id;
    int pic_order_cnt_lsb;
    int delta_pic_order_cnt_bottom;
    int delta_pic_order_cnt[2];
    int direct_spatial_mv_pred_flag;
    int num_ref_idx_active_override_flag;
    int num_ref_idx_l0_active_minus1;
    int num_ref_idx_l1_active_minus1;
    int cabac_init_idc;
    int slice_qp_delta;
    int disable_deblocking_filter_idc;
    int slice_alpha_c0_offset_div2;
    int slice_beta_offset_div2;
    int nal_ref_idc;
    int nal_unit_type;
    int slice_data_bit_offset; /* bits from NAL start (incl. nal header) to slice_data */
    int valid;
} h264_slice_hdr;

/* Strip emulation prevention; *out owned by caller. Returns length or <0. */
int h264_ebsp_to_rbsp(const uint8_t *ebsp, int len, uint8_t **out);

/* Parse SPS/PPS NAL body (after nal header byte). */
int h264_parse_sps(const uint8_t *rbsp, int len, h264_sps *sps);
int h264_parse_pps(const uint8_t *rbsp, int len, h264_pps *pps);

/* Parse slice header; rbsp is NAL body after nal header. nal_hdr is first NAL byte. */
int h264_parse_slice_header(const uint8_t *rbsp, int len, uint8_t nal_hdr,
                            const h264_sps *sps, const h264_pps *pps, h264_slice_hdr *sh);

/* Walk Annex-B; on SPS/PPS update caches; on VCL fill *sh and return slice NAL offset/len.
 * Returns 0 if VCL found, 1 if only params, <0 on error. */
int h264_annexb_prepare(const uint8_t *annex, int len,
                        h264_sps *sps, h264_pps *pps, h264_slice_hdr *sh,
                        int *slice_off, int *slice_len);

/* Write Baseline SPS/PPS Annex-B for known progressive CAVLC stream (encode prepend). */
int h264_write_baseline_paramsets(int width, int height, int fps, int max_ref,
                                  uint8_t **out, int *out_len);

#ifdef __cplusplus
}
#endif

#endif
