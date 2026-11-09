// Port of Radarr src/NzbDrone.Core/Parser/{ReleaseGroupParser,ParserCommon}.cs and
// src/NzbDrone.Core/MediaFiles/FileExtensions.cs (RemoveFileExtension) @ a96bf7c.

package radarr

import (
	"strings"

	"github.com/dlclark/regexp2"
)

// ParserCommon.
var (
	// preSubstitutionRegex is ParserCommon.PreSubstitutionRegex, empty in Radarr.
	preSubstitutionRegex []regexReplace

	// Valid TLDs http://data.iana.org/TLD/tlds-alpha-by-domain.txt
	websitePrefixRegex = newRegexReplace(`^(?:(?:\[|\()\s*)?(?:www\.)?[-a-z0-9-]{1,256}\.(?<!Naruto-Kun\.)(?:[a-z]{2,6}\.[a-z]{2,6}|xn--[a-z0-9-]{4,}|[a-z]{2,})\b(?:\s*(?:\]|\))|[ -]{2,})[ -]*`, "", optI)

	websitePostfixRegex = newRegexReplace(`(?:\[\s*)?(?:www\.)?[-a-z0-9-]{1,256}\.(?:xn--[a-z0-9-]{4,}|[a-z]{2,6})\b(?:\s*\])$`, "", optI)

	cleanTorrentSuffixRegex = newRegexReplace(`\[(?:ettv|rartv|rarbg|cttv|publichd)\]$`, "", optI)
)

// FileExtensions.
var (
	fileExtensionRegex = re(`\.[a-z0-9]{2,4}$`, optI)
	usenetExtensions   = map[string]bool{".par2": true, ".nzb": true}
)

// RemoveFileExtension is FileExtensions.RemoveFileExtension: strips a known media or usenet extension.
func RemoveFileExtension(title string) string {
	return replaceFunc(fileExtensionRegex, title, func(m regexp2.Match) string {
		ext := strings.ToLower(m.String())
		if isMediaExtension(ext) || usenetExtensions[ext] {
			return ""
		}
		return m.String()
	})
}

// ReleaseGroupParser.
var (
	releaseGroupRegex = re(`-(?<releasegroup>[a-z0-9]+(?<part2>-[a-z0-9]+)?(?!.+?(?:480p|576p|720p|1080p|2160p)))(?<!(?:WEB-(DL|Rip)|Blu-Ray|480p|576p|720p|1080p|2160p|DTS-HD|DTS-X|DTS-MA|DTS-ES|-ES|-EN|-CAT|-ENG|-JAP|-GER|-FRA|-FRE|-ITA|-HDRip|\d{1,2}-bit|[ ._]\d{4}-\d{2}|-\d{2}|tmdb(id)?-(?<tmdbid>\d+)|(?<imdbid>tt\d{7,8}))(?:\k<part2>)?)(?:\b|[-._ ]|$)|[-._ ]\[(?<releasegroup>[a-z0-9]+)\]$`, optI)

	invalidReleaseGroupRegex = re(`^([se]\d+|[0-9a-f]{8})$`, optI)

	animeReleaseGroupRegex = re(`^(?:\[(?<subgroup>(?!\s).+?(?<!\s))\](?:_|-|\s|\.)?)`, optI)

	// Handle Exception Release Groups that don't follow -RlsGrp; Manual List
	// name only...be very careful with this last; high chance of false positives
	exceptionReleaseGroupRegexExact = re(`\b(?<releasegroup>KRaLiMaRKo|E\.N\.D|D\-Z0N3|Koten_Gars|BluDragon|ZØNEHD|HQMUX|VARYG|YIFY|YTS(.(MX|LT|AG))?|TMd|Eml HDTeam|LMain|DarQ|BEN THE MEN|TAoE|QxR|126811)\b`, optI)

	// groups whose releases end with RlsGroup) or RlsGroup]
	exceptionReleaseGroupRegex = re(`(?<=[._ \[])(?<releasegroup>(Silence|afm72|Panda|Ghost|MONOLITH|Tigole|Joy|ImE|UTR|t3nzin|Anime Time|Project Angel|Hakata Ramen|HONE|GiLG|Vyndros|SEV|Garshasp|Kappa|Natty|RCVR|SAMPA|YOGI|r00t|EDGE2020|RZeroX|FreetheFish|Anna|Bandi|Qman|theincognito|HDO|DusIctv|DHD|CtrlHD|-ZR-|ADC|XZVN|RH|Kametsu|Celdra)(?=\]|\)))`, optI)

	cleanReleaseGroupRegex = newRegexReplace(`(-(RP|1|NZBGeek|Obfuscated|Obfuscation|Scrambled|sample|Pre|postbot|xpost|Rakuv[a-z0-9]*|WhiteRev|BUYMORE|AsRequested|AlternativeToRequested|GEROV|Z0iDS3N|Chamele0n|4P|4Planet|AlteZachen|RePACKPOST))+$`, "", optI)
)

// ParseReleaseGroup is ReleaseGroupParser.ParseReleaseGroup. It returns nil when there is no group.
func ParseReleaseGroup(title string) *string {
	title = netTrim(title)
	title = RemoveFileExtension(title)
	for _, r := range preSubstitutionRegex {
		if isMatch(r.re, title) {
			title = r.Replace(title)
			break
		}
	}

	title = websitePrefixRegex.Replace(title)
	title = cleanTorrentSuffixRegex.Replace(title)

	if m := match(animeReleaseGroupRegex, title); m != nil {
		return strPtr(value(m, "subgroup"))
	}

	title = cleanReleaseGroupRegex.Replace(title)

	if ms := matches(exceptionReleaseGroupRegexExact, title); len(ms) != 0 {
		return strPtr(value(lastMatch(ms), "releasegroup"))
	}

	if ms := matches(exceptionReleaseGroupRegex, title); len(ms) != 0 {
		return strPtr(value(lastMatch(ms), "releasegroup"))
	}

	if ms := matches(releaseGroupRegex, title); len(ms) != 0 {
		g := value(lastMatch(ms), "releasegroup")

		if isInt32(g) {
			return nil
		}

		if isMatch(invalidReleaseGroupRegex, g) {
			return nil
		}

		return strPtr(g)
	}

	return nil
}

func strPtr(s string) *string { return &s }
