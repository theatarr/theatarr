package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/router"
)

// The wrong-audio rule (docs/design/library.md, "Wrong-audio guard"): a file whose audio tracks all carry a known
// language, none of them one the library wants, does not replace a file and does not fill an empty slot. It reads
// what the file says, not the release name: a French-only file named like an English remux is refused.
//   - Judged only when the probe succeeded, the file has an audio track, and every audio track's language was in the
//     file and is in the language table (decide.AudioLanguage). One untagged or unknown track: no verdict.
//   - Wanted: a film profile's named language; else (profile original, and every series library) the title's
//     original language or English, English alone for an original language the table lacks. No rule for a film
//     profile set to any, nor for a title whose original language nothing names.
//   - Replacing a file (the file at the same path included): refused unless the new file shares an audio language
//     with the files it replaces. An empty film slot: refused once; when this library already refused a different
//     release of the film for the same audio, the next one is taken. An empty series slot: refused.
//   - A download is one verdict: one refused file among those the plan links rejects the import (reject()). Manual
//     import is never refused, only noted. Files already in the library are never judged again.

// audioRefusedPrefix starts every reason the rule gives.
const audioRefusedPrefix = decide.AudioRefusedPrefix

// audioWanted is the version's wanted languages (three-letter codes, English first), or nil for no rule.
func (l *Library) audioWanted(ctx context.Context, d *importDownload) ([]string, error) {
	v := &d.Version
	var lang string
	if pr := l.pol.Profiles[v.Policy.Profile]; pr != nil {
		lang = pr.Language
	}
	if want, decided := profileWanted(v.Title.Kind, lang); decided {
		return want, nil
	}
	t, err := decide.LoadTitle(ctx, l.st.Reader(), v.Title.ID)
	if err != nil {
		return nil, err
	}
	return originalWanted(t.NamedOriginalLanguage), nil
}

// profileWanted is what a film profile's language decides alone: nothing for any (or unset, or a language the
// table lacks), the named language; decided is false for original and for every series library (a series profile
// carries no language), which want the title's original language or English.
func profileWanted(kind, lang string) (want []string, decided bool) {
	lang = strings.TrimSpace(lang)
	if kind != "movie" || strings.EqualFold(lang, "original") {
		return nil, false
	}
	if code, ok := decide.LanguageCode(lang); ok {
		return []string{code}, true
	}
	return nil, true
}

// originalWanted is English and the title's original language (by code or name); English alone for an original
// language the table lacks, nil when nothing names it ("").
func originalWanted(original string) []string {
	if strings.TrimSpace(original) == "" {
		return nil
	}
	want := []string{"eng"}
	if code, ok := decide.LanguageCode(original); ok && code != "eng" {
		want = append(want, code)
	}
	return want
}

// fileAudio is the audio languages of a fresh probe (three-letter codes, de-duplicated, by name), and whether the
// rule may judge the file: a probe with an audio track, every one tagged in the file with a known language.
func fileAudio(p *probe.Result) ([]string, bool) {
	if p == nil || len(p.Audio) == 0 {
		return nil, false
	}
	var out []string
	for _, a := range p.Audio {
		code, ok := decide.AudioLanguage(a.Language)
		if !a.Tagged || !ok {
			return nil, false
		}
		out = append(out, code)
	}
	return sortedLanguages(out), true
}

// sortedLanguages de-duplicates codes and orders them by their names.
func sortedLanguages(codes []string) []string {
	slices.SortFunc(codes, func(a, b string) int { return strings.Compare(decide.LanguageName(a), decide.LanguageName(b)) })
	return slices.Compact(codes)
}

func sharesLanguage(a, b []string) bool {
	return slices.ContainsFunc(a, func(c string) bool { return slices.Contains(b, c) })
}

// languageWords names languages for a sentence: "French", "French and Spanish", "French, German and Spanish" (or,
// with "or", "English or Japanese").
func languageWords(codes []string, conj string) string {
	names := make([]string, len(codes))
	for i, c := range codes {
		names[i] = decide.LanguageName(c)
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + conj + " " + names[len(names)-1]
}

// audioOpening is the start of an empty slot's refusal for a file with these languages. It is also the stable
// marker the library's blocklist rows are matched by (audioRefusedBefore): a refusal made when replacing a file
// reads otherwise and does not count.
func audioOpening(langs []string) string {
	return audioRefusedPrefix + languageWords(langs, "and") + " audio only; this library wants "
}

// audioGuard runs the wrong-audio rule on the files the plan links: the reason the import is refused, "" when it
// goes on. A refused file is put first among the rejected files. An import a person chose (Manual import, an import
// by path) and a preview (dryRun: Manual import's list) are never refused: the rule's finding is a note on the file.
func (l *Library) audioGuard(ctx context.Context, d *importDownload, plan *Plan, dryRun bool) (string, error) {
	noteOnly := dryRun || explicitImport(d)
	var want []string
	wantRead := false
	for i := range plan.Links {
		lk := &plan.Links[i]
		if lk.Exists {
			continue // the file is in the library already
		}
		langs, ok := fileAudio(lk.probe)
		if !ok {
			continue
		}
		if !wantRead {
			var err error
			if want, err = l.audioWanted(ctx, d); err != nil {
				return "", err
			}
			wantRead = true
		}
		if want == nil {
			return "", nil
		}
		if sharesLanguage(langs, want) {
			continue
		}
		replacing, has := false, ""
		for _, old := range displaced(plan.held, lk, d.Version.Title.Kind) {
			replacing = true
			if oldLangs := storedAudio(old.probe, old.media, old.adopted); !sharesLanguage(langs, oldLangs) {
				has = "no known audio language"
				if len(oldLangs) > 0 {
					has = languageWords(oldLangs, "and")
				}
				break
			}
		}
		var reason, note string
		switch {
		case replacing && has == "":
			continue // it shares a language with every file it displaces
		case replacing:
			reason = audioRefusedPrefix + languageWords(langs, "and") + " audio only; the file in the library has " +
				has + "."
		case d.Version.Title.Kind != "movie":
			reason = audioOpening(langs) + languageWords(want, "or") + ". Use Manual import to take it anyway."
		case noteOnly:
			reason = audioOpening(langs) + languageWords(want, "or") + "."
		default:
			earlier, err := l.audioRefusedBefore(ctx, d, audioOpening(langs))
			if err != nil {
				return "", err
			}
			if earlier {
				note = "audio: " + languageWords(langs, "and") + " only, not what this library wants; taken because " +
					"an earlier release with the same audio was refused"
				break
			}
			reason = audioOpening(langs) + languageWords(want, "or") + ". Another release with the same audio would be taken."
		}
		f := plannedFile(plan, lk.File)
		if noteOnly && reason != "" {
			// Manual import: the reason's own words, said as what the file holds (no refusal, no next step).
			note, _, _ = strings.Cut(strings.Replace(reason, audioRefusedPrefix, "Manual import: ", 1), ". ")
			note = strings.TrimSuffix(note, ".") + "."
			reason = ""
		}
		if note != "" {
			if f != nil {
				f.Note = strings.TrimPrefix(f.Note+"; "+note, "; ")
			}
			continue
		}
		rf := PlannedFile{Rel: lk.File, Path: lk.Target}
		if f != nil {
			rf = *f
		}
		rf.Reason = reason
		plan.Rejected = append([]PlannedFile{rf}, plan.Rejected...)
		plan.audioRefused = true
		return reason, nil
	}
	return "", nil
}

// explicitImport says a person chose the import's files: Manual import (a client's chosen files), or an import that
// named its path on the mount (the native import by path; a download client's completed download is not one).
func explicitImport(d *importDownload) bool {
	return len(d.Manual) > 0 || d.SourcePath != "" && d.Client == ""
}

// displaced are the version's files a link displaces, each judged on its own: for a film every file of the version
// (one file replaces them all); for a series the files at the link's path or sharing an episode with it.
func displaced(held []existingFile, lk *PlannedLink, kind string) []existingFile {
	var out []existingFile
	for _, old := range held {
		if kind == "movie" || old.Path == lk.Path || overlaps(old.Episodes, lk.Episodes) {
			out = append(out, old)
		}
	}
	return out
}

func plannedFile(plan *Plan, rel string) *PlannedFile {
	for i := range plan.Files {
		if plan.Files[i].Rel == rel {
			return &plan.Files[i]
		}
	}
	return nil
}

// storedAudio is a library file's known audio languages, from its stored probe, else (an adopted file) the arr's
// media info ("eng/fre").
func storedAudio(pr, media sql.NullString, adopted bool) []string {
	var tags []string
	if p, err := parseProbe(pr); err == nil && p != nil {
		for _, a := range p.Audio {
			tags = append(tags, a.Language)
		}
	} else if adopted && media.String != "" {
		var m struct {
			AudioLanguages string `json:"audioLanguages"`
		}
		if json.Unmarshal([]byte(media.String), &m) == nil && m.AudioLanguages != "" {
			tags = strings.Split(m.AudioLanguages, "/")
		}
	}
	var out []string
	for _, t := range tags {
		if c, ok := decide.AudioLanguage(t); ok {
			out = append(out, c)
		}
	}
	return sortedLanguages(out)
}

// audioRefusedBefore says whether this library refused another release of the title with an empty-slot refusal
// for the same audio: a library blocklist row whose reason starts with opening, under another blocklist key.
func (l *Library) audioRefusedBefore(ctx context.Context, d *importDownload, opening string) (bool, error) {
	var found bool
	err := l.st.Reader().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM router_blocklist WHERE version_key = ?
		AND title_id = ? AND source = ? AND info_hash <> ? AND substr(reason, 1, length(?)) = ?)`, d.Version.Key,
		d.Version.Title.ID, router.BlocklistLibrary, router.BlocklistKey(d.InfoHash, d.ReleaseTitle), opening,
		opening).Scan(&found)
	return found, err
}
