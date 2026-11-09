// Package probe reads a video file's headers, never its whole body, and reports what the arrs' ffprobe-based
// VideoFileInfoReader would: container, run time, the primary video stream (codec, size, bit depth, colour, HDR
// format) and the audio and subtitle streams (codec, profile, channels, languages), from the headers only (docs/design/library.md).
//
// # Why our own readers
//
// The design names abema/go-mp4 for MP4, a small Matroska (EBML) reader of our own, and autobrr/go-mediainfo to be
// measured in shadow. This package has its own EBML and ISO-BMFF readers and takes no dependency:
//   - every read goes through one byte budget per file (16 MiB by default), which go-mediainfo cannot give: it reads
//     by path with no cap, and over an rclone mount each read is a ranged request to the debrid provider;
//   - the few MP4 boxes needed (moov/trak/mdia/stsd and the codec configuration boxes) do not justify a dependency;
//   - go-mediainfo is GPL-2.0-or-later, which can be taken under GPL-3.0 and so combined with AGPL-3.0, but there is
//     nothing it would add to the guard. It stays the shadow-mode comparison the design plans.
//
// ffprobe is not used: it is a binary in the image and an uncapped reader.
//
// # What is read
//
// Matroska: EBML header, Segment Info (duration), Tracks (codec ids, codec private, languages, video size, Colour,
// BlockAdditionMapping for Dolby Vision, ContentEncoding header stripping), and the first block of the primary video
// and audio tracks from the first clusters. MP4: moov (mvhd, tkhd, mdhd, hdlr, stsd with hvcC/avcC/av1C, dvcC/dvvC,
// colr, mdcv, clli, dec3, ddts/udts). The first frames are read for what the arrs' bundled ffprobe reports from
// the bitstream: TrueHD and E-AC-3 Atmos ("thd+", "ec+3"), the DTS profile (core, ES, 96/24, HD HRA, HD MA, DTS:X),
// and, for a PQ video stream as Radarr does, HDR10+ (SMPTE 2094-40) and mastering metadata in the first frame's SEI.
// For every H.264 and HEVC stream the first frame's user-data SEI is read for an x264/x265 encoder and its rate
// control, which the import's mislabel guard reads (at most 64 KiB of a non-PQ frame: the SEI precede the slices).
// MP4 reads only what hvcC carries, not the first sample.
//
// # Known limits
//
//   - Colour primaries and transfer come from the container (Matroska Colour, MP4 colr), not from the HEVC/AVC VUI.
//     mkvmerge copies them from the bitstream; a file without them is read as SDR unless it carries Dolby Vision.
//   - Run time is the container duration; ffprobe prefers the video stream's own duration (equal in practice).
//   - The Atmos and DTS:X checks follow the bitstream specifications; they are to be compared with the arrs' names
//     in shadow mode (theatarr library verify reports names that differ).
package probe

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/theatarr/theatarr/internal/library/naming"
)

// DefaultMaxBytes is the per-file read budget.
const DefaultMaxBytes = 16 << 20

// ErrBudget means the file's headers did not fit in the byte budget.
var ErrBudget = errors.New("probe: byte budget exhausted")

// ErrUnknownFormat means the file is neither Matroska nor MP4.
var ErrUnknownFormat = errors.New("probe: not a Matroska or MP4 file")

// Video is the primary video stream.
type Video struct {
	Codec     string `json:"codec"`              // ffprobe codec name: hevc, h264, av1, mpeg2video, vc1, vp9, mpeg4, ...
	CodecID   string `json:"codec_id,omitempty"` // container codec id (V_MPEGH/ISO/HEVC, hvc1, ...)
	Tag       string `json:"tag,omitempty"`      // codec tag as the arr compares it (XVID, DIVX, DX50, DIV3)
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	BitDepth  int    `json:"bit_depth"`
	Primaries string `json:"primaries,omitempty"` // ffprobe colour names: bt2020, bt709
	Transfer  string `json:"transfer,omitempty"`  // smpte2084, arib-std-b67, bt709
	// DoviCompat is the Dolby Vision configuration's dv_bl_signal_compatibility_id, -1 without Dolby Vision.
	DoviCompat   int  `json:"dovi_compat"`
	DoviProfile  int  `json:"dovi_profile,omitempty"`
	HDR10Plus    bool `json:"hdr10plus,omitempty"`
	Mastering    bool `json:"mastering,omitempty"` // mastering display or content light level metadata
	MultiView    int  `json:"multi_view,omitempty"`
	FrameScanned bool `json:"frame_scanned,omitempty"`
	// Encoder is the library of an x264 or x265 user-data SEI in the first frame or hvcC ("x264", "x265"; MediaInfo's
	// Writing library), and EncoderRC its rate control from the options: "crf=18.0" for CRF, else "abr", "2pass", ...
	Encoder   string `json:"encoder,omitempty"`
	EncoderRC string `json:"encoder_rc,omitempty"`
}

// Audio is one audio stream.
type Audio struct {
	Codec    string `json:"codec"`              // ffprobe codec name: truehd, eac3, ac3, dts, aac, flac, opus, ...
	CodecID  string `json:"codec_id,omitempty"` // container codec id
	Tag      string `json:"tag,omitempty"`      // "thd+" / "ec+3" for Atmos, as the arrs' ffprobe tags them
	Profile  string `json:"profile,omitempty"`  // DTS profile; "LC"/"HE-AAC" for AAC
	Channels int    `json:"channels"`
	Language string `json:"language,omitempty"`
}

// Result is what a probe found.
type Result struct {
	Container      string   `json:"container"` // matroska or mp4
	RunTimeSeconds float64  `json:"runtime_seconds"`
	Video          *Video   `json:"video,omitempty"`
	Audio          []Audio  `json:"audio,omitempty"`
	Subtitles      []string `json:"subtitles,omitempty"`
	Title          string   `json:"title,omitempty"`
	BytesRead      int64    `json:"bytes_read"`
}

// File probes the file at path, reading at most maxBytes (DefaultMaxBytes when 0).
func File(path string, maxBytes int64) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return Reader(f, st.Size(), maxBytes)
}

// Reader probes size bytes from r, reading at most maxBytes (DefaultMaxBytes when 0).
func Reader(r io.ReaderAt, size, maxBytes int64) (*Result, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	br := &budgetReader{r: r, size: size, left: maxBytes}
	head := make([]byte, 12)
	if _, err := br.ReadAt(head, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	var res *Result
	var err error
	switch {
	case head[0] == 0x1A && head[1] == 0x45 && head[2] == 0xDF && head[3] == 0xA3:
		res, err = probeMatroska(br)
	case string(head[4:8]) == "ftyp" || string(head[4:8]) == "moov" || string(head[4:8]) == "free" ||
		string(head[4:8]) == "mdat" || string(head[4:8]) == "wide":
		res, err = probeMP4(br)
	default:
		return nil, ErrUnknownFormat
	}
	if res != nil {
		res.BytesRead = maxBytes - br.left
	}
	return res, err
}

// budgetReader is an io.ReaderAt that fails once a byte budget is spent.
type budgetReader struct {
	r    io.ReaderAt
	size int64
	left int64
}

func (b *budgetReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= b.size {
		return 0, io.EOF
	}
	if int64(len(p)) > b.size-off {
		p = p[:b.size-off]
	}
	if int64(len(p)) > b.left {
		return 0, ErrBudget
	}
	n, err := b.r.ReadAt(p, off)
	b.left -= int64(n)
	if err == nil && n < len(p) {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

// read reads exactly n bytes at off.
func (b *budgetReader) read(off int64, n int) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("probe: bad length %d", n)
	}
	p := make([]byte, n)
	got, err := b.ReadAt(p, off)
	if err != nil && !(errors.Is(err, io.EOF) && got == n) {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return p[:got], io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return p, nil
}

// channelLayout is ffprobe's default layout name for a channel count, as far as MediaInfoFormatter reads it (the
// leading "d.d" of the layout).
func channelLayout(ch int) string {
	switch ch {
	case 3:
		return "2.1"
	case 5:
		return "5.0"
	case 6:
		return "5.1"
	case 7:
		return "6.1"
	case 8:
		return "7.1"
	}
	return ""
}

// Model converts the result to the arrs' MediaInfoModel, from which naming formats the tokens.
func (r *Result) Model() *naming.Model {
	m := &naming.Model{ContainerFormat: r.Container, RunTimeSeconds: r.RunTimeSeconds, AudioStreamCount: len(r.Audio)}
	if v := r.Video; v != nil {
		m.VideoFormat, m.VideoCodecID, m.Width, m.Height = v.Codec, v.Tag, v.Width, v.Height
		m.VideoBitDepth = v.BitDepth
		if m.VideoBitDepth == 0 {
			m.VideoBitDepth = 8
		}
		m.VideoMultiViewCount = 1
		if v.MultiView > 1 {
			m.VideoMultiViewCount = v.MultiView
		}
		m.VideoColourPrimaries, m.VideoTransferChars = v.Primaries, v.Transfer
		m.VideoHdrFormat = naming.HdrFormatOf(m.VideoBitDepth, v.Primaries, v.Transfer, v.DoviCompat, v.HDR10Plus, v.Mastering)
	}
	if len(r.Audio) > 0 {
		a := r.Audio[0]
		m.AudioFormat, m.AudioCodecID, m.AudioProfile, m.AudioChannels = a.Codec, a.Tag, a.Profile, a.Channels
		m.AudioChannelPositions = channelLayout(a.Channels)
	}
	for _, a := range r.Audio {
		if strings.TrimSpace(a.Language) != "" {
			m.AudioLanguages = append(m.AudioLanguages, a.Language)
		}
	}
	for _, s := range r.Subtitles {
		if strings.TrimSpace(s) != "" {
			m.Subtitles = append(m.Subtitles, s)
		}
	}
	return m
}
