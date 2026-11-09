package probe

import (
	"bytes"
	"encoding/binary"
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
	b[19] = 0xF8 | 2 // bitDepthLumaMinus8 = 2
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
	dec3 := []byte{0, 0, 0, 0, 0, 0x01} // one substream, no dependents, then flag_ec3_extension_type_a
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
