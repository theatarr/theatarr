package probe

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"testing"

	"github.com/theatarr/theatarr/internal/library/naming"
)

// ebml builds Matroska elements for tests.
func ebmlID(id int64) []byte {
	switch {
	case id >= 1<<24:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	case id >= 1<<16:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	case id >= 1<<8:
		return []byte{byte(id >> 8), byte(id)}
	}
	return []byte{byte(id)}
}

func ebmlSize(n int) []byte {
	// 8-byte size, always valid.
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(n))
	b[0] = 0x01
	return b
}

func el(id int64, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	return append(append(ebmlID(id), ebmlSize(len(body))...), body...)
}

func unknownEl(id int64, parts ...[]byte) []byte {
	return append(append(ebmlID(id), 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF), bytes.Join(parts, nil)...)
}

func u(id int64, v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return el(id, b)
}

func str(id int64, s string) []byte { return el(id, []byte(s)) }

func f64(id int64, v float64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, math.Float64bits(v))
	return el(id, b)
}

// hvcC with 10-bit luma and 4-byte NAL lengths, no arrays.
func testHvcC() []byte {
	b := make([]byte, 23)
	b[0] = 1
	b[17] = 0xF8 | 2 // bitDepthLumaMinus8 = 2
	b[21] = 0x0F     // lengthSizeMinusOne = 3
	return b
}

// hdr10PlusFrame is one length-prefixed prefix SEI NAL with an SMPTE 2094-40 payload and a mastering display one.
func hdr10PlusFrame() []byte {
	sei := []byte{4, 7, 0xB5, 0x00, 0x3C, 0x00, 0x01, 0x04, 0x00, 137, 2, 0x11, 0x22, 0x80}
	nal := append([]byte{39 << 1, 1}, sei...)
	l := make([]byte, 4)
	binary.BigEndian.PutUint32(l, uint32(len(nal)))
	return append(l, nal...)
}

func truehdFrame() []byte {
	f := make([]byte, 40)
	copy(f[4:], []byte{0xF8, 0x72, 0x6F, 0xBA})
	f[12], f[13] = 0xB7, 0x52
	f[4+17] = 0x80 // substream_info: 16-channel presentation
	return f
}

func simpleBlock(track byte, data []byte) []byte {
	return el(idSimpleBlock, []byte{0x80 | track, 0, 0, 0x80}, data)
}

func testMKV(withFrames bool) []byte {
	dovi := []byte{1, 0, 8 << 1, 0x10<<3 | 0x5, 6 << 4} // profile 8, compat 6
	video := el(idTrackEntry, u(idTrackNumber, 1), u(idTrackType, 1), str(idCodecID, "V_MPEGH/ISO/HEVC"),
		el(idCodecPrivate, testHvcC()),
		el(idVideo, u(idPixelWidth, 3840), u(idPixelHeight, 2160),
			el(idColour, u(idTransfer, 16), u(idPrimaries, 9))),
		el(idBlockAddMapping, u(idBlockAddIDType, 0x64767643), el(idBlockAddIDExtra, dovi)))
	audio := el(idTrackEntry, u(idTrackNumber, 2), u(idTrackType, 2), str(idCodecID, "A_TRUEHD"),
		str(idLanguage, "eng"), el(idAudio, u(idChannels, 8)))
	audio2 := el(idTrackEntry, u(idTrackNumber, 3), u(idTrackType, 2), str(idCodecID, "A_AC3"),
		str(idLanguage, "fre"), el(idAudio, u(idChannels, 6)))
	sub := el(idTrackEntry, u(idTrackNumber, 4), u(idTrackType, 17), str(idCodecID, "S_TEXT/UTF8"), str(idLanguage, "ger"))
	info := el(idInfo, u(idTimestampScale, 1000000), f64(idDuration, 7_200_000))
	var clusters []byte
	if withFrames {
		clusters = unknownEl(idCluster, u(0xE7, 0), simpleBlock(1, hdr10PlusFrame()), simpleBlock(3, []byte{0x0B, 0x77, 0, 0}),
			simpleBlock(2, truehdFrame()))
	}
	seg := append(el(idSegment, info, el(idTracks, video, audio, audio2, sub)), nil...)
	if withFrames {
		seg = append(unknownEl(idSegment, info, el(idTracks, video, audio, audio2, sub)), clusters...)
	}
	return append(el(idEBML, u(0x4286, 1)), seg...)
}

func TestMatroska(t *testing.T) {
	data := testMKV(true)
	r, err := Reader(bytes.NewReader(data), int64(len(data)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Container != "matroska" || r.RunTimeSeconds != 7200 {
		t.Errorf("container/runtime: %+v", r)
	}
	v := r.Video
	if v == nil || v.Codec != "hevc" || v.Width != 3840 || v.Height != 2160 || v.BitDepth != 10 || v.Transfer != "smpte2084" ||
		v.Primaries != "bt2020" || v.DoviCompat != 6 || v.DoviProfile != 8 || !v.HDR10Plus || !v.Mastering {
		t.Errorf("video: %+v", v)
	}
	if len(r.Audio) != 2 || r.Audio[0].Codec != "truehd" || r.Audio[0].Tag != "thd+" || r.Audio[0].Channels != 8 ||
		r.Audio[1].Language != "fre" {
		t.Errorf("audio: %+v", r.Audio)
	}
	if len(r.Subtitles) != 1 || r.Subtitles[0] != "ger" {
		t.Errorf("subtitles: %v", r.Subtitles)
	}
	mi := r.Model().Format("Movie.2160p.UHD.BluRay.REMUX.HEVC-GRP", false)
	if mi.VideoCodec != "HEVC" || mi.AudioCodec != "TrueHD Atmos" || mi.AudioChannels != 7.1 ||
		mi.VideoDynamicRangeType != "DV HDR10Plus" || mi.VideoBitDepth != 10 {
		t.Errorf("formatted: %+v", mi)
	}
	if r.BytesRead > 4*int64(len(data)) || r.BytesRead == 0 {
		t.Errorf("bytes read %d of %d", r.BytesRead, len(data))
	}
}

// The legacy language element wins unless it is absent or und; then the LanguageIETF tag's primary subtag is used.
// Element order does not matter.
func TestMatroskaLanguageIETF(t *testing.T) {
	cases := []struct {
		legacy, ietf, want string
	}{
		{"", "ja-JP", "ja"},
		{"chi", "cmn-Hans", "chi"},
		{"nob", "nb-NO", "nob"},
		{"und", "ja", "ja"},
		{"", " ", "eng"},
		{"", "", "eng"},
	}
	for _, c := range cases {
		var l, i []byte
		if c.legacy != "" {
			l = str(idLanguage, c.legacy)
		}
		if c.ietf != "" {
			i = str(idLanguageIETF, c.ietf)
		}
		for _, body := range [][]byte{bytes.Join([][]byte{u(idTrackNumber, 2), l, i}, nil),
			bytes.Join([][]byte{u(idTrackNumber, 2), i, l}, nil)} {
			tr, err := parseTrack(body)
			if err != nil || tr.language != c.want {
				t.Errorf("legacy %q ietf %q: language %q %v, want %q", c.legacy, c.ietf, tr.language, err, c.want)
			}
		}
	}
}

func TestMatroskaNoFrames(t *testing.T) {
	data := testMKV(false)
	r, err := Reader(bytes.NewReader(data), int64(len(data)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Video.HDR10Plus || r.Audio[0].Tag != "" {
		t.Errorf("no frames, no HDR10+ or Atmos: %+v %+v", r.Video, r.Audio[0])
	}
	if got := naming.FormatVideoDynamicRangeType(r.Model(), false); got != "DV HDR10" {
		t.Errorf("hdr type %q", got)
	}
}

// zeroReader is a huge sparse file: zeros except for the given chunks.
type zeroReader struct {
	size   int64
	chunks map[int64][]byte
}

func (z *zeroReader) ReadAt(p []byte, off int64) (int, error) {
	for i := range p {
		p[i] = 0
	}
	for at, c := range z.chunks {
		for i := range c {
			if pos := at + int64(i) - off; pos >= 0 && pos < int64(len(p)) {
				p[pos] = c[i]
			}
		}
	}
	return len(p), nil
}

func TestBudget(t *testing.T) {
	data := testMKV(true)
	if _, err := Reader(bytes.NewReader(data), int64(len(data)), 64); !errors.Is(err, ErrBudget) {
		t.Errorf("tiny budget: %v", err)
	}
	// A 50 GB file whose first cluster comes after the tracks: only headers are read.
	big := &zeroReader{size: 50 << 30, chunks: map[int64][]byte{0: data}}
	r, err := Reader(big, big.size, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.BytesRead > 1<<20 {
		t.Errorf("read %d bytes", r.BytesRead)
	}
}

func box(typ string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b, uint32(8+len(body)))
	copy(b[4:], typ)
	return append(b, body...)
}

func TestMP4MoovAtEnd(t *testing.T) {
	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[12:], 1000)
	binary.BigEndian.PutUint32(mvhd[16:], 5_400_000) // 90 min
	mdhd := make([]byte, 24)
	binary.BigEndian.PutUint16(mdhd[20:], uint16(('j'-0x60)<<10|('p'-0x60)<<5|('n'-0x60)))
	hdlr := func(h string) []byte { b := make([]byte, 24); copy(b[8:], h); return b }
	visual := make([]byte, 78)
	binary.BigEndian.PutUint16(visual[24:], 1920)
	binary.BigEndian.PutUint16(visual[26:], 1080)
	avcC := []byte{1, 100, 0, 40, 0xFF, 0xE1}
	// SPS: profile 100, chroma 1, bit depth luma minus8 = 2 (10-bit): ue(0)=1, ue(1)=010, ue(2)=011.
	sps := []byte{0x67, 100, 0, 40, 0b1010_0110}
	avcC = append(avcC, 0, byte(len(sps)))
	avcC = append(avcC, sps...)
	vEntry := box("avc1", visual, box("avcC", avcC))
	stsd := func(entry []byte) []byte { return box("stsd", []byte{0, 0, 0, 0, 0, 0, 0, 1}, entry) }
	trakV := box("trak", box("mdia", box("mdhd", mdhd), box("hdlr", hdlr("vide")),
		box("minf", box("stbl", stsd(vEntry)))))
	audio := make([]byte, 28)
	binary.BigEndian.PutUint16(audio[16:], 6)
	// One substream (acmod 7, lfeon: 5.1), no dependents, then flag_ec3_extension_type_a.
	dec3 := []byte{0, 0, 0, 0x0F, 0, 0x01}
	aEntry := box("ec-3", audio, box("dec3", dec3))
	trakA := box("trak", box("mdia", box("mdhd", mdhd), box("hdlr", hdlr("soun")),
		box("minf", box("stbl", stsd(aEntry)))))
	moov := box("moov", box("mvhd", mvhd), trakV, trakA)
	ftyp := box("ftyp", []byte("isom\x00\x00\x02\x00"))
	mdatSize := int64(20 << 30)
	mdatHdr := []byte{0, 0, 0, 1, 'm', 'd', 'a', 't', 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint64(mdatHdr[8:], uint64(mdatSize))
	z := &zeroReader{size: int64(len(ftyp)) + mdatSize + int64(len(moov)),
		chunks: map[int64][]byte{0: ftyp, int64(len(ftyp)): mdatHdr, int64(len(ftyp)) + mdatSize: moov}}
	r, err := Reader(z, z.size, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Container != "mp4" || r.RunTimeSeconds != 5400 || r.Video == nil || r.Video.Codec != "h264" ||
		r.Video.Width != 1920 || r.Video.BitDepth != 10 {
		t.Errorf("mp4: %+v %+v", r, r.Video)
	}
	if len(r.Audio) != 1 || r.Audio[0].Codec != "eac3" || r.Audio[0].Tag != "ec+3" || r.Audio[0].Language != "jpn" {
		t.Errorf("audio: %+v", r.Audio)
	}
	if mi := r.Model().Format("", false); mi.AudioCodec != "EAC3 Atmos" || mi.AudioChannels != 5.1 {
		t.Errorf("formatted %+v", mi)
	}
}

func TestDTSProfiles(t *testing.T) {
	core := append([]byte{}, dtsCore...)
	core = append(core, make([]byte, 20)...)
	ma := append(append(append([]byte{}, core...), dtsSubstream...), append(make([]byte, 8), dtsXLL...)...)
	x := append(append(append([]byte{}, core...), dtsSubstream...), append(make([]byte, 8), dtsXLLX...)...)
	hra := append(append(append([]byte{}, core...), dtsSubstream...), append(make([]byte, 8), dtsXBR...)...)
	for _, c := range []struct {
		f    []byte
		want string
	}{{core, "DTS"}, {ma, "DTS-HD MA"}, {x, "DTS:X"}, {hra, "DTS-HD HRA"}} {
		if got := dtsProfile(c.f); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestEAC3Atmos(t *testing.T) {
	for _, flag := range []uint32{1, 0} {
		frame := eac3Frame(flag)
		if atmos, ok := eac3Atmos(frame); !ok || atmos != (flag == 1) {
			t.Errorf("flag %d: atmos %v ok %v", flag, atmos, ok)
		}
	}
}

func eac3Frame(flag uint32) []byte {
	// strmtyp 0, substreamid 0, frmsiz, fscod 0, numblkscod 3, acmod 7, lfeon 1, bsid 16, dialnorm, compre 0,
	// mixmdate 0, infomdate 0, addbsie 1, addbsil 0, 7 bits, flag 1.
	w := &bitWriter{}
	w.put(0, 2)
	w.put(0, 3)
	w.put(100, 11)
	w.put(0, 2)
	w.put(3, 2)
	w.put(7, 3)
	w.put(1, 1)
	w.put(16, 5)
	w.put(27, 5)
	w.put(0, 1)
	w.put(0, 1) // mixmdate
	w.put(0, 1) // infomdate
	w.put(1, 1) // addbsie
	w.put(1, 6)
	w.put(0, 7)
	w.put(flag, 1)
	return append([]byte{0x0B, 0x77}, w.bytes()...)
}

type bitWriter struct {
	b []byte
	n int
}

func (w *bitWriter) put(v uint32, bits int) {
	for i := bits - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>uint(i)&1 == 1 {
			w.b[len(w.b)-1] |= 1 << uint(7-w.n%8)
		}
		w.n++
	}
}

func (w *bitWriter) bytes() []byte { return append(w.b, 0, 0) }

func TestUnknownFormat(t *testing.T) {
	data := []byte("not a video file at all")
	if _, err := Reader(bytes.NewReader(data), int64(len(data)), 0); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("err %v", err)
	}
}

// mkvSeek builds Segment[SeekHead, Info, Cluster, Tracks, SeekHead2?]: Tracks sits after the first Cluster, so only
// the SeekHead finds it. mode "direct": the first SeekHead points at Tracks; "chained": it points at a second
// SeekHead (after Tracks) that points at Tracks; "loop": as chained, but the second SeekHead points back at the first.
func mkvSeek(mode string) []byte {
	video := el(idTrackEntry, u(idTrackNumber, 1), u(idTrackType, 1), str(idCodecID, "V_MPEGH/ISO/HEVC"),
		el(idCodecPrivate, testHvcC()), el(idVideo, u(idPixelWidth, 3840), u(idPixelHeight, 2160)))
	audio := el(idTrackEntry, u(idTrackNumber, 2), u(idTrackType, 2), str(idCodecID, "A_AC3"),
		str(idLanguage, "eng"), el(idAudio, u(idChannels, 6)))
	tracks := el(idTracks, video, audio)
	info := el(idInfo, u(idTimestampScale, 1000000), f64(idDuration, 7_200_000))
	cluster := el(idCluster, u(0xE7, 0), simpleBlock(2, []byte{0x0B, 0x77, 0, 0}))
	seek := func(id int64, pos int) []byte {
		return el(idSeekHead, el(idSeek, el(idSeekID, ebmlID(id)), u(idSeekPosition, uint64(pos))))
	}
	headLen := len(seek(idTracks, 0))
	tracksPos := headLen + len(info) + len(cluster)
	var body []byte
	switch mode {
	case "direct":
		body = bytes.Join([][]byte{seek(idTracks, tracksPos), info, cluster, tracks}, nil)
	case "chained":
		body = bytes.Join([][]byte{seek(idSeekHead, tracksPos+len(tracks)), info, cluster, tracks,
			seek(idTracks, tracksPos)}, nil)
	case "loop":
		body = bytes.Join([][]byte{seek(idSeekHead, tracksPos+len(tracks)), info, cluster, tracks,
			seek(idSeekHead, 0)}, nil)
	}
	return append(el(idEBML, u(0x4286, 1)), el(idSegment, body)...)
}

// A SeekHead that points at a second SeekHead holding Tracks is followed once; a loop back ends in the usual error.
func TestMatroskaChainedSeekHead(t *testing.T) {
	for _, mode := range []string{"direct", "chained"} {
		data := mkvSeek(mode)
		r, err := Reader(bytes.NewReader(data), int64(len(data)), 0)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if r.Video == nil || r.Video.Width != 3840 || len(r.Audio) != 1 || r.Audio[0].Channels != 6 {
			t.Fatalf("%s: tracks not read: %+v", mode, r)
		}
	}
	data := mkvSeek("loop")
	if _, err := Reader(bytes.NewReader(data), int64(len(data)), 0); err == nil ||
		err.Error() != "probe: no Matroska Tracks element" {
		t.Fatalf("loop: %v", err)
	}
}

// mp4Sound is a one-track MP4 whose audio sample entry is format with body (from the reserved bytes on) and children.
func mp4Sound(format string, body []byte, children ...[]byte) []byte {
	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[12:], 1000)
	binary.BigEndian.PutUint32(mvhd[16:], 5_400_000)
	hdlr := make([]byte, 24)
	copy(hdlr[8:], "soun")
	stsd := box("stsd", []byte{0, 0, 0, 0, 0, 0, 0, 1}, box(format, append([][]byte{body}, children...)...))
	trak := box("trak", box("mdia", box("mdhd", make([]byte, 24)), box("hdlr", hdlr), box("minf", box("stbl", stsd))))
	return append(box("ftyp", []byte("isom\x00\x00\x02\x00")), box("moov", box("mvhd", mvhd), trak)...)
}

// soundEntry is a sound sample entry body of the given QuickTime version (0: 28 bytes, 1: 44, 2: 64) whose
// 16-bit channel count says channels; version 2 holds v2Channels in its 32-bit field.
func soundEntry(version, channels uint16, v2Channels uint32) []byte {
	b := make([]byte, map[uint16]int{0: 28, 1: 44, 2: 64}[version])
	binary.BigEndian.PutUint16(b[8:], version)
	binary.BigEndian.PutUint16(b[16:], channels)
	if version == 2 {
		binary.BigEndian.PutUint32(b[40:], v2Channels)
	}
	return b
}

// AC-3 and E-AC-3 in MP4 take their channel count from dac3/dec3 (acmod, lfeon), not the sample entry's channel
// field, which muxers often leave at 2; QuickTime version 1 and 2 sound entries have their child boxes further on,
// version 1 (as ffmpeg's mov muxer writes it) inside a wave box.
func TestMP4AC3Channels(t *testing.T) {
	dac3 := box("dac3", []byte{0x50, 0x3d, 0xc0})                   // ffmpeg's 5.1 AC-3: acmod 7, lfeon 1
	dec3 := box("dec3", []byte{0x0c, 0x00, 0x60, 0x0f, 0x00, 0x00}) // ffmpeg's 5.1 E-AC-3
	dec3Atmos := box("dec3", []byte{0x0c, 0x00, 0x60, 0x0f, 0x00, 0x01})
	dec3Stereo := box("dec3", []byte{0x0c, 0x00, 0x60, 0x04, 0x00, 0x00}) // acmod 2, no LFE
	// 5.1 plus a dependent substream with chan_loc Lrs/Rrs (7.1), and with Lw/Rw, Cs and LFE2 (10 channels).
	dec3Dependent := box("dec3", []byte{0x0c, 0x00, 0x60, 0x0f, 0x02, 0x02, 0x00})
	dec3Wide := box("dec3", []byte{0x0c, 0x00, 0x60, 0x0f, 0x03, 0x24, 0x00})
	wave := func(format string, b []byte) []byte {
		return box("wave", box("frma", []byte(format)), b, make([]byte, 8))
	}
	for _, c := range []struct {
		name     string
		data     []byte
		channels int
		tag      string
	}{
		{"v0 ac-3", mp4Sound("ac-3", soundEntry(0, 2, 0), dac3), 6, ""},
		{"v0 ec-3", mp4Sound("ec-3", soundEntry(0, 2, 0), dec3), 6, ""},
		{"v0 ec-3 atmos", mp4Sound("ec-3", soundEntry(0, 2, 0), dec3Atmos), 6, "ec+3"},
		{"v0 ec-3 stereo", mp4Sound("ec-3", soundEntry(0, 6, 0), dec3Stereo), 2, ""},
		{"v0 ec-3 7.1", mp4Sound("ec-3", soundEntry(0, 2, 0), dec3Dependent), 8, ""},
		{"v0 ec-3 chan_loc pairs and singles", mp4Sound("ec-3", soundEntry(0, 2, 0), dec3Wide), 10, ""},
		{"v1 ac-3 in wave", mp4Sound("ac-3", soundEntry(1, 2, 0), wave("ac-3", dac3)), 6, ""},
		{"v1 ec-3 in wave", mp4Sound("ec-3", soundEntry(1, 2, 0), wave("ec-3", dec3Atmos)), 6, "ec+3"},
		{"v1 ec-3 bare", mp4Sound("ec-3", soundEntry(1, 2, 0), dec3Atmos), 6, "ec+3"},
		{"v2 ec-3", mp4Sound("ec-3", soundEntry(2, 3, 2), dec3Atmos), 6, "ec+3"},
		{"v2 lpcm", mp4Sound("lpcm", soundEntry(2, 3, 6)), 6, ""},
	} {
		r, err := Reader(bytes.NewReader(c.data), int64(len(c.data)), 0)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(r.Audio) != 1 || r.Audio[0].Channels != c.channels || r.Audio[0].Tag != c.tag {
			t.Errorf("%s: audio %+v, want %d channels tag %q", c.name, r.Audio, c.channels, c.tag)
		}
	}
}

func TestModelLiftsHdrBitDepth(t *testing.T) {
	for _, c := range []struct {
		v    Video
		want string
	}{
		{Video{Codec: "hevc", BitDepth: 8, DoviCompat: 1, DoviProfile: 8}, "DV HDR10"},
		{Video{Codec: "hevc", BitDepth: 8, DoviCompat: 6, DoviProfile: 7}, "DV HDR10"},
		{Video{Codec: "hevc", BitDepth: 8, DoviCompat: 0, DoviProfile: 5}, "DV"},
		{Video{Codec: "hevc", BitDepth: 8, DoviCompat: -1, Primaries: "bt2020", Transfer: "smpte2084"}, "PQ"},
		{Video{Codec: "hevc", BitDepth: 8, DoviCompat: -1, Primaries: "bt2020", Transfer: "smpte2084", Mastering: true}, "HDR10"},
		{Video{Codec: "hevc", BitDepth: 8, DoviCompat: -1, Primaries: "bt709", Transfer: "bt709"}, ""},
		{Video{Codec: "h264", BitDepth: 8, DoviCompat: 1, DoviProfile: 9}, ""},
	} {
		v := c.v
		if got := naming.FormatVideoDynamicRangeType((&Result{Video: &v}).Model(), false); got != c.want {
			t.Errorf("%+v: %q, want %q", c.v, got, c.want)
		}
	}
}

// ue writes an unsigned Exp-Golomb code.
func (w *bitWriter) ue(v uint32) {
	n := v + 1
	bits := 0
	for x := n; x > 0; x >>= 1 {
		bits++
	}
	w.put(0, bits-1)
	w.put(n, bits)
}

// escapeRBSP adds emulation prevention bytes, as an encoder does.
func escapeRBSP(b []byte) []byte {
	var out []byte
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

type spsOpts struct {
	subLayers               int // sps_max_sub_layers_minus1
	scaling, sets, longTerm bool
	noVUI, noColour         bool
	primaries, transfer     uint32
}

// testSPS builds an HEVC SPS NAL unit (ITU-T H.265 7.3.2.2) with the parts that are easy to get wrong when asked.
func testSPS(o spsOpts) []byte {
	w := &bitWriter{}
	w.put(0, 4)
	w.put(uint32(o.subLayers), 3)
	w.put(1, 1)
	// general profile_tier_level: Main 10, level 5.1 (96 bits)
	w.put(2, 8)
	w.put(0x20000000, 32)
	w.put(0xB, 4)
	w.put(0, 32)
	w.put(0, 12)
	w.put(153, 8)
	for i := 0; i < o.subLayers; i++ {
		w.put(uint32(1-i%2), 1) // sub_layer_profile_present_flag on even layers
		w.put(1, 1)             // sub_layer_level_present_flag
	}
	if o.subLayers > 0 {
		w.put(0, 2*(8-o.subLayers))
	}
	for i := 0; i < o.subLayers; i++ {
		if i%2 == 0 {
			w.put(0x5AAAAAAA, 32)
			w.put(0x55555555, 32)
			w.put(0xA5A5A5, 24)
		}
		w.put(120, 8)
	}
	w.ue(0)    // sps_seq_parameter_set_id
	w.ue(1)    // chroma_format_idc
	w.ue(3840) // width
	w.ue(2160) // height
	w.put(1, 1)
	w.ue(0)
	w.ue(0)
	w.ue(0)
	w.ue(4)
	w.ue(2) // bit depths
	w.ue(2)
	w.ue(4) // log2_max_pic_order_cnt_lsb_minus4: 8 bits
	w.put(1, 1)
	for i := 0; i <= o.subLayers; i++ {
		w.ue(4)
		w.ue(2)
		w.ue(0)
	}
	for _, v := range []uint32{0, 2, 0, 3, 1, 1} {
		w.ue(v)
	}
	if o.scaling {
		w.put(1, 1)
		w.put(1, 1)
		for sizeID := 0; sizeID < 4; sizeID++ {
			step := 1
			if sizeID == 3 {
				step = 3
			}
			for m := 0; m < 6; m += step {
				if m%2 == 0 {
					w.put(0, 1)
					w.ue(uint32(m % 3))
					continue
				}
				w.put(1, 1)
				if sizeID > 1 {
					w.ue(5)
				}
				for i := 0; i < min(64, 1<<(4+sizeID<<1)); i++ {
					w.ue(uint32(i % 3))
				}
			}
		}
	} else {
		w.put(0, 1)
	}
	w.put(3, 2) // amp, sao
	if o.scaling {
		w.put(1, 1) // pcm
		w.put(0x77, 8)
		w.ue(0)
		w.ue(1)
		w.put(1, 1)
	} else {
		w.put(0, 1)
	}
	if o.sets {
		w.ue(5)
		// 0: S0 -1 -3, S1 +1
		w.ue(2)
		w.ue(1)
		w.ue(0)
		w.put(1, 1)
		w.ue(1)
		w.put(1, 1)
		w.ue(0)
		w.put(1, 1)
		// 1: predicted from 0 with deltaRps -1 (4 flag pairs): S0 -1 -2 -4
		w.put(1, 1)
		w.put(1, 1)
		w.ue(0)
		w.put(1, 1)    // j0 used
		w.put(0b01, 2) // j1 use_delta
		w.put(0b00, 2) // j2 dropped
		w.put(1, 1)    // j3 used
		// 2: predicted from 1 with deltaRps +3 (4 flag pairs): S0 -1, S1 1 2 3
		w.put(1, 1)
		w.put(0, 1)
		w.ue(2)
		w.put(0b1111, 4)
		// 3: predicted from 2 with deltaRps -1: 5 flag pairs (NumDeltaPocs[2] = 4), all use_delta
		w.put(1, 1)
		w.put(1, 1)
		w.ue(0)
		w.put(0b0101010101, 10)
		// 4: explicit, one negative picture
		w.put(0, 1)
		w.ue(1)
		w.ue(0)
		w.ue(0)
		w.put(1, 1)
	} else {
		w.ue(0)
	}
	if o.longTerm {
		w.put(1, 1)
		w.ue(2)
		w.put(0xAB, 8)
		w.put(1, 1)
		w.put(0x12, 8)
		w.put(0, 1)
	} else {
		w.put(0, 1)
	}
	w.put(3, 2) // temporal mvp, strong intra smoothing
	if o.noVUI {
		w.put(0, 1)
	} else {
		w.put(1, 1)
		w.put(1, 1) // aspect_ratio_info_present_flag
		w.put(255, 8)
		w.put(1, 16)
		w.put(1, 16)
		w.put(0, 1) // overscan
		w.put(1, 1) // video_signal_type_present_flag
		w.put(5, 3)
		w.put(0, 1)
		if o.noColour {
			w.put(0, 1)
		} else {
			w.put(1, 1)
			w.put(o.primaries, 8)
			w.put(o.transfer, 8)
			w.put(9, 8)
		}
		w.put(0, 7) // the rest of the VUI's flags off
	}
	w.put(0, 1) // sps_extension_present_flag
	w.put(1, 1) // rbsp_stop_one_bit
	return append([]byte{33 << 1, 1}, escapeRBSP(w.b)...)
}

// hvcCWith is testHvcC with one array per NAL unit.
func hvcCWith(nals ...[]byte) []byte {
	b := testHvcC()
	b[22] = byte(len(nals))
	for _, n := range nals {
		b = append(b, 0x80|(n[0]>>1)&0x3F, 0, 1, byte(len(n)>>8), byte(len(n)))
		b = append(b, n...)
	}
	return b
}

func TestHEVCSPSColour(t *testing.T) {
	complexSPS := spsOpts{subLayers: 3, scaling: true, sets: true, longTerm: true, primaries: 9, transfer: 16}
	for name, c := range map[string]struct {
		o         spsOpts
		prim, trc uint64
		ok        bool
	}{
		"pq":                 {spsOpts{primaries: 9, transfer: 16}, 9, 16, true},
		"hlg":                {spsOpts{primaries: 9, transfer: 18}, 9, 18, true},
		"bt709":              {spsOpts{primaries: 1, transfer: 1}, 1, 1, true},
		"sublayers and sets": {complexSPS, 9, 16, true},
		"one sub-layer":      {spsOpts{subLayers: 1, sets: true, primaries: 9, transfer: 18}, 9, 18, true},
		"no vui":             {spsOpts{noVUI: true}, 0, 0, false},
		"no colour":          {spsOpts{noColour: true, sets: true}, 0, 0, false},
	} {
		p, tr, ok := hevcSPSColour(testSPS(c.o))
		if p != c.prim || tr != c.trc || ok != c.ok {
			t.Errorf("%s: %d %d %v", name, p, tr, ok)
		}
	}
	// Truncated anywhere: never a panic, never a wrong value.
	full := testSPS(complexSPS)
	for n := 0; n < len(full); n++ {
		p, tr, ok := hevcSPSColour(full[:n])
		if ok && (p != 9 || tr != 16) {
			t.Errorf("cut at %d: %d %d", n, p, tr)
		}
		if n < len(full)/2 && ok {
			t.Errorf("cut at %d of %d still read", n, len(full))
		}
	}
	// Real SPS units written by x265 (ffmpeg 4.4 libx265, 320x180): one 10-bit with BT.2020 PQ, one 8-bit with
	// BT.709 primaries and HLG; ffprobe reads them as bt2020/smpte2084 and bt709/arib-std-b67.
	for _, c := range []struct {
		hex       string
		prim, trc uint64
	}{
		{"42010102200000030090000003000003003ca00a080b9f6d96566924caf016a122012080000003008000000c04", 9, 16},
		{"42010101600000030090000003000003003ca00a080b9f796566924caf016a02240208000003000800000300c040", 1, 18},
	} {
		nal, _ := hex.DecodeString(c.hex)
		if p, tr, ok := hevcSPSColour(nal); !ok || p != c.prim || tr != c.trc {
			t.Errorf("x265 SPS %s: %d %d %v", c.hex[:12], p, tr, ok)
		}
	}
}

// hevcMKV is a 2160p HEVC Matroska file with the given hvcC, Colour children, Dolby Vision record and first frame.
func hevcMKV(hvcc, colour, dovi, frame []byte) []byte {
	vparts := [][]byte{u(idPixelWidth, 3840), u(idPixelHeight, 2160)}
	if colour != nil {
		vparts = append(vparts, el(idColour, colour))
	}
	parts := [][]byte{u(idTrackNumber, 1), u(idTrackType, 1), str(idCodecID, "V_MPEGH/ISO/HEVC"),
		el(idCodecPrivate, hvcc), el(idVideo, vparts...)}
	if dovi != nil {
		parts = append(parts, el(idBlockAddMapping, u(idBlockAddIDType, 0x64767643), el(idBlockAddIDExtra, dovi)))
	}
	video := el(idTrackEntry, parts...)
	info := el(idInfo, u(idTimestampScale, 1000000), f64(idDuration, 7_200_000))
	cluster := unknownEl(idCluster, u(0xE7, 0), simpleBlock(1, frame))
	return append(el(idEBML, u(0x4286, 1)), unknownEl(idSegment, info, el(idTracks, video), cluster)...)
}

func TestMatroskaColourFromSPS(t *testing.T) {
	pq := testSPS(spsOpts{sets: true, primaries: 9, transfer: 16})
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		// no container colour: the SPS says PQ, the first frame HDR10+
		"sps pq":  {hevcMKV(hvcCWith(pq), nil, nil, hdr10PlusFrame()), "HDR10Plus"},
		"sps hlg": {hevcMKV(hvcCWith(testSPS(spsOpts{primaries: 9, transfer: 18})), nil, nil, hdr10PlusFrame()), "HLG"},
		// the container stays first
		"container sdr": {hevcMKV(hvcCWith(pq), append(u(idTransfer, 1), u(idPrimaries, 1)...), nil, hdr10PlusFrame()), ""},
		// Dolby Vision with an HDR10-compatible base layer and no colour anywhere: the first frame is read
		"dv compat 1": {hevcMKV(testHvcC(), nil, []byte{1, 0, 8 << 1, 0x10<<3 | 0x5, 1 << 4}, hdr10PlusFrame()), "DV HDR10Plus"},
		"no colour":   {hevcMKV(testHvcC(), nil, nil, hdr10PlusFrame()), ""},
	} {
		r, err := Reader(bytes.NewReader(c.data), int64(len(c.data)), 0)
		if err != nil {
			t.Fatal(name, err)
		}
		if got := naming.FormatVideoDynamicRangeType(r.Model(), false); got != c.want {
			t.Errorf("%s: %q, video %+v", name, got, r.Video)
		}
		if r.V != Version {
			t.Errorf("%s: version mark %d", name, r.V)
		}
	}
}

func TestMP4ColourFromSPS(t *testing.T) {
	pq := hvcCWith(testSPS(spsOpts{primaries: 9, transfer: 16}))
	v := &Video{DoviCompat: -1}
	mp4VideoEntry(v, "hvc1", box("hvcC", pq))
	if v.Primaries != "bt2020" || v.Transfer != "smpte2084" {
		t.Errorf("no colr: %+v", v)
	}
	nclx := []byte{'n', 'c', 'l', 'x', 0, 1, 0, 1, 0, 1, 0}
	v = &Video{DoviCompat: -1}
	mp4VideoEntry(v, "hvc1", append(box("hvcC", pq), box("colr", nclx)...))
	if v.Primaries != "bt709" || v.Transfer != "bt709" {
		t.Errorf("colr after hvcC stays first: %+v", v)
	}
}
