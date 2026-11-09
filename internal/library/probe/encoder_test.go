package probe

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// Encoder SEI texts as x264 and x265 write them (MediaInfo's "Encoding settings"), trimmed to the options that matter.
const (
	x265CRF = "x265 (build 199) - 3.5+1-f0c1022b5:[Linux][GCC 9.3.0][64 bit] 10bit - H.265/HEVC codec - Copyright " +
		"2013-2018 (c) Multicoreware, Inc - http://x265.org - options: cpuid=1111039 frame-threads=4 wpp ctu=64 " +
		"min-cu-size=8 max-tu-size=32 bframes=8 b-adapt=2 rc-lookahead=60 rc=crf crf=18.0 qcomp=0.60 qpstep=4 " +
		"stats-write=0 stats-read=0 vbv-maxrate=0 vbv-bufsize=0 crf-max=0.0 crf-min=0.0 ipratio=1.40 pbratio=1.30"
	x265ABR = "x265 (build 199) - 3.5+1-f0c1022b5:[Linux][GCC 9.3.0][64 bit] 10bit - H.265/HEVC codec - Copyright " +
		"2013-2018 (c) Multicoreware, Inc - http://x265.org - options: cpuid=1111039 bframes=4 rc=abr bitrate=50000 " +
		"qcomp=0.60 qpstep=4 stats-write=0 stats-read=2 vbv-maxrate=60000 vbv-bufsize=60000 crf-max=0.0 crf-min=0.0"
	x264Bluray = "x264 - core 164 r3095 baee400 - H.264/MPEG-4 AVC codec - Copyleft 2003-2022 - " +
		"http://www.videolan.org/x264.html - options: cabac=1 ref=4 deblock=1:-1:-1 analyse=0x3:0x133 me=umh " +
		"subme=10 bluray_compat=1 b_pyramid=1 open_gop=1 weightp=1 keyint=24 keyint_min=1 rc=2pass mbtree=1 " +
		"bitrate=30000 ratetol=1.0 qcomp=0.60 qpmin=0 qpmax=69 qpstep=4 cplxblur=20.0 qblur=0.5 vbv_maxrate=40000 " +
		"vbv_bufsize=30000 nal_hrd=vbr filler=0 ip_ratio=1.40 aq=1:0.80"
	x264CRF = "x264 - core 164 r3095 baee400 - H.264/MPEG-4 AVC codec - Copyleft 2003-2022 - " +
		"http://www.videolan.org/x264.html - options: cabac=1 ref=5 subme=9 keyint=250 rc_lookahead=60 rc=crf " +
		"mbtree=1 crf=20.0 qcomp=0.60 qpmin=0 qpmax=69 qpstep=4 ip_ratio=1.40 aq=1:1.00"
)

func TestEncoderSEI(t *testing.T) {
	for _, tc := range []struct{ text, enc, rc string }{
		{x265CRF, "x265", "crf=18.0"},
		{x265ABR, "x265", "abr"}, // crf-max= and crf-min= are not CRF
		{x264Bluray, "x264", "2pass"},
		{x264CRF, "x264", "crf=20.0"},
		{"x264 - core 164 - options: rc=crf mbtree=1", "x264", "crf"},
		{"Lavc60.3.100 libx264", "", ""},
	} {
		if enc, rc := encoderSEI(tc.text); enc != tc.enc || rc != tc.rc {
			t.Errorf("%.40q: %q %q, want %q %q", tc.text, enc, rc, tc.enc, tc.rc)
		}
	}
}

// userDataSEI is a user_data_unregistered SEI message (payload type 5: a 16-byte UUID, then the text) with its
// rbsp trailing bits, the size coded with 0xFF runs as for the encoders' 1-2 KB option strings.
func userDataSEI(text string) []byte {
	payload := append([]byte{0xdc, 0x45, 0xe9, 0xbd, 0xe6, 0xd9, 0x48, 0xb7, 0x96, 0x2c, 0xd8, 0x20, 0xd9, 0x23, 0xee, 0xef},
		append([]byte(text), 0)...)
	b := []byte{5}
	n := len(payload)
	for ; n >= 255; n -= 255 {
		b = append(b, 0xFF)
	}
	b = append(b, byte(n))
	return append(append(b, payload...), 0x80)
}

func lengthPrefixed(nals ...[]byte) []byte {
	var out []byte
	for _, n := range nals {
		l := make([]byte, 4)
		binary.BigEndian.PutUint32(l, uint32(len(n)))
		out = append(append(out, l...), n...)
	}
	return out
}

// sdrMKV is an SDR 1920x1080 Matroska file with one video track and its first frame.
func sdrMKV(codecID string, private, frame []byte) []byte {
	video := el(idTrackEntry, u(idTrackNumber, 1), u(idTrackType, 1), str(idCodecID, codecID),
		el(idCodecPrivate, private), el(idVideo, u(idPixelWidth, 1920), u(idPixelHeight, 1080)))
	audio := el(idTrackEntry, u(idTrackNumber, 2), u(idTrackType, 2), str(idCodecID, "A_AAC"), el(idAudio, u(idChannels, 2)))
	info := el(idInfo, u(idTimestampScale, 1000000), f64(idDuration, 7_200_000))
	cluster := unknownEl(idCluster, u(0xE7, 0), simpleBlock(1, frame))
	return append(el(idEBML, u(0x4286, 1)), unknownEl(idSegment, info, el(idTracks, video, audio), cluster)...)
}

func TestMatroskaEncoderSEI(t *testing.T) {
	avcC := []byte{1, 100, 0, 41, 0xFF, 0xE0, 0}
	slice := bytes.Repeat([]byte{0x65, 0x88}, 64) // an IDR slice after the SEI
	cases := []struct {
		name, codecID string
		private       []byte
		frame         []byte
		enc, rc       string
	}{
		{"x264 in the first frame", "V_MPEG4/ISO/AVC", avcC,
			lengthPrefixed(append([]byte{0x06}, userDataSEI(x264CRF)...), slice), "x264", "crf=20.0"},
		{"x264 bluray-compat", "V_MPEG4/ISO/AVC", avcC,
			lengthPrefixed(append([]byte{0x06}, userDataSEI(x264Bluray)...), slice), "x264", "2pass"},
		{"x265 in the first frame", "V_MPEGH/ISO/HEVC", testHvcC(),
			lengthPrefixed(append([]byte{39 << 1, 1}, userDataSEI(x265CRF)...), slice), "x265", "crf=18.0"},
		{"no encoder SEI", "V_MPEG4/ISO/AVC", avcC, lengthPrefixed(slice), "", ""},
	}
	for _, tc := range cases {
		data := sdrMKV(tc.codecID, tc.private, tc.frame)
		r, err := Reader(bytes.NewReader(data), int64(len(data)), 0)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if v := r.Video; v.Encoder != tc.enc || v.EncoderRC != tc.rc || v.FrameScanned || v.HDR10Plus {
			t.Errorf("%s: %+v", tc.name, v)
		}
	}
}

// ffmpeg's libx265 puts the encoder SEI in hvcC's NAL arrays: found without a frame (and in MP4, whose samples
// are not read).
func TestHvcCEncoderSEI(t *testing.T) {
	nal := append([]byte{39 << 1, 1}, userDataSEI(x265CRF)...)
	hvcc := testHvcC()
	hvcc[22] = 1
	arr := []byte{0x80 | 39, 0, 1, byte(len(nal) >> 8), byte(len(nal))}
	hvcc = append(append(hvcc, arr...), nal...)
	video := el(idTrackEntry, u(idTrackNumber, 1), u(idTrackType, 1), str(idCodecID, "V_MPEGH/ISO/HEVC"),
		el(idCodecPrivate, hvcc), el(idVideo, u(idPixelWidth, 3840), u(idPixelHeight, 2160)))
	info := el(idInfo, u(idTimestampScale, 1000000), f64(idDuration, 7_200_000))
	data := append(el(idEBML, u(0x4286, 1)), el(idSegment, info, el(idTracks, video))...)
	r, err := Reader(bytes.NewReader(data), int64(len(data)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Video.Encoder != "x265" || !strings.HasPrefix(r.Video.EncoderRC, "crf=") {
		t.Errorf("video %+v", r.Video)
	}
}
