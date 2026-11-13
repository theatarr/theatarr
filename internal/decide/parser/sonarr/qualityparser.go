package sonarr

// Port of Sonarr src/NzbDrone.Core/Parser/QualityParser.cs @ cab419a.

import (
	"strconv"
	"strings"
)

var (
	sourceRegex = re(`\b(?:
                                                                (?<bluray>BluRay|Blu-Ray|HD-?DVD|BDMux|BD(?!$))|
                                                                (?<webdl>WEB[-_. ]DL(?:mux)?|WEBDL|AmazonHD|AmazonSD|iTunesHD|MaxdomeHD|NetflixU?HD|WebHD|HBOMaxHD|DisneyHD|[. ]WEB[. ](?:[xh][ .]?26[45]|AVC|HEVC|DDP?5[. ]1)|[. ](?-i:WEB)$|(?:720|1080|2160)p[-. ]WEB[-. ]|[-. ]WEB[-. ](?:720|1080|2160)p|\b\s\/\sWEB\s\/\s\b|(?:AMZN|NF|DP)[. -]WEB[. -](?!Rip))|
                                                                (?<webrip>WebRip|Web-Rip|WEBMux)|
                                                                (?<hdtv>HDTV)|
                                                                (?<bdrip>BDRip|BDLight)|
                                                                (?<brrip>BRRip)|
                                                                (?<dvd>DVD|DVDRip|NTSC|PAL|xvidvd)|
                                                                (?<dsr>WS[-_. ]DSR|DSR)|
                                                                (?<pdtv>PDTV)|
                                                                (?<sdtv>SDTV)|
                                                                (?<tvrip>TVRip)
                                                                )(?:\b|$|[ .])`, optI|optX)

	rawHDRegex = re(`\b(?<rawhd>RawHD|Raw[-_. ]HD)\b`, optI)

	mpeg2Regex = re(`\b(?<mpeg2>MPEG[-_. ]?2)\b`, optNone)

	properRegex = re(`\b(?<proper>proper)\b`, optI)

	repackRegex = re(`\b(?<repack>repack\d?|rerip\d?)\b`, optI)

	versionRegex = re(`\d[-._ ]?v(?<version>\d)[-._ ]|\[v(?<version>\d)\]|repack(?<version>\d)|rerip(?<version>\d)|(?:480|576|720|1080|2160)p[._ ]v(?<version>\d)`, optI)

	realRegex = re(`\b(?<real>REAL)\b`, optNone)

	resolutionRegex = re(`\b(?:(?<R360p>360p)|(?<R480p>480p|480i|640x480|848x480)|(?<R540p>540p)|(?<R576p>576p)|(?<R720p>720p|1280x720|960p)|(?<R1080p>1080p|1920x1080|1440p|FHD|1080i|4kto1080p)|(?<R2160p>2160p|3840x2160|4k[-_. ](?:UHD|HEVC|BD|H265)|(?:UHD|HEVC|BD|H265)[-_. ]4k))\b`, optI)

	// Handle cases where no resolution is in the release name (assume if UHD then 4k) or resolution is non-standard
	alternativeResolutionRegex = re(`\b(?<R2160p>UHD)\b|(?<R2160p>\[4K\])`, optI)

	codecRegex = re(`\b(?:(?<x264>x264)|(?<h264>h264)|(?<xvidhd>XvidHD)|(?<xvid>Xvid)|(?<divx>divx))\b`, optI)

	otherSourceRegex = re(`(?<hdtv>HD[-_. ]TV)|(?<sdtv>SD[-_. ]TV)`, optI)

	animeBlurayRegex = re(`bd(?:720|1080|2160)|(?<=[-_. (\[])bd(?=[-_. )\]])`, optI)
	animeWebDlRegex  = re(`\[WEB\]|[\[\(]WEB[ .]`, optI)

	highDefPdtvRegex = re(`hr[-_. ]ws`, optI)

	remuxRegex = re(`(?:[_. ]|\d{4}p-|\bHybrid-)(?<remux>(?:(BD|UHD)[-_. ]?)?Remux)\b|(?<remux>(?:(BD|UHD)[-_. ]?)?Remux[_. ]\d{4}p)`, optI)
)

// Resolution is QualityParser's Resolution enum.
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
func ParseQuality(name string) QualityModel { return v4plusd.parseQuality(name) }

func (d *dialect) parseQuality(name string) QualityModel {
	if strings.TrimSpace(name) == "" {
		return NewQualityModel()
	}

	name = trimDotNet(name)

	result := d.parseQualityName(name)

	// Based on extension
	if result.Quality == QualityUnknown && !containsInvalidPathChars(name) {
		result.Quality = qualityForExtension(pathGetExtension(name))
		result.SourceDetectionSource = DetectionExtension
		result.ResolutionDetectionSource = DetectionExtension
	}

	return result
}

// ParseQualityName is QualityParser.ParseQualityName.
func ParseQualityName(name string) QualityModel { return v4plusd.parseQualityName(name) }

func (d *dialect) parseQualityName(name string) QualityModel {
	normalizedName := trimDotNet(strings.ReplaceAll(name, "_", " "))
	result := parseQualityModifiers(name, normalizedName)

	if isMatch(rawHDRegex, normalizedName) {
		result.SourceDetectionSource = DetectionName
		result.ResolutionDetectionSource = DetectionName
		result.Quality = QualityRAWHD
		return result
	}

	sourceMatches := matches(d.source, normalizedName)
	var sourceMatch = lastMatch(sourceMatches)
	resolution := d.parseResolution(normalizedName)
	codec := match(codecRegex, normalizedName)
	remuxMatch := isMatch(remuxRegex, normalizedName)

	if resolution != ResolutionUnknown {
		result.ResolutionDetectionSource = DetectionName
	}

	if sourceMatch != nil {
		result.SourceDetectionSource = DetectionName

		if success(sourceMatch, "bluray") {
			if success(codec, "xvid") || success(codec, "divx") {
				result.Quality = QualityBluray480p
				return result
			}

			if resolution == R2160p {
				result.Quality = pick(remuxMatch, QualityBluray2160pRemux, QualityBluray2160p)
				return result
			}

			if resolution == R1080p {
				result.Quality = pick(remuxMatch, QualityBluray1080pRemux, QualityBluray1080p)
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
				result.Quality = QualityBluray1080pRemux
				return result
			}

			result.Quality = QualityBluray720p
			return result
		}

		if success(sourceMatch, "webdl") {
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

		if success(sourceMatch, "webrip") {
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

		if success(sourceMatch, "hdtv") {
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

		if success(sourceMatch, "bdrip") || success(sourceMatch, "brrip") {
			switch resolution {
			case R720p:
				result.Quality = QualityBluray720p
			case R1080p:
				result.Quality = QualityBluray1080p
			case R2160p:
				result.Quality = QualityBluray2160p
			default:
				result.Quality = QualityBluray480p
			}
			return result
		}

		if success(sourceMatch, "dvd") {
			result.Quality = QualityDVD
			return result
		}

		if success(sourceMatch, "pdtv") || success(sourceMatch, "sdtv") || success(sourceMatch, "dsr") || success(sourceMatch, "tvrip") {
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
			result.Quality = QualityBluray2160pRemux
			return result
		case R1080p:
			result.Quality = QualityBluray1080pRemux
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
			result.Quality = pick(remuxMatch, QualityBluray1080pRemux, QualityBluray1080p)
			return result
		}

		if resolution == R2160p || containsIgnoreCase(normalizedName, "2160p") {
			result.ResolutionDetectionSource = DetectionName
			result.Quality = pick(remuxMatch, QualityBluray2160pRemux, QualityBluray2160p)
			return result
		}

		// Treat a remux without a source as 1080p, not 720p.
		// 720p remux should fallback as 720p BluRay
		if remuxMatch && resolution != R720p {
			result.Quality = QualityBluray1080pRemux
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

		if remuxMatch {
			result.SourceDetectionSource = DetectionName
			source = SourceBlurayRaw
		} else {
			quality := qualityForExtension(getPathExtension(name))
			if quality != QualityUnknown {
				result.SourceDetectionSource = DetectionExtension
				source = quality.Source
			}
		}

		if resolution == R2160p {
			result.ResolutionDetectionSource = DetectionName
			result.Quality = pickSource(source, QualityHDTV2160p, 2160)
			return result
		}

		if resolution == R1080p {
			result.ResolutionDetectionSource = DetectionName
			result.Quality = pickSource(source, QualityHDTV1080p, 1080)
			return result
		}

		if resolution == R720p {
			result.ResolutionDetectionSource = DetectionName
			result.Quality = pickSource(source, QualityHDTV720p, 720)
			return result
		}

		if resolution == R360p || resolution == R480p || resolution == R540p || resolution == R576p {
			result.ResolutionDetectionSource = DetectionName
			result.Quality = pickSource(source, QualitySDTV, 480)
			return result
		}
	}

	if success(codec, "x264") {
		result.Quality = QualitySDTV
		return result
	}

	if strings.Contains(normalizedName, "848x480") {
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

	otherSourceMatch := otherSourceMatchQuality(normalizedName)

	if otherSourceMatch != QualityUnknown {
		result.SourceDetectionSource = DetectionName
		result.Quality = otherSourceMatch
	}

	return result
}

func pick(cond bool, a, b Quality) Quality {
	if cond {
		return a
	}
	return b
}

func pickSource(source QualitySource, fallback Quality, resolution int) Quality {
	if source == SourceUnknown {
		return fallback
	}
	return FindBySourceAndResolution(source, resolution)
}

func (d *dialect) parseResolution(name string) Resolution {
	m := match(d.resolution, name)
	alt := match(alternativeResolutionRegex, name)

	if m == nil && alt == nil {
		return ResolutionUnknown
	}

	switch {
	case success(m, "R360p"):
		return R360p
	case success(m, "R480p"):
		return R480p
	case success(m, "R540p"):
		return R540p
	case success(m, "R576p"):
		return R576p
	case success(m, "R720p"):
		return R720p
	case success(m, "R1080p"):
		return R1080p
	case success(m, "R2160p"):
		return R2160p
	case success(alt, "R2160p"):
		return R2160p
	}
	return ResolutionUnknown
}

func otherSourceMatchQuality(name string) Quality {
	m := match(otherSourceRegex, name)
	if m == nil {
		return QualityUnknown
	}
	if success(m, "sdtv") {
		return QualitySDTV
	}
	if success(m, "hdtv") {
		return QualityHDTV720p
	}
	return QualityUnknown
}

func parseQualityModifiers(name, normalizedName string) QualityModel {
	result := NewQualityModel()

	versionMatch := match(versionRegex, normalizedName)
	version := 0
	if versionMatch != nil {
		// Convert.ToInt32 of a single digit cannot fail.
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
