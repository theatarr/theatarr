package probe

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

type mp4Box struct {
	typ     string
	dataOff int64
	end     int64
}

func (b *budgetReader) mp4Box(off, limit int64) (mp4Box, error) {
	h, err := b.read(off, 8)
	if err != nil {
		return mp4Box{}, err
	}
	size := int64(binary.BigEndian.Uint32(h))
	typ := string(h[4:8])
	data := off + 8
	switch size {
	case 1:
		x, err := b.read(off+8, 8)
		if err != nil {
			return mp4Box{}, err
		}
		size = int64(binary.BigEndian.Uint64(x))
		data = off + 16
	case 0:
		size = limit - off
	}
	if size < data-off || off+size > limit {
		size = limit - off
	}
	return mp4Box{typ: typ, dataOff: data, end: off + size}, nil
}

// boxes iterates the boxes of an in-memory payload.
func memBoxes(b []byte, f func(typ string, data []byte)) {
	for p := 0; p+8 <= len(b); {
		size := int(binary.BigEndian.Uint32(b[p:]))
		typ := string(b[p+4 : p+8])
		hdr := 8
		if size == 1 && p+16 <= len(b) {
			size = int(binary.BigEndian.Uint64(b[p+8:]))
			hdr = 16
		} else if size == 0 {
			size = len(b) - p
		}
		if size < hdr || p+size > len(b) {
			return
		}
		f(typ, b[p+hdr:p+size])
		p += size
	}
}

func probeMP4(b *budgetReader) (*Result, error) {
	var moov []byte
	for off := int64(0); off < b.size; {
		box, err := b.mp4Box(off, b.size)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if box.typ == "moov" {
			if box.end-box.dataOff > maxMasterBytes*4 {
				return nil, errors.New("probe: moov too large")
			}
			moov, err = b.read(box.dataOff, int(box.end-box.dataOff))
			if err != nil {
				return nil, err
			}
			break
		}
		if box.end <= off {
			break
		}
		off = box.end
	}
	if moov == nil {
		return nil, errors.New("probe: no moov box")
	}
	res := &Result{Container: "mp4"}
	memBoxes(moov, func(typ string, d []byte) {
		switch typ {
		case "mvhd":
			res.RunTimeSeconds = mvhdDuration(d)
		case "udta":
		case "trak":
			mp4Trak(res, d)
		}
	})
	return res, nil
}

func mvhdDuration(d []byte) float64 {
	if len(d) < 20 {
		return 0
	}
	if d[0] == 1 {
		if len(d) < 32 {
			return 0
		}
		ts := binary.BigEndian.Uint32(d[20:])
		dur := binary.BigEndian.Uint64(d[24:])
		if ts == 0 {
			return 0
		}
		return float64(dur) / float64(ts)
	}
	ts := binary.BigEndian.Uint32(d[12:])
	dur := binary.BigEndian.Uint32(d[16:])
	if ts == 0 {
		return 0
	}
	return float64(dur) / float64(ts)
}

func mdhdLanguage(d []byte) string {
	off := 20
	if len(d) > 0 && d[0] == 1 {
		off = 32
	}
	if len(d) < off+2 {
		return ""
	}
	v := binary.BigEndian.Uint16(d[off:])
	if v == 0 || v == 0x7FFF {
		return "und"
	}
	return string([]byte{byte(v>>10&0x1F) + 0x60, byte(v>>5&0x1F) + 0x60, byte(v&0x1F) + 0x60})
}

func mp4Trak(res *Result, trak []byte) {
	var handler, lang string
	var width, height int
	var stsd []byte
	memBoxes(trak, func(typ string, d []byte) {
		switch typ {
		case "tkhd":
			if len(d) >= 84 {
				off := 76
				if d[0] == 1 {
					off = 88
				}
				if len(d) >= off+8 {
					width = int(binary.BigEndian.Uint32(d[off:]) >> 16)
					height = int(binary.BigEndian.Uint32(d[off+4:]) >> 16)
				}
			}
		case "mdia":
			memBoxes(d, func(typ string, d []byte) {
				switch typ {
				case "mdhd":
					lang = mdhdLanguage(d)
				case "hdlr":
					if len(d) >= 12 {
						handler = string(d[8:12])
					}
				case "minf":
					memBoxes(d, func(typ string, d []byte) {
						if typ == "stbl" {
							memBoxes(d, func(typ string, d []byte) {
								if typ == "stsd" {
									stsd = d
								}
							})
						}
					})
				}
			})
		}
	})
	if len(stsd) < 16 {
		return
	}
	entry := stsd[8:] // version/flags, entry count
	if len(entry) < 8 {
		return
	}
	size := int(binary.BigEndian.Uint32(entry))
	if size > len(entry) || size < 8 {
		size = len(entry)
	}
	format := string(entry[4:8])
	body := entry[8:size]
	switch handler {
	case "vide":
		if res.Video != nil {
			return
		}
		v := &Video{CodecID: format, Width: width, Height: height, DoviCompat: -1, BitDepth: 8}
		if len(body) >= 78 {
			v.Width = int(binary.BigEndian.Uint16(body[24:]))
			v.Height = int(binary.BigEndian.Uint16(body[26:]))
			mp4VideoEntry(v, format, body[78:])
		}
		res.Video = v
	case "soun":
		a := Audio{CodecID: format, Language: lang}
		if len(body) >= 28 {
			a.Channels = int(binary.BigEndian.Uint16(body[16:]))
			mp4AudioEntry(&a, format, body[28:])
		}
		res.Audio = append(res.Audio, a)
	case "subt", "sbtl", "text":
		res.Subtitles = append(res.Subtitles, lang)
	}
}

func mp4VideoEntry(v *Video, format string, ext []byte) {
	switch format {
	case "hvc1", "hev1", "dvh1", "dvhe":
		v.Codec = "hevc"
	case "avc1", "avc3", "dva1", "dvav":
		v.Codec = "h264"
	case "av01", "dav1":
		v.Codec = "av1"
	case "mp4v":
		v.Codec = "mpeg4"
	case "vp09":
		v.Codec = "vp9"
	default:
		v.Codec = strings.ToLower(strings.TrimSpace(format))
	}
	memBoxes(ext, func(typ string, d []byte) {
		switch typ {
		case "hvcC":
			if c, ok := parseHvcC(d); ok {
				v.BitDepth = c.bitDepth
				var sf seiFindings
				for _, nal := range c.nals {
					scanHEVCNAL(nal, &sf)
				}
				v.Mastering = v.Mastering || sf.mastering
				v.HDR10Plus = v.HDR10Plus || sf.hdr10Plus
				v.Encoder, v.EncoderRC = sf.encoder, sf.rc
			}
		case "avcC":
			if c, ok := parseAvcC(d); ok {
				v.BitDepth = c.bitDepth
			}
		case "av1C":
			if bd := av1BitDepth(d); bd > 0 {
				v.BitDepth = bd
			}
		case "dvcC", "dvvC", "dvwC":
			if p, c, ok := doviCompat(d); ok {
				v.DoviProfile, v.DoviCompat = p, c
			}
		case "colr":
			if len(d) >= 10 && string(d[:4]) == "nclx" {
				v.Primaries = mkvPrimaries[uint64(binary.BigEndian.Uint16(d[4:]))]
				v.Transfer = mkvTransfer[uint64(binary.BigEndian.Uint16(d[6:]))]
			}
		case "mdcv", "clli", "SmDm", "CoLL":
			v.Mastering = true
		case "st3d":
			v.MultiView = 2
		}
	})
}

func mp4AudioEntry(a *Audio, format string, ext []byte) {
	switch format {
	case "mp4a":
		a.Codec = "aac"
	case "ac-3":
		a.Codec = "ac3"
	case "ec-3":
		a.Codec = "eac3"
	case "mlpa":
		a.Codec = "truehd"
	case "dtsc", "dtsh", "dtsl", "dtse", "dtsx", "dtsy":
		a.Codec = "dts"
		a.Profile = map[string]string{"dtsc": "DTS", "dtsh": "DTS-HD HRA", "dtsl": "DTS-HD MA", "dtse": "DTS Express",
			"dtsx": "DTS:X", "dtsy": "DTS:X"}[format]
	case "Opus":
		a.Codec = "opus"
	case "fLaC":
		a.Codec = "flac"
	case ".mp3":
		a.Codec = "mp3"
	case "lpcm", "sowt", "twos", "ipcm":
		a.Codec = "pcm_s16le"
	default:
		a.Codec = strings.ToLower(strings.TrimSpace(format))
	}
	memBoxes(ext, func(typ string, d []byte) {
		switch typ {
		case "dec3":
			// data_rate(13) num_ind_sub(3), then per substream 3 bytes (+1 with dependents); a following byte holds
			// flag_ec3_extension_type_a in its least significant bit.
			if len(d) < 2 {
				return
			}
			n := int(d[1]&7) + 1
			p := 2
			for i := 0; i < n && p+3 <= len(d); i++ {
				deps := int(d[p+2]>>1) & 0xF
				p += 3
				if deps > 0 {
					p++
				}
			}
			if p < len(d) && d[p]&1 == 1 {
				a.Tag = "ec+3"
			}
		case "esds":
			if strings.Contains(string(d), "\x05") && a.Codec == "aac" {
				// Object type 5 (SBR) in the AudioSpecificConfig: HE-AAC.
				if asc := audioSpecificConfig(d); len(asc) > 0 && asc[0]>>3 == 5 {
					a.Tag = "A_AAC/MPEG4/LC/SBR"
				}
			}
		}
	})
}

// audioSpecificConfig finds the DecoderSpecificInfo (tag 5) payload in an esds box.
func audioSpecificConfig(d []byte) []byte {
	for i := 4; i < len(d)-2; i++ {
		if d[i] != 5 {
			continue
		}
		p := i + 1
		l := 0
		for k := 0; k < 4 && p < len(d); k++ {
			c := d[p]
			p++
			l = l<<7 | int(c&0x7F)
			if c&0x80 == 0 {
				break
			}
		}
		if l > 0 && p+l <= len(d) {
			return d[p : p+l]
		}
	}
	return nil
}
