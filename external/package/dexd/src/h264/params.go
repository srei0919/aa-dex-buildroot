package h264

// NAL header bytes (nal_ref_idc 3 for parameter sets and pictures).
const (
	nalAUD = 0x09
	nalSPS = 0x67
	nalPPS = 0x68
	nalIDR = 0x65
)

func mbs(n int) int { return (n + 15) / 16 }

func level(w, h int) uint64 {
	switch n := mbs(w) * mbs(h); {
	case n <= 1620:
		return 30
	case n <= 3600:
		return 31
	default:
		return 40
	}
}

// Params returns the stream parameters (SPS then PPS) for a w x h stream,
// Annex B with 4-byte start codes.
func Params(w, h int) []byte {
	var s bitWriter
	s.u(8, 66)   // profile: baseline
	s.u(8, 0xC0) // constraint_set0 and set1: constrained baseline
	s.u(8, level(w, h))
	s.ue(0)   // seq_parameter_set_id
	s.ue(0)   // log2_max_frame_num_minus4
	s.ue(2)   // pic_order_cnt_type
	s.ue(1)   // max_num_ref_frames
	s.u(1, 0) // gaps_in_frame_num_value_allowed_flag
	s.ue(uint64(mbs(w) - 1))
	s.ue(uint64(mbs(h) - 1))
	s.u(1, 1)                                        // frame_mbs_only_flag
	s.u(1, 1)                                        // direct_8x8_inference_flag
	cropR, cropB := (mbs(w)*16-w)/2, (mbs(h)*16-h)/2 // in units of 2 samples
	if cropR > 0 || cropB > 0 {
		s.u(1, 1)
		s.ue(0)
		s.ue(uint64(cropR))
		s.ue(0)
		s.ue(uint64(cropB))
	} else {
		s.u(1, 0)
	}
	s.u(1, 0) // vui_parameters_present_flag
	s.trailing()

	var p bitWriter
	p.ue(0)   // pic_parameter_set_id
	p.ue(0)   // seq_parameter_set_id
	p.u(1, 0) // entropy_coding_mode_flag: CAVLC
	p.u(1, 0) // bottom_field_pic_order_in_frame_present_flag
	p.ue(0)   // num_slice_groups_minus1
	p.ue(0)   // num_ref_idx_l0_default_active_minus1
	p.ue(0)   // num_ref_idx_l1_default_active_minus1
	p.u(1, 0) // weighted_pred_flag
	p.u(2, 0) // weighted_bipred_idc
	p.se(0)   // pic_init_qp_minus26
	p.se(0)   // pic_init_qs_minus26
	p.se(0)   // chroma_qp_index_offset
	p.u(1, 1) // deblocking_filter_control_present_flag
	p.u(1, 0) // constrained_intra_pred_flag
	p.u(1, 0) // redundant_pic_cnt_present_flag
	p.trailing()

	return append(nal(nalSPS, s.buf), nal(nalPPS, p.buf)...)
}
