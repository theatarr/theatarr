package probe

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// bitReader reads big-endian bits; reads past the end return zeros and set over.
type bitReader struct {
	b    []byte
	pos  int // in bits
	over bool
}

func (r *bitReader) bit() uint32 {
	if r.pos >= len(r.b)*8 {
		r.over = true
		return 0
	}
	v := (r.b[r.pos/8] >> (7 - uint(r.pos%8))) & 1
	r.pos++
	return uint32(v)
}

func (r *bitReader) bits(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		v = v<<1 | r.bit()
	}
	return v
}

func (r *bitReader) skip(n int) { r.pos += n }

// ok reports whether every read so far stayed inside the data.
func (r *bitReader) ok() bool { return !r.over && r.pos <= len(r.b)*8 }

// ue is an unsigned Exp-Golomb code.
func (r *bitReader) ue() uint32 {
	zeros := 0
	for r.bit() == 0 {
		zeros++
		if zeros > 31 || r.over {
			r.over = true
			return 0
		}
	}
	if zeros == 0 {
		return 0
	}
	return (1<<uint(zeros) - 1) + r.bits(zeros)
}

// unescapeRBSP removes emulation prevention bytes (00 00 03).
func unescapeRBSP(b []byte) []byte {
	if !bytes.Contains(b, []byte{0, 0, 3}) {
		return b
	}
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// hevcConfig is what hvcC gives.
type hevcConfig struct {
	bitDepth   int
	lengthSize int
	nals       [][]byte // parameter set and SEI NAL units carried in the record
}

func parseHvcC(b []byte) (hevcConfig, bool) {
	if len(b) < 23 {
		return hevcConfig{}, false
	}
	c := hevcConfig{bitDepth: int(b[17]&7) + 8, lengthSize: int(b[21]&3) + 1}
	n := int(b[22])
	p := 23
	for i := 0; i < n && p+3 <= len(b); i++ {
		count := int(binary.BigEndian.Uint16(b[p+1:]))
		p += 3
		for j := 0; j < count && p+2 <= len(b); j++ {
			l := int(binary.BigEndian.Uint16(b[p:]))
			p += 2
			if p+l > len(b) {
				return c, true
			}
			c.nals = append(c.nals, b[p:p+l])
			p += l
		}
	}
	return c, true
}

// hevcSPSColour walks an HEVC sequence parameter set NAL unit (with its two header bytes) to the VUI's
// colour_description, ITU-T H.265 section 7.3.2.2 (seq_parameter_set_rbsp, profile_tier_level, scaling_list_data,
// st_ref_pic_set, vui_parameters), and returns colour_primaries and transfer_characteristics. ok is false when the
// SPS carries no colour description or cannot be walked to it (truncated, out of range, not the base layer).
func hevcSPSColour(nal []byte) (primaries, transfer uint64, ok bool) {
	if len(nal) < 3 || (nal[0]>>1)&0x3F != 33 || (nal[0]&1)<<5|nal[1]>>3 != 0 {
		return 0, 0, false
	}
	r := &bitReader{b: unescapeRBSP(nal[2:])}
	r.skip(4) // sps_video_parameter_set_id
	maxSubLayersMinus1 := int(r.bits(3))
	r.skip(1) // sps_temporal_id_nesting_flag
	// profile_tier_level(1, sps_max_sub_layers_minus1)
	r.skip(96) // general profile space .. general_level_idc
	var profilePresent, levelPresent [8]bool
	for i := 0; i < maxSubLayersMinus1; i++ {
		profilePresent[i] = r.bit() == 1
		levelPresent[i] = r.bit() == 1
	}
	if maxSubLayersMinus1 > 0 {
		r.skip(2 * (8 - maxSubLayersMinus1)) // reserved_zero_2bits
	}
	for i := 0; i < maxSubLayersMinus1; i++ {
		if profilePresent[i] {
			r.skip(88)
		}
		if levelPresent[i] {
			r.skip(8)
		}
	}
	r.ue() // sps_seq_parameter_set_id
	if r.ue() == 3 {
		r.skip(1) // separate_colour_plane_flag
	}
	r.ue()            // pic_width_in_luma_samples
	r.ue()            // pic_height_in_luma_samples
	if r.bit() == 1 { // conformance_window_flag
		r.ue()
		r.ue()
		r.ue()
		r.ue()
	}
	r.ue() // bit_depth_luma_minus8
	r.ue() // bit_depth_chroma_minus8
	log2MaxPocLsb := int(r.ue()) + 4
	if log2MaxPocLsb > 16 {
		return 0, 0, false
	}
	first := maxSubLayersMinus1
	if r.bit() == 1 { // sps_sub_layer_ordering_info_present_flag
		first = 0
	}
	for i := first; i <= maxSubLayersMinus1; i++ {
		r.ue() // sps_max_dec_pic_buffering_minus1
		r.ue() // sps_max_num_reorder_pics
		r.ue() // sps_max_latency_increase_plus1
	}
	for i := 0; i < 6; i++ {
		r.ue() // coding and transform block sizes, transform hierarchy depths
	}
	if r.bit() == 1 && r.bit() == 1 { // scaling_list_enabled_flag, sps_scaling_list_data_present_flag
		for sizeID := 0; sizeID < 4; sizeID++ {
			step := 1
			if sizeID == 3 {
				step = 3
			}
			for matrixID := 0; matrixID < 6; matrixID += step {
				if r.bit() == 0 { // scaling_list_pred_mode_flag
					r.ue() // scaling_list_pred_matrix_id_delta
					continue
				}
				if sizeID > 1 {
					r.ue() // scaling_list_dc_coef_minus8 (se)
				}
				for i := 0; i < min(64, 1<<(4+sizeID<<1)); i++ {
					r.ue() // scaling_list_delta_coef (se)
				}
			}
		}
	}
	r.skip(2)         // amp_enabled_flag, sample_adaptive_offset_enabled_flag
	if r.bit() == 1 { // pcm_enabled_flag
		r.skip(8) // pcm sample bit depths
		r.ue()
		r.ue()
		r.skip(1) // pcm_loop_filter_disabled_flag
	}
	numSets := int(r.ue())
	if numSets > 64 {
		return 0, 0, false
	}
	sets := make([]stRPS, numSets)
	for i := range sets {
		if !readSTRPS(r, sets, i) {
			return 0, 0, false
		}
	}
	if r.bit() == 1 { // long_term_ref_pics_present_flag
		n := int(r.ue())
		if n > 32 {
			return 0, 0, false
		}
		r.skip(n * (log2MaxPocLsb + 1)) // lt_ref_pic_poc_lsb_sps, used_by_curr_pic_lt_sps_flag
	}
	r.skip(2)         // sps_temporal_mvp_enabled_flag, strong_intra_smoothing_enabled_flag
	if r.bit() == 0 { // vui_parameters_present_flag
		return 0, 0, false
	}
	if r.bit() == 1 { // aspect_ratio_info_present_flag
		if r.bits(8) == 255 {
			r.skip(32) // sar_width, sar_height
		}
	}
	if r.bit() == 1 { // overscan_info_present_flag
		r.skip(1)
	}
	if r.bit() == 0 { // video_signal_type_present_flag
		return 0, 0, false
	}
	r.skip(4)         // video_format, video_full_range_flag
	if r.bit() == 0 { // colour_description_present_flag
		return 0, 0, false
	}
	primaries, transfer = uint64(r.bits(8)), uint64(r.bits(8))
	if !r.ok() {
		return 0, 0, false
	}
	return primaries, transfer, true
}

// stRPS is a short-term reference picture set: DeltaPocS0 and DeltaPocS1 (H.265 7.4.8).
type stRPS struct{ s0, s1 []int }

// readSTRPS reads st_ref_pic_set(idx) of the SPS into sets[idx]. With inter_ref_pic_set_prediction_flag the set is
// derived from sets[idx-1] (in an SPS, delta_idx_minus1 is absent and RefRpsIdx is idx-1), one flag pair for each of
// its NumDeltaPocs entries plus one (equations 7-61 and 7-62).
func readSTRPS(r *bitReader, sets []stRPS, idx int) bool {
	if idx > 0 && r.bit() == 1 { // inter_ref_pic_set_prediction_flag
		ref := sets[idx-1]
		sign := r.bit()
		deltaRps := int(r.ue()) + 1
		if sign == 1 {
			deltaRps = -deltaRps
		}
		n := len(ref.s0) + len(ref.s1)
		use := make([]bool, n+1)
		for j := 0; j <= n; j++ {
			used := r.bit() == 1
			use[j] = used || r.bit() == 1 // use_delta_flag, inferred 1 when used_by_curr_pic_flag is set
		}
		if r.over {
			return false
		}
		var s stRPS
		for j := len(ref.s1) - 1; j >= 0; j-- {
			s.neg(ref.s1[j]+deltaRps, use[len(ref.s0)+j])
		}
		s.neg(deltaRps, use[n])
		for j := 0; j < len(ref.s0); j++ {
			s.neg(ref.s0[j]+deltaRps, use[j])
		}
		for j := len(ref.s0) - 1; j >= 0; j-- {
			s.pos(ref.s0[j]+deltaRps, use[j])
		}
		s.pos(deltaRps, use[n])
		for j := 0; j < len(ref.s1); j++ {
			s.pos(ref.s1[j]+deltaRps, use[len(ref.s0)+j])
		}
		if len(s.s0) > 16 || len(s.s1) > 16 {
			return false
		}
		sets[idx] = s
		return true
	}
	negative, positive := int(r.ue()), int(r.ue())
	if r.over || negative > 16 || positive > 16 {
		return false
	}
	var s stRPS
	poc := 0
	for i := 0; i < negative; i++ {
		poc -= int(r.ue()) + 1 // delta_poc_s0_minus1
		r.skip(1)              // used_by_curr_pic_s0_flag
		s.s0 = append(s.s0, poc)
	}
	poc = 0
	for i := 0; i < positive; i++ {
		poc += int(r.ue()) + 1 // delta_poc_s1_minus1
		r.skip(1)              // used_by_curr_pic_s1_flag
		s.s1 = append(s.s1, poc)
	}
	sets[idx] = s
	return !r.over
}

// neg and pos add a derived entry to S0 or S1 when its flag is set and its sign fits.
func (b *stRPS) neg(d int, use bool) {
	if d < 0 && use {
		b.s0 = append(b.s0, d)
	}
}

func (b *stRPS) pos(d int, use bool) {
	if d > 0 && use {
		b.s1 = append(b.s1, d)
	}
}

// hevcColour fills the video's primaries and transfer from the SPS in hvcC's NAL units where the container gave
// none: the container's values stay first.
func hevcColour(v *Video, nals [][]byte) {
	if v.Primaries != "" && v.Transfer != "" {
		return
	}
	for _, nal := range nals {
		if p, t, ok := hevcSPSColour(nal); ok {
			if v.Primaries == "" {
				v.Primaries = mkvPrimaries[p]
			}
			if v.Transfer == "" {
				v.Transfer = mkvTransfer[t]
			}
			return
		}
	}
}

// avcConfig is what avcC (and its SPS) gives.
type avcConfig struct {
	bitDepth   int
	lengthSize int
}

func parseAvcC(b []byte) (avcConfig, bool) {
	if len(b) < 7 {
		return avcConfig{}, false
	}
	c := avcConfig{bitDepth: 8, lengthSize: int(b[4]&3) + 1}
	nsps := int(b[5] & 0x1F)
	p := 6
	if nsps > 0 && p+2 <= len(b) {
		l := int(binary.BigEndian.Uint16(b[p:]))
		if p+2+l <= len(b) && l > 0 {
			if d := spsBitDepth(b[p+2 : p+2+l]); d > 0 {
				c.bitDepth = d
			}
		}
	}
	return c, true
}

// spsBitDepth reads bit_depth_luma from an H.264 SPS NAL unit (with its header byte).
func spsBitDepth(nal []byte) int {
	if len(nal) < 4 {
		return 0
	}
	rb := unescapeRBSP(nal[1:])
	r := &bitReader{b: rb}
	profile := r.bits(8)
	r.skip(16) // constraint flags, level
	r.ue()     // seq_parameter_set_id
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		if r.ue() == 3 {
			r.skip(1)
		}
		d := int(r.ue()) + 8
		if r.over || d > 16 {
			return 0
		}
		return d
	}
	return 8
}

// av1BitDepth reads av1C.
func av1BitDepth(b []byte) int {
	if len(b) < 3 {
		return 0
	}
	high, twelve := b[2]&0x40 != 0, b[2]&0x20 != 0
	switch {
	case high && twelve:
		return 12
	case high:
		return 10
	}
	return 8
}

// doviCompat reads a DOVIDecoderConfigurationRecord: profile and dv_bl_signal_compatibility_id.
func doviCompat(b []byte) (profile, compat int, ok bool) {
	if len(b) < 5 {
		return 0, 0, false
	}
	return int(b[2] >> 1), int(b[4] >> 4), true
}

// seiFindings is what a first frame's SEI says.
type seiFindings struct {
	hdr10Plus, mastering bool
	encoder, rc          string // an x264/x265 user-data SEI: the library and its rate control (encoderSEI)
}

// scanHEVCFrame walks a length-prefixed HEVC access unit (or bare NAL units from hvcC) for prefix/suffix SEI:
// payload 137 (mastering display), 144 (content light level), 4 with the SMPTE 2094-40 ITU-T T.35 header, and 5
// (user data unregistered) with an encoder's settings.
func scanHEVCFrame(frame []byte, lengthSize int, f *seiFindings) {
	walkNALs(frame, lengthSize, func(nal []byte) { scanHEVCNAL(nal, f) })
}

// scanAVCFrame walks a length-prefixed H.264 access unit for SEI (NAL type 6): only the encoder's settings are read.
func scanAVCFrame(frame []byte, lengthSize int, f *seiFindings) {
	walkNALs(frame, lengthSize, func(nal []byte) {
		if len(nal) >= 2 && nal[0]&0x1F == 6 {
			scanSEI(unescapeRBSP(nal[1:]), f)
		}
	})
}

// walkNALs calls fn for each NAL unit of a length-prefixed access unit, up to the first truncated one.
func walkNALs(frame []byte, lengthSize int, fn func([]byte)) {
	for p := 0; p+lengthSize <= len(frame); {
		var l int
		for i := 0; i < lengthSize; i++ {
			l = l<<8 | int(frame[p+i])
		}
		p += lengthSize
		if l <= 0 || p+l > len(frame) {
			return
		}
		fn(frame[p : p+l])
		p += l
	}
}

func scanHEVCNAL(nal []byte, f *seiFindings) {
	if len(nal) < 3 {
		return
	}
	typ := (nal[0] >> 1) & 0x3F
	if typ != 39 && typ != 40 {
		return
	}
	scanSEI(unescapeRBSP(nal[2:]), f)
}

func scanSEI(b []byte, f *seiFindings) {
	p := 0
	for p < len(b) && b[p] != 0x80 {
		pt, ps := 0, 0
		for p < len(b) && b[p] == 0xFF {
			pt += 255
			p++
		}
		if p >= len(b) {
			return
		}
		pt += int(b[p])
		p++
		for p < len(b) && b[p] == 0xFF {
			ps += 255
			p++
		}
		if p >= len(b) {
			return
		}
		ps += int(b[p])
		p++
		if p+ps > len(b) {
			return
		}
		payload := b[p : p+ps]
		switch pt {
		case 137, 144:
			f.mastering = true
		case 4:
			// itu_t_t35_country_code 0xB5, provider 0x003C, oriented code 0x0001, application identifier 4.
			if len(payload) >= 6 && payload[0] == 0xB5 && payload[1] == 0x00 && payload[2] == 0x3C &&
				payload[3] == 0x00 && payload[4] == 0x01 && payload[5] == 0x04 {
				f.hdr10Plus = true
			}
		case 5:
			// uuid_iso_iec_11578 (16 bytes), then the encoder's text.
			if len(payload) > 16 && f.encoder == "" {
				f.encoder, f.rc = encoderSEI(string(bytes.TrimRight(payload[16:], "\x00")))
			}
		}
		p += ps
	}
}

// encoderSEI reads the user-data SEI x264 and x265 write into the first frame, which MediaInfo shows as "Writing
// library" and "Encoding settings": "x264 - core 164 r3095 baee400 - H.264/MPEG-4 AVC codec - ... - options: cabac=1
// ... rc=crf mbtree=1 crf=18.0 ..." or "x265 (build 199) - 3.5+1-f0c1022b5:[...] - H.265/HEVC codec - ... - options:
// ... rc=crf crf=18.0 ...". It returns the library ("x264", "x265"; "" for any other text) and the rate control:
// "crf=18.0" (or "crf") for CRF, else the options' rc value ("abr", "2pass", "cqp"), "" when the options name none.
func encoderSEI(text string) (encoder, rc string) {
	switch {
	case strings.HasPrefix(text, "x264 "):
		encoder = "x264"
	case strings.HasPrefix(text, "x265 "):
		encoder = "x265"
	default:
		return "", ""
	}
	_, opts, _ := strings.Cut(text, "options:")
	var crf string
	for _, tok := range strings.Fields(opts) {
		switch k, v, _ := strings.Cut(tok, "="); k { // crf-max= and crf-min= (x265) are other keys
		case "rc":
			rc = v
		case "crf":
			crf = v
		}
	}
	if crf != "" && (rc == "" || rc == "crf") {
		return encoder, "crf=" + crf
	}
	return encoder, rc
}

// truehdAtmos reports whether a TrueHD access unit's major sync announces a 16-channel (Atmos) presentation:
// substream_info bit 7, as the arrs' ffprobe reads it to tag the stream "thd+".
func truehdAtmos(frame []byte) (atmos, found bool) {
	i := bytes.Index(frame, []byte{0xF8, 0x72, 0x6F, 0xBA})
	if i < 0 || i+21 > len(frame) {
		return false, false
	}
	ms := frame[i:]
	// sync+type 4, format info 4, signature 2, flags 2, reserved 2, vbr/peak 2, substreams nibble byte 1, then
	// substream_info.
	if ms[8] != 0xB7 || ms[9] != 0x52 {
		return false, false
	}
	return ms[17]&0x80 != 0, true
}

// DTS sync words (libavcodec dca_syncwords.h).
var (
	dtsCore      = []byte{0x7F, 0xFE, 0x80, 0x01}
	dtsSubstream = []byte{0x64, 0x58, 0x20, 0x25}
	dtsXLL       = []byte{0x41, 0xA2, 0x95, 0x47}
	dtsXBR       = []byte{0x65, 0x5E, 0x31, 0x5E}
	dtsX96       = []byte{0x1D, 0x95, 0xF2, 0x62}
	dtsXCH       = []byte{0x5A, 0x5A, 0x5A, 0x5A}
	dtsXXCH      = []byte{0x47, 0x00, 0x4A, 0x03}
	dtsLBR       = []byte{0x0A, 0x80, 0x19, 0x21}
	dtsXLLX      = []byte{0x02, 0x00, 0x08, 0x50}
	dtsXLLXIMAX  = []byte{0xF1, 0x40, 0x00, 0xD0}
)

// dtsProfile names a DTS frame's profile as the arrs' ffprobe does.
func dtsProfile(frame []byte) string {
	hasCore := bytes.HasPrefix(frame, dtsCore) || bytes.Contains(frame[:min(len(frame), 16)], dtsCore)
	sub := bytes.Index(frame, dtsSubstream)
	if sub >= 0 {
		ext := frame[sub:]
		switch {
		case bytes.Contains(ext, dtsXLLX) || bytes.Contains(ext, dtsXLLXIMAX):
			return "DTS:X"
		case bytes.Contains(ext, dtsXLL):
			return "DTS-HD MA"
		case bytes.Contains(ext, dtsXBR) || bytes.Contains(ext, dtsXXCH) || bytes.Contains(ext, dtsX96):
			return "DTS-HD HRA"
		case bytes.Contains(ext, dtsLBR) && !hasCore:
			return "DTS Express"
		}
	}
	if hasCore {
		end := frame
		if sub > 0 {
			end = frame[:sub]
		}
		switch {
		case bytes.Contains(end[4:], dtsXCH) || bytes.Contains(end[4:], dtsXXCH):
			return "DTS-ES"
		case bytes.Contains(end[4:], dtsX96):
			return "DTS 96/24"
		}
	}
	return "DTS"
}

// eac3Atmos parses the first E-AC-3 syncframe's bit stream information up to addbsi, where
// flag_ec3_extension_type_a marks Joint Object Coding (Atmos). The bsi layout is ATSC A/52 Annex E, read as
// libavcodec's ff_eac3_parse_header reads it.
func eac3Atmos(frame []byte) (atmos, found bool) {
	i := bytes.Index(frame, []byte{0x0B, 0x77})
	if i < 0 || i+8 > len(frame) {
		return false, false
	}
	r := &bitReader{b: frame[i+2:]}
	strmtyp := r.bits(2)
	r.skip(3)  // substreamid
	r.skip(11) // frmsiz
	fscod := r.bits(2)
	numBlocks := 6
	if fscod == 3 {
		r.skip(2)
	} else {
		numBlocks = []int{1, 2, 3, 6}[r.bits(2)]
	}
	acmod := r.bits(3)
	lfeon := r.bit()
	bsid := r.bits(5)
	if bsid <= 10 || bsid > 16 {
		return false, false // plain AC-3 or not E-AC-3
	}
	chans := 1
	if acmod == 0 {
		chans = 2
	}
	for c := 0; c < chans; c++ {
		r.skip(5) // dialnorm
		if r.bit() == 1 {
			r.skip(8) // compr
		}
	}
	if strmtyp == 1 && r.bit() == 1 {
		r.skip(16) // chanmap
	}
	if r.bit() == 1 { // mixmdate
		if acmod > 2 {
			r.skip(2) // dmixmod
		}
		if acmod&1 == 1 && acmod > 2 {
			r.skip(6) // ltrtcmixlev, lorocmixlev
		}
		if acmod&4 != 0 {
			r.skip(6) // ltrtsurmixlev, lorosurmixlev
		}
		if lfeon == 1 && r.bit() == 1 {
			r.skip(5) // lfemixlevcod
		}
		if strmtyp == 0 {
			for c := 0; c < chans; c++ {
				if r.bit() == 1 {
					r.skip(6) // pgmscl
				}
			}
			if r.bit() == 1 {
				r.skip(6) // extpgmscl
			}
			switch r.bits(2) { // mixdef
			case 1:
				r.skip(5)
			case 2:
				r.skip(12)
			case 3:
				r.skip(int(r.bits(5)+2) * 8)
			}
			if acmod < 2 {
				for c := 0; c < chans; c++ {
					if r.bit() == 1 {
						r.skip(14) // panmean, paninfo
					}
				}
			}
			if r.bit() == 1 { // frmmixcfginfoe
				for b := 0; b < numBlocks; b++ {
					if numBlocks == 1 || r.bit() == 1 {
						r.skip(5)
					}
				}
			}
		}
	}
	if r.bit() == 1 { // infomdate
		r.skip(3) // bsmod
		r.skip(2) // copyrightb, origbs
		if acmod == 2 {
			r.skip(4) // dsurmod, dheadphonmod
		}
		if acmod >= 6 {
			r.skip(2) // dsurexmod
		}
		for c := 0; c < chans; c++ {
			if r.bit() == 1 {
				r.skip(8) // mixlevel, roomtyp, adconvtyp
			}
		}
		if fscod != 3 {
			r.skip(1) // sourcefscod
		}
	}
	if strmtyp == 0 && numBlocks != 6 {
		r.skip(1) // convsync
	}
	if strmtyp == 2 && (numBlocks == 6 || r.bit() == 1) {
		r.skip(6) // frmsizecod
	}
	if r.bit() == 1 { // addbsie
		addbsil := r.bits(6)
		_ = addbsil
		r.skip(7)
		ext := r.bit() == 1 // flag_ec3_extension_type_a
		if r.over {
			return false, false
		}
		return ext, true
	}
	if r.over {
		return false, false
	}
	return false, true
}
