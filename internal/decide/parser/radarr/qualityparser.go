// Port of Radarr src/NzbDrone.Core/Parser/QualityParser.cs @ a96bf7c.

package radarr

import (
	"strconv"
	"strings"
)

var (
	sourceRegex = re(`\b(?:
                                                            (?<bluray>M?Blu[-_. ]?Ray|HD[-_. ]?DVD|BD(?!$)|UHD2?BD|BDISO|BDMux|BD25|BD50|BR[-_. ]?DISK)|
                                                            (?<webdl>WEB[-_. ]?DL(?:mux)?|AmazonHD|AmazonSD|iTunesHD|MaxdomeHD|NetflixU?HD|WebHD|HBOMaxHD|DisneyHD|[. ]WEB[. ](?:[xh][ .]?26[45]|AVC|HEVC|DDP?5[. ]1)|[. ](?-i:WEB)$|(?:\d{3,4}0p)[-. ](?:Hybrid[-_. ]?)?WEB[-. ]|[-. ]WEB[-. ]\d{3,4}0p|\b\s\/\sWEB\s\/\s\b|(?:AMZN|NF|DP)[. -]WEB[. -](?!Rip))|
                                                            (?<webrip>WebRip|Web-Rip|WEBMux)|
                                                            (?<hdtv>HDTV)|
                                                            (?<bdrip>BDRip|BDLight|HD[-_. ]?DVDRip|UHDBDRip)|
                                                            (?<brrip>BRRip)|
                                                            (?<dvdr>\d?x?M?DVD-?[R59])|
                                                            (?<dvd>DVD(?!-R)|DVDRip|xvidvd)|
                                                            (?<dsr>WS[-_. ]DSR|DSR)|
                                                            (?<regional>R[0-9]{1}|REGIONAL)|
                                                            (?<scr>SCR|SCREENER|DVDSCR|DVDSCREENER)|
                                                            (?<ts>TS[-_. ]|TELESYNCH?|HD-TS|HDTS|PDVD|TSRip|HDTSRip)|
                                                            (?<tc>TC|TELECINE|HD-TC|HDTC)|
                                                            (?<cam>CAMRIP|(?:NEW)?CAM|HD-?CAM(?:Rip)?|HQCAM)|
                                                            (?<wp>WORKPRINT|WP)|
                                                            (?<pdtv>PDTV)|
                                                            (?<sdtv>SDTV)|
                                                            (?<tvrip>TVRip)
                                                            )(?:\b|$|[ .])`, optI|optX)

	rawHDRegex = re(`\b(?<rawhd>RawHD|Raw[-_. ]HD)\b`, optI)

	mpeg2Regex = re(`\b(?<mpeg2>MPEG[-_. ]?2)\b`, 0)

	brDiskRegex = re(`^(?!.*\b((?<!HD[._ -]|HD)DVD|BDRip|720p|MKV|XviD|WMV|d3g|(BD)?REMUX|^(?=.*1080p)(?=.*HEVC)|[xh][-_. ]?26[45]|German.*[DM]L|((?<=\d{4}).*German.*([DM]L)?)(?=.*\b(AVC|HEVC|VC[-_. ]?1|MVC|MPEG[-_. ]?2)\b))\b)(((?=.*\b(Blu[-_. ]?ray|BD|HD[-_. ]?DVD)\b)(?=.*\b(AVC|HEVC|VC[-_. ]?1|MVC|MPEG[-_. ]?2|BDMV|ISO)\b))|^((?=.*\b(((?=.*\b((.*_)?COMPLETE.*|Dis[ck])\b)(?=.*(Blu[-_. ]?ray|HD[-_. ]?DVD)))|3D[-_. ]?BD|BR[-_. ]?DISK|Full[-_. ]?Blu[-_. ]?ray|^((?=.*((BD|UHD)[-_. ]?(25|50|66|100|ISO)))))))).*`, optI)

	properRegex = re(`\b(?<proper>proper)\b`, optI)

	repackRegex = re(`\b(?<repack>repack\d?|rerip\d?)\b`, optI)

	versionRegex = re(`\d[-._ ]?v(?<version>\d)[-._ ]|\[v(?<version>\d)\]|repack(?<version>\d)|rerip(?<version>\d)`, optI)

	realRegex = re(`\b(?<real>REAL)\b`, 0)

	resolutionRegex = re(`\b(?:(?<R360p>360p)|(?<R480p>480p|480i|640x480|848x480)|(?<R540p>540p)|(?<R576p>576p)|(?<R720p>720p|1280x720|960p)|(?<R1080p>1080p|1920x1080|1440p|FHD|1080i|4kto1080p)|(?<R2160p>2160p|3840x2160|4k[-_. ](?:UHD|HEVC|BD|H\.?265)|(?:UHD|HEVC|BD|H\.?265)[-_. ]4k))\b`, optI)

	// Handle cases where no resolution is in the release name (assume if UHD then 4k) or resolution is non-standard
	alternativeResolutionRegex = re(`\b(?<R2160p>UHD)\b|(?<R2160p>\[4K\])`, optI)

	codecRegex = re(`\b(?:(?<x264>x264)|(?<h264>h264)|(?<xvidhd>XvidHD)|(?<xvid>X-?vid)|(?<divx>divx))\b`, optI)

	otherSourceRegex = re(`(?<hdtv>HD[-_. ]TV)|(?<sdtv>SD[-_. ]TV)`, optI)

	animeBlurayRegex = re(`bd(?:720|1080|2160)|(?<=[-_. (\[])bd(?=[-_. )\]])`, optI)
	animeWebDlRegex  = re(`\[WEB\]|[\[\(]WEB[ .]`, optI)

	highDefPdtvRegex = re(`hr[-_. ]ws`, optI)

	remuxRegex       = re(`(?:[_. \[]|\d{4}p-|\bHybrid-)(?<remux>(?:(BD|UHD)[-_. ]?)?Remux)\b|(?<remux>(?:(BD|UHD)[-_. ]?)?Remux[_. ]\d{4}p)`, optI)
	germanRemuxRegex = re(`((?<=\d{4}).*German.*([DM]L)?)(?=.*\b(AVC|HEVC|VC[_. -]?1|MVC|MPEG[_. -]?2))(?=.*Blu-?ray)`, optI)
)

// Resolution is QualityParser's Resolution enum; its value is the resolution in lines.
type Resolution int

const (
	ResolutionUnknown Resolution = 0
	R360p             Resolution = 360
	R480p             Resolution = 480
	R540p             Resolution = 540
	R576p             Resolution = 576
	R720p             Resolution = 720
	R1080p            Resolution = 1080
	R2160p            Resolution = 2160
)

// ParseQuality is QualityParser.ParseQuality.
func ParseQuality(name string) QualityModel {
	if isNullOrWhiteSpace(name) {
		return newQualityModel()
	}

	name = netTrim(name)

	result := ParseQualityName(name)

	// Based on extension
	if result.Quality.ID == QualityUnknown.ID && !containsInvalidPathChars(name) {
		result.Quality = qualityForExtension(pathGetExtension(name))
		result.SourceDetectionSource = DetectionExtension
		result.ResolutionDetectionSource = DetectionExtension
		result.ModifierDetectionSource = DetectionExtension
	}

	return result
}

// ParseQualityName is QualityParser.ParseQualityName.
func ParseQualityName(name string) QualityModel {
	normalizedName := netTrim(strings.ReplaceAll(name, "_", " "))
	result := parseQualityModifiers(name, normalizedName)

	sourceMatches := matches(sourceRegex, normalizedName)
	var sourceMatch = lastMatch(sourceMatches)
	resolution := parseResolution(normalizedName)
	codec := match(codecRegex, normalizedName)
	remuxMatch := isMatch(remuxRegex, normalizedName) || isMatch(germanRemuxRegex, normalizedName)
	brDiskMatch := isMatch(brDiskRegex, normalizedName)

	if isMatch(rawHDRegex, normalizedName) && !brDiskMatch {
		result.SourceDetectionSource = DetectionName
		result.ResolutionDetectionSource = DetectionName
		result.Quality = QualityRAWHD
		return result
	}

	if resolution != ResolutionUnknown {
		result.ResolutionDetectionSource = DetectionName
	}

	if sourceMatch != nil {
		result.SourceDetectionSource = DetectionName

		if ok(sourceMatch, "bluray") {
			if brDiskMatch {
				result.Quality = QualityBRDISK
				return result
			}

			if ok(codec, "xvid") || ok(codec, "divx") {
				result.Quality = QualityBluray480p
				return result
			}

			if resolution == R2160p {
				result.Quality = pick(remuxMatch, QualityRemux2160p, QualityBluray2160p)
				return result
			}

			if resolution == R1080p {
				result.Quality = pick(remuxMatch, QualityRemux1080p, QualityBluray1080p)
				return result
			}

			if resolution == R720p {
				result.Quality = QualityBluray720p
				return result
			}

			if resolution == R576p {
				result.Quality = QualityBluray576p
				return result
			}

			if resolution == R360p || resolution == R480p || resolution == R540p {
				result.Quality = QualityBluray480p
				return result
			}

			// Treat a remux without a source as 1080p, not 720p.
			// 720p remux should fallback as 720p BluRay
			if remuxMatch && resolution != R720p {
				result.Quality = QualityRemux1080p
				return result
			}

			result.Quality = QualityBluray720p
			return result
		}

		if ok(sourceMatch, "webdl") {
			switch {
			case resolution == R2160p:
				result.Quality = QualityWEBDL2160p
			case resolution == R1080p:
				result.Quality = QualityWEBDL1080p
			case resolution == R720p:
				result.Quality = QualityWEBDL720p
			case strings.Contains(name, "[WEBDL]"):
				result.Quality = QualityWEBDL720p
			default:
				result.Quality = QualityWEBDL480p
			}
			return result
		}

		if ok(sourceMatch, "webrip") {
			switch resolution {
			case R2160p:
				result.Quality = QualityWEBRip2160p
			case R1080p:
				result.Quality = QualityWEBRip1080p
			case R720p:
				result.Quality = QualityWEBRip720p
			default:
				result.Quality = QualityWEBRip480p
			}
			return result
		}

		if ok(sourceMatch, "scr") {
			result.Quality = QualityDVDSCR
			return result
		}

		if ok(sourceMatch, "cam") {
			result.Quality = QualityCAM
			return result
		}

		if ok(sourceMatch, "ts") {
			result.Quality = QualityTELESYNC
			result.Quality.Resolution = int(resolution)
			return result
		}

		if ok(sourceMatch, "tc") {
			result.Quality = QualityTELECINE
			return result
		}

		if ok(sourceMatch, "wp") {
			result.Quality = QualityWORKPRINT
			return result
		}

		if ok(sourceMatch, "regional") {
			result.Quality = QualityREGIONAL
			return result
		}

		if ok(sourceMatch, "hdtv") {
			switch {
			case isMatch(mpeg2Regex, normalizedName):
				result.Quality = QualityRAWHD
			case resolution == R2160p:
				result.Quality = QualityHDTV2160p
			case resolution == R1080p:
				result.Quality = QualityHDTV1080p
			case resolution == R720p:
				result.Quality = QualityHDTV720p
			case strings.Contains(name, "[HDTV]"):
				result.Quality = QualityHDTV720p
			default:
				result.Quality = QualitySDTV
			}
			return result
		}

		if ok(sourceMatch, "bdrip") || ok(sourceMatch, "brrip") {
			switch resolution {
			case R720p:
				result.Quality = QualityBluray720p
			case R1080p:
				result.Quality = QualityBluray1080p
			case R2160p:
				result.Quality = QualityBluray2160p
			case R576p:
				result.Quality = QualityBluray576p
			default:
				result.Quality = QualityBluray480p
			}
			return result
		}

		if ok(sourceMatch, "dvdr") {
			result.Quality = QualityDVDR
			return result
		}

		if ok(sourceMatch, "dvd") {
			result.Quality = QualityDVD
			return result
		}

		if ok(sourceMatch, "pdtv") || ok(sourceMatch, "sdtv") || ok(sourceMatch, "dsr") || ok(sourceMatch, "tvrip") {
			if resolution == R1080p || containsIgnoreCase(normalizedName, "1080p") {
				result.Quality = QualityHDTV1080p
				return result
			}

			if resolution == R720p || containsIgnoreCase(normalizedName, "720p") {
				result.Quality = QualityHDTV720p
				return result
			}

			if isMatch(highDefPdtvRegex, normalizedName) {
				result.ResolutionDetectionSource = DetectionName
				result.Quality = QualityHDTV720p
				return result
			}

			result.Quality = QualitySDTV
			return result
		}
	}

	if sourceMatch == nil && remuxMatch && resolution != ResolutionUnknown {
		result.SourceDetectionSource = DetectionUnknown

		switch resolution {
		case R480p:
			result.Quality = QualityBluray480p
			return result
		case R720p:
			result.Quality = QualityBluray720p
			return result
		case R2160p:
			result.Quality = QualityRemux2160p
			return result
		case R1080p:
			result.Quality = QualityRemux1080p
			return result
		}
	}

	// Anime Bluray matching
	if isMatch(animeBlurayRegex, normalizedName) {
		result.SourceDetectionSource = DetectionName

		if resolution == R360p || resolution == R480p || resolution == R540p || resolution == R576p ||
			containsIgnoreCase(normalizedName, "480p") {
			result.ResolutionDetectionSource = DetectionName
			result.Quality = QualityDVD
			return result
		}

		if resolution == R1080p || containsIgnoreCase(normalizedName, "1080p") {
			result.ResolutionDetectionSource = DetectionName
			result.Quality = pick(remuxMatch, QualityRemux1080p, QualityBluray1080p)
			return result
		}

		if resolution == R2160p || containsIgnoreCase(normalizedName, "2160p") {
			result.ResolutionDetectionSource = DetectionName
			result.Quality = pick(remuxMatch, QualityRemux2160p, QualityBluray2160p)
			return result
		}

		// Treat a remux without a source as 1080p, not 720p.
		// 720p remux should fallback as 720p BluRay
		if remuxMatch && resolution != R720p {
			result.Quality = QualityRemux1080p
			return result
		}

		result.Quality = QualityBluray720p
		return result
	}

	if isMatch(animeWebDlRegex, normalizedName) {
		result.SourceDetectionSource = DetectionName

		if resolution == R360p || resolution == R480p || resolution == R540p || resolution == R576p ||
			containsIgnoreCase(normalizedName, "480p") {
			result.ResolutionDetectionSource = DetectionName
			result.Quality = QualityWEBDL480p
			return result
		}

		if resolution == R1080p || containsIgnoreCase(normalizedName, "1080p") {
			result.ResolutionDetectionSource = DetectionName
			result.Quality = QualityWEBDL1080p
			return result
		}

		if resolution == R2160p || containsIgnoreCase(normalizedName, "2160p") {
			result.ResolutionDetectionSource = DetectionName
			result.Quality = QualityWEBDL2160p
			return result
		}

		result.Quality = QualityWEBDL720p
		return result
	}

	if resolution != ResolutionUnknown {
		source := SourceUnknown
		modifier := ModifierNONE

		if remuxMatch {
			result.SourceDetectionSource = DetectionName
			source = SourceBLURAY
			modifier = ModifierREMUX
		} else {
			quality := qualityForExtension(getPathExtension(name))
			if quality.ID != QualityUnknown.ID {
				result.SourceDetectionSource = DetectionExtension
				source = quality.Source
			}
		}

		var res int
		var fallback Quality
		switch resolution {
		case R2160p:
			res, fallback = 2160, QualityHDTV2160p
		case R1080p:
			res, fallback = 1080, QualityHDTV1080p
		case R720p:
			res, fallback = 720, QualityHDTV720p
		case R360p, R480p, R540p, R576p:
			res, fallback = 480, QualitySDTV
		}
		if res != 0 {
			result.ResolutionDetectionSource = DetectionName
			if source == SourceUnknown {
				result.Quality = fallback
			} else {
				result.Quality = FindBySourceAndResolution(source, res, modifier)
			}
			return result
		}
	}

	if ok(codec, "x264") {
		result.Quality = QualitySDTV
		return result
	}

	if containsIgnoreCase(normalizedName, "848x480") {
		result.ResolutionDetectionSource = DetectionName

		if strings.Contains(normalizedName, "dvd") {
			result.SourceDetectionSource = DetectionName
			result.Quality = QualityDVD
		} else if containsIgnoreCase(normalizedName, "bluray") {
			result.SourceDetectionSource = DetectionName
			result.Quality = QualityBluray480p
		} else {
			result.Quality = QualitySDTV
		}

		return result
	}

	if containsIgnoreCase(normalizedName, "1280x720") {
		result.ResolutionDetectionSource = DetectionName

		if containsIgnoreCase(normalizedName, "bluray") {
			result.SourceDetectionSource = DetectionName
			result.Quality = QualityBluray720p
		} else {
			result.Quality = QualityHDTV720p
		}

		return result
	}

	if containsIgnoreCase(normalizedName, "1920x1080") {
		result.ResolutionDetectionSource = DetectionName

		if containsIgnoreCase(normalizedName, "bluray") {
			result.SourceDetectionSource = DetectionName
			result.Quality = QualityBluray1080p
		} else {
			result.Quality = QualityHDTV1080p
		}

		return result
	}

	if containsIgnoreCase(normalizedName, "bluray720p") {
		result.SourceDetectionSource = DetectionName
		result.ResolutionDetectionSource = DetectionName
		result.Quality = QualityBluray720p
		return result
	}

	if containsIgnoreCase(normalizedName, "bluray1080p") {
		result.SourceDetectionSource = DetectionName
		result.ResolutionDetectionSource = DetectionName
		result.Quality = QualityBluray1080p
		return result
	}

	if containsIgnoreCase(normalizedName, "bluray2160p") {
		result.SourceDetectionSource = DetectionName
		result.ResolutionDetectionSource = DetectionName
		result.Quality = QualityBluray2160p
		return result
	}

	otherSource := otherSourceMatch(normalizedName)
	if otherSource.ID != QualityUnknown.ID {
		result.SourceDetectionSource = DetectionName
		result.Quality = otherSource
	}

	return result
}

func pick(cond bool, a, b Quality) Quality {
	if cond {
		return a
	}
	return b
}

func parseResolution(name string) Resolution {
	m := match(resolutionRegex, name)
	alt := match(alternativeResolutionRegex, name)

	if m == nil && alt == nil {
		return ResolutionUnknown
	}

	switch {
	case ok(m, "R360p"):
		return R360p
	case ok(m, "R480p"):
		return R480p
	case ok(m, "R540p"):
		return R540p
	case ok(m, "R576p"):
		return R576p
	case ok(m, "R720p"):
		return R720p
	case ok(m, "R1080p"):
		return R1080p
	case ok(m, "R2160p") || ok(alt, "R2160p"):
		return R2160p
	}
	return ResolutionUnknown
}

func otherSourceMatch(name string) Quality {
	m := match(otherSourceRegex, name)
	if m == nil {
		return QualityUnknown
	}
	if ok(m, "sdtv") {
		return QualitySDTV
	}
	if ok(m, "hdtv") {
		return QualityHDTV720p
	}
	return QualityUnknown
}

func parseQualityModifiers(name, normalizedName string) QualityModel {
	result := newQualityModel()

	versionMatch := match(versionRegex, normalizedName)
	version := 0
	if versionMatch != nil {
		version, _ = strconv.Atoi(value(versionMatch, "version"))
		result.Revision.Version = version
		result.RevisionDetectionSource = DetectionName
	}

	if isMatch(properRegex, normalizedName) {
		if versionMatch != nil {
			result.Revision.Version = version + 1
		} else {
			result.Revision.Version = 2
		}
		result.RevisionDetectionSource = DetectionName
	}

	if isMatch(repackRegex, normalizedName) {
		if versionMatch != nil {
			result.Revision.Version = version + 1
		} else {
			result.Revision.Version = 2
		}
		result.Revision.IsRepack = true
		result.RevisionDetectionSource = DetectionName
	}

	// TODO: re-enable this when we have a reliable way to determine real
	// TODO: Only treat it as a real if it comes AFTER the season/episode number
	if n := len(matches(realRegex, name)); n > 0 {
		result.Revision.Real = n
		result.RevisionDetectionSource = DetectionName
	}

	return result
}
