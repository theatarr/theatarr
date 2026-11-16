package journal

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Plain words for the journal's list (docs/journal.md "The list"): every entry's Summary names files by their file
// name only (the full paths are in Entry.Paths and on the entry), and Label is its kind in History's words.

// baseName is a path's file name ("" for none).
func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	return path.Base(p)
}

// providerShort names a provider briefly: RD, AD, TB, else its key.
func providerShort(p string) string {
	switch strings.ToLower(p) {
	case "rd", "realdebrid":
		return "RD"
	case "ad", "alldebrid":
		return "AD"
	case "tb", "torbox":
		return "TB"
	}
	return p
}

// arrWords names an arr instance: "radarr_4k" is "Radarr (radarr_4k)".
func arrWords(inst string) string {
	switch {
	case inst == "":
		return "Sonarr/Radarr"
	case strings.Contains(strings.ToLower(inst), "sonarr"):
		return "Sonarr (" + inst + ")"
	case strings.Contains(strings.ToLower(inst), "radarr"):
		return "Radarr (" + inst + ")"
	}
	return "Sonarr/Radarr (" + inst + ")"
}

// filesWord is one file's name, or a count of several.
func filesWord(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return strconv.Itoa(len(names)) + " files"
}

// pathEnds end a path inside free text: a quote, ": ", "; ", ", " or the end.
var pathEnds = []string{`"`, ": ", "; ", ", "}

// plainDetail is a free-text detail with every path cut to its file name and "moved aside to <trash path>" said as
// "moved to the trash", in the short form (plainText).
func plainDetail(s string) string { return plainText(s, false) }

// plainText is plainDetail, in full when full is set (cleanText).
func plainText(s string, full bool) string {
	s = strings.ReplaceAll(cleanText(s, full), "moved aside to the trash", "moved to the trash")
	for _, p := range []string{"moved aside to ", "moved to "} {
		for i := 0; ; {
			k := strings.Index(s[i:], p)
			if k < 0 {
				break
			}
			k += i
			at := k + len(p)
			if at >= len(s) || s[at] != '/' {
				i = at
				continue
			}
			end := len(s)
			for _, m := range append([]string{" (renamed", " when its library"}, pathEnds...) {
				if j := strings.Index(s[at:], m); j >= 0 && at+j < end {
					end = at + j
				}
			}
			s = s[:k] + "moved to the trash" + s[end:]
			i = k + len("moved to the trash")
		}
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '/' && (i == 0 || strings.ContainsRune(` ("'=`, rune(s[i-1]))) && i+1 < len(s) && s[i+1] != ' ' {
			end := len(s)
			for _, m := range pathEnds {
				if j := strings.Index(s[i:], m); j >= 0 && i+j < end {
					end = i + j
				}
			}
			b.WriteString(baseName(s[i:end]))
			i = end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// join keeps the non-empty parts, with " · " between them.
func join(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

// grabOutcome is what became of a download, in words: imported (with the files it replaced), failed, or where it is.
func grabOutcome(state, errText string, linked, replaced int, replacedName string, full bool) string {
	switch state {
	case "imported":
		s := "imported"
		if linked > 1 {
			s += fmt.Sprintf(" %d files", linked)
		}
		switch {
		case replaced == 1:
			s += ", replaced " + replacedName
		case replaced > 1:
			s += fmt.Sprintf(", replaced %d files", replaced)
		}
		return s
	case "failed":
		if errText == "" {
			return "failed"
		}
		return "failed: " + plainText(errText, full)
	case "queued":
		return "waiting"
	case "adding", "added":
		return "starting"
	case "downloading":
		return "downloading"
	case "ready":
		return "downloaded, not imported yet"
	case "importing":
		return "importing"
	}
	return state
}

// importWords are an import's label and its state in words.
// A refused import whose download failed with it was not held for review: failed says so.
func importWords(state string, failed bool) (label, words string) {
	switch state {
	case "linked":
		return "Imported", "imported"
	case "partial":
		return "Imported", "partly imported"
	case "rejected":
		if failed {
			return "Import failed", "import failed"
		}
		return "Import held", "held for review"
	case "waiting_mount":
		return "Import waiting", "waiting for the mount"
	case "dry_run":
		return "Dry run", "dry run, nothing linked"
	case "failed":
		return "Import failed", "import failed"
	}
	return "Import", state
}

// searchWords is a search run in words: who searched, what for, and what came of it.
func searchWords(source, target, state, research, message string, full bool) string {
	who := "Search"
	switch source {
	case "wanted":
		who = "Wanted search"
	case "command":
		who = "Search now"
	}
	if research != "" {
		who = "Search again after " + map[string]string{"failed": "a failed download", "stalled": "a stalled download",
			"rejected": "a refused import", "exhausted": "every release tried failed", "library": "a refused import",
			"provider": "a provider failure", "torrent_files": "a release with unwanted files",
			"ready_timeout": "a download that never got ready", "dead_link": "a missing file"}[research]
		if strings.HasSuffix(who, "after ") {
			who += research
		}
	}
	if target != "" && target != "movie" {
		who += " (" + target + ")"
	}
	message = plainText(message, full)
	switch state {
	case "nothing":
		return join(who+": nothing grabbed", message)
	case "failed":
		return join(who+": failed", message)
	case "routed":
		if rel, ok := strings.CutPrefix(message, "routed "); ok {
			return who + ": grabbed " + rel
		}
		return join(who+": grabbed", message)
	}
	return join(who+": "+state, message)
}

// seasonOf is "Season N" when every label (S01E02) is of one season, else "".
func seasonOf(labels []string) string {
	season := ""
	for _, l := range labels {
		s, _, ok := strings.Cut(strings.TrimPrefix(l, "S"), "E")
		if !ok || (season != "" && s != season) {
			return ""
		}
		season = s
	}
	if n, err := strconv.Atoi(season); err == nil {
		return "Season " + strconv.Itoa(n)
	}
	return ""
}

// missingHash is a dead link's detail that only names the file's hash ("missing <hash>").
var missingHash = regexp.MustCompile(`(?i)^missing [0-9a-f]{8,}$`)

// byWhom is a removal's reason as who did it: "copy removed by X", "title removed by X" and "unlinked by X" are
// "by X" (the removed copy or title has a line of its own).
func byWhom(why string) string {
	for _, p := range []string{"copy removed by ", "title removed by ", "unlinked by "} {
		if who, ok := strings.CutPrefix(why, p); ok {
			return "by " + who
		}
	}
	return why
}
