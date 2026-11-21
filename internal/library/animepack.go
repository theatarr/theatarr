package library

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/indexer"
)

// The proof of an anime pack's numbering (docs/design/series.md, "Anime packs of several seasons"). A download whose
// stored mapping carries a pack claim (decide.PackClaim: a span of several seasons, or an absolute batch across
// seasons) imports only when its files match the claim one to one, for the whole pack, also when the download was
// narrowed to some seasons: every regular file reads as numbers in the claim's own convention, no episode is claimed
// by two files, and the files' episodes are exactly the claimed ones. Extras are set aside first and never linked.
// The proof's table is then the placement (resolver.episodes); a failed proof rejects the download like a mislabelled
// release (failed, blocklisted, nothing linked), and its seasons are searched again one by one.

// HowPack is a link's How for a file placed by the proof of a pack of several seasons; HowPackExtra an extra it set
// aside.
const (
	HowPack      = "pack"
	HowPackExtra = "pack extra"
)

// packProof is a proved pack's placement: each regular file's episodes, and the extras set aside.
type packProof struct {
	files  map[string][]int64
	extras map[string]bool
}

// claimed is the pack claim the import must prove, nil when none: a client's explicit files are their own choice.
func claimed(d *importDownload) *decide.PackClaim {
	if d.EpisodeMap == nil || len(d.Manual) > 0 {
		return nil
	}
	return d.EpisodeMap.Pack
}

var (
	// packExtraFolderRE is a folder of extras inside a pack.
	packExtraFolderRE = regexp.MustCompile(`(?i)^(?:extras?|specials?|bonus|nc|nced|ncop|creditless|season[ ._]?0+|s0+|menus?|ovas?|oads?)$`)
	// packSeasonFolderRE is a folder named as one season ("Season 2", "S02", "Show 2nd Season").
	packSeasonFolderRE = regexp.MustCompile(`(?i)^(?:(?:season|s)[ ._]?0*(\d{1,2})|.*\b(\d)(?:st|nd|rd|th)[ ._]season)$`)
	// packTitleSeasonRE is a season at the end of a parsed series title ("Show S2" of "Show S2 - 05").
	// packOrdinalRE is an ordinal ("2nd"); packTrailingTagsRE release tags after a folder's name ("[BD 1080p]").
	packOrdinalRE      = regexp.MustCompile(`(?i)\b\d(?:st|nd|rd|th)\b`)
	packTrailingTagsRE = regexp.MustCompile(`(?:\s*(?:\[[^\]]*\]|\([^)]*\)))+\s*$`)
	// leadingGroupRE is a leading release group bracket.
	leadingGroupRE    = regexp.MustCompile(`^\s*\[[^\]]*\]\s*`)
	packTitleSeasonRE = regexp.MustCompile(`(?i)(?:^|[ ._])(?:(?:S|Season[ ._]?)0*(\d{1,2})|(\d)(?:st|nd|rd|th)[ ._]Season)$`)
)

// packExtra says whether a file of a pack is an extra: below an extras folder ("Extras", "Specials", "Bonus", "NC",
// "Season 0", "S00"), with a marker word anywhere in its name after the library title (indexer.ExtraEpisodeToken: a
// marker only ever sets a file aside), or parsed as a special or on season 0.
func packExtra(rel string, p *sonarr.ParsedEpisodeInfo, title *indexer.TitleMatcher) bool {
	if isExtraFile(rel) {
		return true
	}
	for dir := filepath.Dir(rel); dir != "." && dir != "/" && dir != ""; dir = filepath.Dir(dir) {
		// A folder follows the same marker rule as a file name, before anything is read from it ("Season 1 [OVA]").
		name := strings.TrimSpace(filepath.Base(dir))
		if packExtraFolderRE.MatchString(name) || indexer.ExtraEpisodeToken(name, title) {
			return true
		}
	}
	if indexer.ExtraEpisodeToken(filepath.Base(rel), title) {
		return true
	}
	if p == nil {
		return false
	}
	// A whole-season name with no number is a regular video without a number: it fails the proof, not set aside.
	// The parser's special flag counts only without episode numbers ("- 05 - Special Training" is episode 5).
	return p.Special && len(p.AbsoluteEpisodeNumbers) == 0 && len(p.EpisodeNumbers) == 0 || p.IsSeasonExtra || len(p.SpecialAbsoluteEpisodeNumbers) > 0 || p.SeasonNumber == 0 &&
		len(p.EpisodeNumbers) > 0
}

// folderSeason is the one season the folders above a file name (inside the release), 0 for none; ok is false when
// two folders name different seasons.
func folderSeason(rel string) (int, bool) {
	season := 0
	for dir := filepath.Dir(rel); dir != "." && dir != "/" && dir != ""; dir = filepath.Dir(dir) {
		name := strings.TrimSpace(filepath.Base(dir))
		n := 0
		if _, _, _, span := indexer.AnimePackSeasons(name); span {
			continue // a folder that names a range ("Show S01-S02") names no season, as the release name does
		}
		if len(packOrdinalRE.FindAllString(name, 2)) > 1 {
			continue // "Show 1st & 2nd Season" names no one season
		}
		if m := packSeasonFolderRE.FindStringSubmatch(strings.TrimSpace(packTrailingTagsRE.ReplaceAllString(name, ""))); m != nil {
			n, _ = strconv.Atoi(m[1] + m[2])
		} else if p := sonarr.ParseTitle(name); p != nil && p.FullSeason && len(p.EpisodeNumbers) == 0 &&
			len(p.AbsoluteEpisodeNumbers) == 0 && !p.IsMultiSeason {
			n = p.SeasonNumber
		}
		if n <= 0 {
			continue
		}
		if season != 0 && season != n {
			return 0, false
		}
		season = n
	}
	return season, true
}

// proveFile reads one regular file of the pack under the claim: its episodes, or why the numbering cannot be trusted.
func proveFile(rel string, p *sonarr.ParsedEpisodeInfo, read string, claim *decide.PackClaim, byNumber map[[2]int]int64,
	numbers map[int]int64, seasonOf map[int64]int, claimedIDs map[int64]bool) ([]int64, string) {
	if p == nil || len(p.EpisodeNumbers) == 0 && len(p.AbsoluteEpisodeNumbers) == 0 {
		return nil, "a file reads as no episode: " + rel
	}
	if (len(p.EpisodeNumbers) > 1 || len(p.AbsoluteEpisodeNumbers) > 1) && !packJoinedRE.MatchString(read) {
		// Several episodes only from a joined token: "Brave 10 - 13" is no file of episodes 10 to 13.
		return nil, "a file reads as several numbers across a title separator: " + rel
	}
	folder, ok := folderSeason(rel)
	if !ok {
		return nil, "a file sits in folders of two seasons: " + rel
	}
	own := 0
	nums := p.EpisodeNumbers
	if len(nums) > 0 {
		own = p.SeasonNumber
	} else if m := packTitleSeasonRE.FindStringSubmatch(p.SeriesTitle); m != nil {
		own, _ = strconv.Atoi(m[1] + m[2]) // "Show S2 - 05", "Show 2nd Season - 05"
	}
	if own > 0 && folder > 0 && own != folder {
		return nil, fmt.Sprintf("a file named for season %d sits in a season %d folder: %s", own, folder, rel)
	}
	if claim.Mode == decide.PackAbsolute {
		if own > 0 || len(p.AbsoluteEpisodeNumbers) == 0 {
			return nil, "a season-numbered file under an absolute name: " + rel
		}
		var out []int64
		for _, n := range p.AbsoluteEpisodeNumbers {
			id, ok := numbers[n]
			if !ok {
				return nil, fmt.Sprintf("a file outside the pack's numbers (%d): %s", n, rel)
			}
			if folder > 0 && seasonOf[id] != folder {
				// A season folder is accepted only where its season is the numbered episode's ("Season 2/Show - 13");
				// numbers restarting per folder ("Season 2/Show - 01") are not absolute.
				return nil, fmt.Sprintf("a file numbered %d sits in a season %d folder: %s", n, folder, rel)
			}
			out = append(out, id)
		}
		return out, ""
	}
	season := cmpOr(own, folder)
	if season == 0 {
		return nil, "an absolute-numbered file under a season name: " + rel
	}
	if len(nums) == 0 {
		nums = p.AbsoluteEpisodeNumbers // "Season 2/Show - 05": the folder's season, the file's number
	}
	var out []int64
	for _, n := range nums {
		id, ok := byNumber[[2]int{season, n}]
		if !ok || !claimedIDs[id] {
			return nil, fmt.Sprintf("a file outside the pack's episodes (S%02dE%02d): %s", season, n, rel)
		}
		out = append(out, id)
	}
	return out, ""
}

// places says whether the table puts a file wholly on the download's episodes (allowed): a file that also holds an
// episode the download was not given is out of scope and cannot be placed.
func (p *packProof) places(allowed map[int64]episodeInfo) bool {
	for _, ids := range p.files {
		if len(ids) > 0 && !slices.ContainsFunc(ids, func(id int64) bool { _, ok := allowed[id]; return !ok }) {
			return true
		}
	}
	return false
}

// parseFile parses a pack file's name with the library title dropped first (never a scene name: it can carry the
// season, "Show 2nd Season"): when the name (after a leading group bracket) starts with title, word boundary after
// it, the rest is parsed after a neutral name, so a number in the title is never read as an episode ("Brave 10 - 11"
// is episode 11). Otherwise the name is parsed as it is. rest is what was read for numbers.
func parseFile(stem string, title *indexer.TitleMatcher) (p *sonarr.ParsedEpisodeInfo, rest string) {
	if rest, ok := title.Strip(stem); ok {
		// The group bracket stays: the parser reads "[Grp] Series S1 - 01" as season 1's episode 1.
		return sonarr.ParseTitle(leadingGroupRE.FindString(stem) + "Series" + rest), rest
	}
	return sonarr.ParseTitle(stem), stem
}

// packJoinedRE is a name's token that joins several episodes with no title separator: S01E01-E02, S01E01E02, 01-02,
// 01+02, 01 & 02, 01~02.
var packJoinedRE = regexp.MustCompile(`(?i)S\d{1,2}E\d{1,4}(?:-?(?:S\d{1,2})?E\d{1,4})+|\d{1,4}(?:-|\s*[+&~]\s*)\d{1,4}`)

func cmpOr(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// provePack proves a claimed pack's files (the download's own video files) against its claim, over the title's
// episodes. why is the first reason the numbering cannot be trusted ("" when the proof holds).
func provePack(t *decide.Title, claim *decide.PackClaim, files []PlannedFile) (*packProof, string) {
	byNumber := map[[2]int]int64{}
	label := map[int64]string{}
	seasonOf := map[int64]int{}
	for _, e := range t.Episodes {
		byNumber[[2]int{e.Season, e.Number}] = e.ID
		seasonOf[e.ID] = e.Season
		label[e.ID] = fmt.Sprintf("S%02dE%02d", e.Season, e.Number)
	}
	claimedIDs := map[int64]bool{}
	for _, id := range claim.Episodes {
		claimedIDs[id] = true
	}
	numbers := make(map[int]int64, len(claim.Numbers))
	for _, x := range claim.Numbers {
		numbers[x.N] = x.Episode
	}
	proof := &packProof{files: map[string][]int64{}, extras: map[string]bool{}}
	title := indexer.NewTitleMatcher(t.Name)
	holder := map[int64]string{}
	for _, f := range files {
		stem := strings.TrimSuffix(filepath.Base(f.Rel), filepath.Ext(f.Rel))
		p, read := parseFile(stem, title)
		if packExtra(f.Rel, p, title) {
			proof.extras[f.Rel] = true
			continue
		}
		ids, why := proveFile(f.Rel, p, read, claim, byNumber, numbers, seasonOf, claimedIDs)
		if why != "" {
			return nil, why
		}
		for _, id := range ids {
			if other, ok := holder[id]; ok {
				return nil, fmt.Sprintf("two files on %s: %s and %s", label[id], other, f.Rel)
			}
			holder[id] = f.Rel
		}
		proof.files[f.Rel] = ids
	}
	for _, id := range claim.Episodes {
		if _, ok := holder[id]; !ok {
			return nil, fmt.Sprintf("no file for %s of the %d episodes the pack claims", label[id], len(claim.Episodes))
		}
	}
	return proof, ""
}
