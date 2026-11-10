package probe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

// Matroska element ids.
const (
	idEBML            = 0x1A45DFA3
	idSegment         = 0x18538067
	idSeekHead        = 0x114D9B74
	idSeek            = 0x4DBB
	idSeekID          = 0x53AB
	idSeekPosition    = 0x53AC
	idInfo            = 0x1549A966
	idTimestampScale  = 0x2AD7B1
	idDuration        = 0x4489
	idTitle           = 0x7BA9
	idTracks          = 0x1654AE6B
	idTrackEntry      = 0xAE
	idTrackNumber     = 0xD7
	idTrackType       = 0x83
	idCodecID         = 0x86
	idCodecPrivate    = 0x63A2
	idLanguage        = 0x22B59C
	idFlagEnabled     = 0xB9
	idVideo           = 0xE0
	idPixelWidth      = 0xB0
	idPixelHeight     = 0xBA
	idStereoMode      = 0x53B8
	idColour          = 0x55B0
	idBitsPerChannel  = 0x55B2
	idTransfer        = 0x55BA
	idPrimaries       = 0x55BB
	idMaxCLL          = 0x55BC
	idMaxFALL         = 0x55BD
	idMastering       = 0x55D0
	idAudio           = 0xE1
	idChannels        = 0x9F
	idBlockAddMapping = 0x41E4
	idBlockAddIDType  = 0x41E7
	idBlockAddIDExtra = 0x41ED
	idContentEncs     = 0x6D80
	idContentEnc      = 0x6240
	idContentComp     = 0x5034
	idContentCompAlgo = 0x4254
	idContentCompSet  = 0x4255
	idCluster         = 0x1F43B675
	idSimpleBlock     = 0xA3
	idBlockGroup      = 0xA0
	idBlock           = 0xA1
)

const unknownSize = -1

// readVint reads an EBML variable-size integer at off: its value (marker removed unless keepMarker) and length.
func readVint(b []byte, keepMarker bool) (int64, int, error) {
	if len(b) == 0 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	first := b[0]
	l := 1
	for mask := byte(0x80); l <= 8 && first&mask == 0; mask >>= 1 {
		l++
	}
	if l > 8 {
		return 0, 0, errors.New("probe: bad EBML vint")
	}
	if len(b) < l {
		return 0, 0, io.ErrUnexpectedEOF
	}
	v := int64(first)
	if !keepMarker {
		v &= int64(0xFF >> uint(l))
	}
	allOnes := v == int64(0xFF>>uint(l))
	for i := 1; i < l; i++ {
		v = v<<8 | int64(b[i])
		if b[i] != 0xFF {
			allOnes = false
		}
	}
	if !keepMarker && allOnes {
		return unknownSize, l, nil
	}
	return v, l, nil
}

type element struct {
	id         int64
	dataOff    int64
	size       int64 // unknownSize for an unknown-size element
	headerSize int
}

// readElement reads an element header at off.
func (b *budgetReader) readElement(off int64) (element, error) {
	n := 12
	if off+int64(n) > b.size {
		n = int(b.size - off)
	}
	if n <= 0 {
		return element{}, io.EOF
	}
	h, err := b.read(off, n)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return element{}, err
	}
	id, il, err := readVint(h, true)
	if err != nil {
		return element{}, err
	}
	size, sl, err := readVint(h[il:], false)
	if err != nil {
		return element{}, err
	}
	return element{id: id, dataOff: off + int64(il+sl), size: size, headerSize: il + sl}, nil
}

func (e element) end(parentEnd int64) int64 {
	if e.size == unknownSize || e.dataOff+e.size > parentEnd {
		return parentEnd
	}
	return e.dataOff + e.size
}

// children parses the children of [start, end) in memory (for small master elements).
func children(b []byte, f func(id int64, data []byte) error) error {
	for p := 0; p < len(b); {
		id, il, err := readVint(b[p:], true)
		if err != nil {
			return err
		}
		size, sl, err := readVint(b[p+il:], false)
		if err != nil {
			return err
		}
		p += il + sl
		if size == unknownSize || p+int(size) > len(b) || size < 0 {
			size = int64(len(b) - p)
		}
		if err := f(id, b[p:p+int(size)]); err != nil {
			return err
		}
		p += int(size)
	}
	return nil
}

func uintOf(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func floatOf(b []byte) float64 {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	}
	return 0
}

type mkvTrack struct {
	number    uint64
	typ       uint64
	codecID   string
	private   []byte
	language  string
	enabled   bool
	width     int
	height    int
	stereo    uint64
	bits      int
	transfer  uint64
	primaries uint64
	mastering bool
	channels  int
	dovi      []byte
	strip     []byte // header stripping bytes
}

var mkvTransfer = map[uint64]string{1: "bt709", 16: "smpte2084", 18: "arib-std-b67", 14: "bt2020-10", 15: "bt2020-12"}
var mkvPrimaries = map[uint64]string{1: "bt709", 9: "bt2020", 5: "bt470bg", 6: "smpte170m"}

func parseTrack(data []byte) (*mkvTrack, error) {
	t := &mkvTrack{language: "eng", enabled: true}
	err := children(data, func(id int64, d []byte) error {
		switch id {
		case idTrackNumber:
			t.number = uintOf(d)
		case idTrackType:
			t.typ = uintOf(d)
		case idCodecID:
			t.codecID = strings.TrimRight(string(d), "\x00")
		case idCodecPrivate:
			t.private = d
		case idLanguage:
			t.language = strings.TrimRight(string(d), "\x00")
		case idFlagEnabled:
			t.enabled = uintOf(d) != 0
		case idVideo:
			return children(d, func(id int64, d []byte) error {
				switch id {
				case idPixelWidth:
					t.width = int(uintOf(d))
				case idPixelHeight:
					t.height = int(uintOf(d))
				case idStereoMode:
					t.stereo = uintOf(d)
				case idColour:
					return children(d, func(id int64, d []byte) error {
						switch id {
						case idBitsPerChannel:
							t.bits = int(uintOf(d))
						case idTransfer:
							t.transfer = uintOf(d)
						case idPrimaries:
							t.primaries = uintOf(d)
						case idMastering, idMaxCLL, idMaxFALL:
							t.mastering = true
						}
						return nil
					})
				}
				return nil
			})
		case idAudio:
			return children(d, func(id int64, d []byte) error {
				if id == idChannels {
					t.channels = int(uintOf(d))
				}
				return nil
			})
		case idBlockAddMapping:
			var typ uint64
			var extra []byte
			_ = children(d, func(id int64, d []byte) error {
				switch id {
				case idBlockAddIDType:
					typ = uintOf(d)
				case idBlockAddIDExtra:
					extra = d
				}
				return nil
			})
			if typ == 0x64766343 || typ == 0x64767643 || typ == 0x64767743 { // dvcC, dvvC, dvwC
				t.dovi = extra
			}
		case idContentEncs:
			return children(d, func(id int64, d []byte) error {
				if id != idContentEnc {
					return nil
				}
				return children(d, func(id int64, d []byte) error {
					if id != idContentComp {
						return nil
					}
					algo := uint64(0)
					var set []byte
					_ = children(d, func(id int64, d []byte) error {
						switch id {
						case idContentCompAlgo:
							algo = uintOf(d)
						case idContentCompSet:
							set = d
						}
						return nil
					})
					if algo == 3 {
						t.strip = set
					}
					return nil
				})
			})
		}
		return nil
	})
	return t, err
}

// maxMasterBytes caps a master element read into memory (Info, Tracks).
const maxMasterBytes = 4 << 20

func probeMatroska(b *budgetReader) (*Result, error) {
	hdr, err := b.readElement(0)
	if err != nil {
		return nil, err
	}
	if hdr.id != idEBML {
		return nil, ErrUnknownFormat
	}
	seg, err := b.readElement(hdr.end(b.size))
	if err != nil {
		return nil, err
	}
	if seg.id != idSegment {
		return nil, fmt.Errorf("probe: no Matroska segment")
	}
	segEnd := seg.end(b.size)
	res := &Result{Container: "matroska"}
	scale := uint64(1000000)
	var duration float64
	var tracks []*mkvTrack
	var tracksPos, infoPos int64 = -1, -1
	haveInfo, haveTracks := false, false

	readMaster := func(e element) ([]byte, error) {
		n := e.end(segEnd) - e.dataOff
		if n > maxMasterBytes {
			return nil, fmt.Errorf("probe: element %x too large", e.id)
		}
		return b.read(e.dataOff, int(n))
	}
	parseInfo := func(d []byte) {
		_ = children(d, func(id int64, d []byte) error {
			switch id {
			case idTimestampScale:
				scale = uintOf(d)
			case idDuration:
				duration = floatOf(d)
			case idTitle:
				res.Title = string(d)
			}
			return nil
		})
		haveInfo = true
	}
	parseTracks := func(d []byte) error {
		haveTracks = true
		return children(d, func(id int64, d []byte) error {
			if id == idTrackEntry {
				t, err := parseTrack(d)
				if err != nil {
					return err
				}
				tracks = append(tracks, t)
			}
			return nil
		})
	}

	// headPos is where a SeekHead entry points at another SeekHead (a chained index), -1 if none.
	var headPos int64 = -1
	parseSeekHead := func(d []byte) {
		_ = children(d, func(id int64, d []byte) error {
			if id != idSeek {
				return nil
			}
			var sid int64
			var pos int64 = -1
			_ = children(d, func(id int64, d []byte) error {
				switch id {
				case idSeekID:
					sid = int64(uintOf(d))
				case idSeekPosition:
					pos = int64(uintOf(d))
				}
				return nil
			})
			switch sid {
			case idTracks:
				tracksPos = seg.dataOff + pos
			case idInfo:
				infoPos = seg.dataOff + pos
			case idSeekHead:
				if headPos < 0 && pos >= 0 {
					headPos = seg.dataOff + pos
				}
			}
			return nil
		})
	}

	firstCluster := int64(-1)
	for off := seg.dataOff; off < segEnd; {
		e, err := b.readElement(off)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		switch e.id {
		case idSeekHead:
			d, err := readMaster(e)
			if err != nil {
				return nil, err
			}
			parseSeekHead(d)
		case idInfo:
			d, err := readMaster(e)
			if err != nil {
				return nil, err
			}
			parseInfo(d)
		case idTracks:
			d, err := readMaster(e)
			if err != nil {
				return nil, err
			}
			if err := parseTracks(d); err != nil {
				return nil, err
			}
		case idCluster:
			if firstCluster < 0 {
				firstCluster = off
			}
		}
		if e.id == idCluster || e.size == unknownSize {
			break
		}
		off = e.end(segEnd)
		if haveInfo && haveTracks {
			// Keep walking only to find the first cluster.
			continue
		}
	}
	// A SeekHead that points at a second SeekHead (often at the end of the file) for Tracks or Info: read that one
	// once. SeekHead entries inside it are not followed further.
	if (!haveTracks && tracksPos < 0 || !haveInfo && infoPos < 0) && headPos >= 0 && headPos < segEnd {
		if e, err := b.readElement(headPos); err == nil && e.id == idSeekHead {
			if d, err := readMaster(e); err == nil {
				parseSeekHead(d)
			}
		}
	}
	// Info or Tracks after the clusters: follow the SeekHead.
	for _, x := range []struct {
		pos  int64
		have *bool
		f    func([]byte) error
	}{{infoPos, &haveInfo, func(d []byte) error { parseInfo(d); return nil }}, {tracksPos, &haveTracks, parseTracks}} {
		if *x.have || x.pos < 0 || x.pos >= segEnd {
			continue
		}
		e, err := b.readElement(x.pos)
		if err != nil {
			return nil, err
		}
		d, err := readMaster(e)
		if err != nil {
			return nil, err
		}
		if err := x.f(d); err != nil {
			return nil, err
		}
	}
	if !haveTracks {
		return nil, errors.New("probe: no Matroska Tracks element")
	}
	res.RunTimeSeconds = duration * float64(scale) / 1e9

	var video, audio *mkvTrack
	for _, t := range tracks {
		switch t.typ {
		case 1:
			if video == nil && t.codecID != "V_MJPEG" && !strings.HasPrefix(t.codecID, "V_PNG") {
				video = t
			}
		case 2:
			if audio == nil {
				audio = t
			}
			res.Audio = append(res.Audio, mkvAudio(t))
		case 17:
			res.Subtitles = append(res.Subtitles, t.language)
		}
	}
	if video != nil {
		res.Video = mkvVideo(video)
	}
	// First frames: the audio codec's profile and Atmos marks, and the video's SEI: HDR metadata when it is PQ, the
	// encoder's settings for any H.264 or HEVC stream.
	hevc := res.Video != nil && video.codecID == "V_MPEGH/ISO/HEVC"
	avc := res.Video != nil && video.codecID == "V_MPEG4/ISO/AVC"
	pq := hevc && res.Video.Transfer == "smpte2084"
	needVideo := hevc || avc
	needAudio := audio != nil && (audio.codecID == "A_TRUEHD" || audio.codecID == "A_EAC3" || strings.HasPrefix(audio.codecID, "A_DTS"))
	if (needVideo || needAudio) && firstCluster > 0 {
		videoMax := int64(64 << 10)
		if pq {
			videoMax = 4 << 20
		}
		frames := firstFrames(b, firstCluster, segEnd, video, audio, needVideo, needAudio, videoMax)
		if f, ok := frames[audioKey]; ok && len(res.Audio) > 0 {
			applyAudioFrame(&res.Audio[0], audio.codecID, append(append([]byte(nil), audio.strip...), f...))
		}
		if f, ok := frames[videoKey]; ok {
			var sf seiFindings
			if hevc {
				cfg, _ := parseHvcC(video.private)
				for _, nal := range cfg.nals {
					scanHEVCNAL(nal, &sf)
				}
				scanHEVCFrame(f, max(cfg.lengthSize, 1), &sf)
			} else {
				cfg, _ := parseAvcC(video.private)
				scanAVCFrame(f, max(cfg.lengthSize, 1), &sf)
			}
			if pq {
				res.Video.HDR10Plus = sf.hdr10Plus
				res.Video.Mastering = res.Video.Mastering || sf.mastering
				res.Video.FrameScanned = true
			}
			if sf.encoder != "" {
				res.Video.Encoder, res.Video.EncoderRC = sf.encoder, sf.rc
			}
		}
	}
	return res, nil
}

const (
	videoKey = "video"
	audioKey = "audio"
)

// firstFrames reads clusters from start until it has the first block of each wanted track, or the budget ends (a
// budget error then just ends the scan).
func firstFrames(b *budgetReader, start, segEnd int64, video, audio *mkvTrack, needVideo, needAudio bool, videoMax int64) map[string][]byte {
	out := map[string][]byte{}
	want := func() bool { return (needVideo && out[videoKey] == nil) || (needAudio && out[audioKey] == nil) }
	const maxBlock = 4 << 20
	for off := start; off < segEnd && want(); {
		cl, err := b.readElement(off)
		if err != nil || cl.id != idCluster {
			return out
		}
		clEnd := cl.end(segEnd)
		for p := cl.dataOff; p < clEnd && want(); {
			e, err := b.readElement(p)
			if err != nil {
				return out
			}
			if e.id == idCluster { // unknown-size cluster ended
				clEnd = p
				break
			}
			var blockOff, blockLen int64 = -1, 0
			switch e.id {
			case idSimpleBlock:
				blockOff, blockLen = e.dataOff, e.end(clEnd)-e.dataOff
			case idBlockGroup:
				// Find the Block inside.
				for q := e.dataOff; q < e.end(clEnd); {
					be, err := b.readElement(q)
					if err != nil {
						break
					}
					if be.id == idBlock {
						blockOff, blockLen = be.dataOff, be.end(e.end(clEnd))-be.dataOff
						break
					}
					q = be.end(e.end(clEnd))
				}
			}
			if blockOff >= 0 && blockLen > 4 {
				h, err := b.read(blockOff, 8)
				if err != nil {
					return out
				}
				tn, tl, err := readVint(h, false)
				if err == nil {
					key := ""
					switch {
					case video != nil && uint64(tn) == video.number && needVideo && out[videoKey] == nil:
						key = videoKey
					case audio != nil && uint64(tn) == audio.number && needAudio && out[audioKey] == nil:
						key = audioKey
					}
					if key != "" {
						n := min(blockLen-int64(tl+3), maxBlock)
						if key == audioKey {
							n = min(n, 64<<10)
						} else {
							n = min(n, videoMax)
						}
						if d, err := b.read(blockOff+int64(tl+3), int(n)); err == nil {
							out[key] = d
						} else {
							return out
						}
					}
				}
			}
			if e.size == unknownSize {
				return out
			}
			p = e.end(clEnd)
		}
		off = clEnd
	}
	return out
}

func mkvVideo(t *mkvTrack) *Video {
	v := &Video{CodecID: t.codecID, Width: t.width, Height: t.height, BitDepth: t.bits, DoviCompat: -1,
		Transfer: mkvTransfer[t.transfer], Primaries: mkvPrimaries[t.primaries], Mastering: t.mastering}
	if t.stereo > 0 {
		v.MultiView = 2
	}
	switch {
	case t.codecID == "V_MPEGH/ISO/HEVC":
		v.Codec = "hevc"
		if c, ok := parseHvcC(t.private); ok {
			v.BitDepth = c.bitDepth
			var sf seiFindings
			for _, nal := range c.nals {
				scanHEVCNAL(nal, &sf)
			}
			v.Encoder, v.EncoderRC = sf.encoder, sf.rc
		}
	case t.codecID == "V_MPEG4/ISO/AVC":
		v.Codec = "h264"
		if c, ok := parseAvcC(t.private); ok {
			v.BitDepth = c.bitDepth
		}
	case t.codecID == "V_AV1":
		v.Codec = "av1"
		if d := av1BitDepth(t.private); d > 0 {
			v.BitDepth = d
		}
	case t.codecID == "V_MPEG2" || t.codecID == "V_MPEG1":
		v.Codec = map[string]string{"V_MPEG2": "mpeg2video", "V_MPEG1": "mpeg1video"}[t.codecID]
	case t.codecID == "V_VP9" || t.codecID == "V_VP8":
		v.Codec = strings.ToLower(t.codecID[2:])
	case strings.HasPrefix(t.codecID, "V_MPEG4/ISO/"):
		v.Codec = "mpeg4"
	case t.codecID == "V_MS/VFW/FOURCC" && len(t.private) >= 20:
		fourcc := string(t.private[16:20])
		v.Tag = strings.ToUpper(fourcc)
		switch strings.ToUpper(fourcc) {
		case "WVC1", "WMV3":
			v.Codec = map[string]string{"WVC1": "vc1", "WMV3": "wmv3"}[strings.ToUpper(fourcc)]
		case "XVID", "DIVX", "DX50", "DIV3", "MP4V", "FMP4":
			v.Codec = "mpeg4"
		case "H264", "AVC1":
			v.Codec = "h264"
		}
	case strings.HasPrefix(t.codecID, "V_REAL/"):
		v.Codec = strings.ToLower(t.codecID[7:])
	default:
		v.Codec = strings.ToLower(strings.TrimPrefix(t.codecID, "V_"))
	}
	if t.dovi != nil {
		if p, c, ok := doviCompat(t.dovi); ok {
			v.DoviProfile, v.DoviCompat = p, c
		}
	}
	if v.BitDepth == 0 {
		v.BitDepth = 8
	}
	return v
}

func mkvAudio(t *mkvTrack) Audio {
	a := Audio{CodecID: t.codecID, Channels: t.channels, Language: t.language}
	switch {
	case t.codecID == "A_TRUEHD":
		a.Codec = "truehd"
	case t.codecID == "A_EAC3":
		a.Codec = "eac3"
	case t.codecID == "A_AC3" || strings.HasPrefix(t.codecID, "A_AC3/"):
		a.Codec = "ac3"
	case strings.HasPrefix(t.codecID, "A_DTS"):
		a.Codec = "dts"
		a.Profile = "DTS"
	case strings.HasPrefix(t.codecID, "A_AAC"):
		a.Codec = "aac"
		if t.codecID == "A_AAC/MPEG4/LC/SBR" {
			a.Tag = t.codecID
		}
	case t.codecID == "A_FLAC":
		a.Codec = "flac"
	case t.codecID == "A_OPUS":
		a.Codec = "opus"
	case t.codecID == "A_VORBIS":
		a.Codec = "vorbis"
	case t.codecID == "A_MPEG/L3":
		a.Codec = "mp3"
	case t.codecID == "A_MPEG/L2":
		a.Codec = "mp2"
	case strings.HasPrefix(t.codecID, "A_PCM/"):
		a.Codec = "pcm_" + strings.ToLower(strings.ReplaceAll(t.codecID[6:], "/", "_"))
	default:
		a.Codec = strings.ToLower(strings.TrimPrefix(t.codecID, "A_"))
	}
	return a
}

// applyAudioFrame reads what the first audio block says about the codec's profile and Atmos.
func applyAudioFrame(a *Audio, codecID string, frame []byte) {
	switch {
	case codecID == "A_TRUEHD":
		if atmos, ok := truehdAtmos(frame); ok && atmos {
			a.Tag = "thd+"
		}
	case codecID == "A_EAC3":
		if atmos, ok := eac3Atmos(frame); ok && atmos {
			a.Tag = "ec+3"
		}
	case strings.HasPrefix(codecID, "A_DTS"):
		a.Profile = dtsProfile(frame)
	}
}
