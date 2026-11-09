package anime

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Markdown renders a result as the golden out/<slug>.md files have it. generator names the program in the header
// ("theatarr anime" in the golden files, "theatarr anime build" from the command); overridesPath is the overrides
// file the story-order paragraph names.

// c renders a value for a table cell: empty for None, [] and "", lists joined with ", ", "|" made "/" in plain values.
func c(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case *string:
		if x == nil {
			return ""
		}
		return c(*x)
	case *int:
		if x == nil {
			return ""
		}
		return strconv.Itoa(*x)
	case *bool:
		if x == nil {
			return ""
		}
		return c(*x)
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return strings.ReplaceAll(x, "|", "/")
	case int:
		return strconv.Itoa(x)
	case []string:
		return strings.Join(x, ", ")
	case []int:
		parts := make([]string, len(x))
		for i, n := range x {
			parts[i] = strconv.Itoa(n)
		}
		return strings.Join(parts, ", ")
	case []any:
		if len(x) == 0 {
			return ""
		}
		parts := make([]string, len(x))
		for i, n := range x {
			parts[i] = pyStrAny(n)
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprint(v)
}

// pyStrAny is str() of a decoded JSON value.
func pyStrAny(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return pyFloat(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func rawList(r json.RawMessage) any {
	if len(r) == 0 || string(r) == "null" {
		return nil
	}
	var v any
	if err := json.Unmarshal(r, &v); err != nil {
		return string(r)
	}
	if l, ok := v.([]any); ok {
		return l
	}
	return pyStrAny(v)
}

func seTupleList(x []SE) string {
	parts := make([]string, len(x))
	for i, se := range x {
		parts[i] = fmt.Sprintf("(%d, %d)", se[0], se[1])
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func movesRepr(ms []Move) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = m.pyRepr()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func Markdown(r *Result, generator, overridesPath string) string {
	L := []string{fmt.Sprintf("# %s: watch order and arr mapping", r.Franchise), "",
		fmt.Sprintf("Generated %s by `%s` from open data (AniList seed %d). See the JSON next to this file for every field.",
			r.Generated, generator, r.SeedAniList), ""}
	L = append(L, "## TVDB series", "", "| TVDB id | Title | In Sonarr | SkyHook AniList ids | XEM scene != TVDB |",
		"|---|---|---|---|---|")
	for _, t := range r.TVDBSeriesList {
		x := t.XEM
		differs, rows := "", ""
		if x.Error == "" {
			differs, rows = strconv.Itoa(x.Differs), strconv.Itoa(x.Rows)
		}
		L = append(L, fmt.Sprintf("| %d | %s | %s | %s | %s of %s |", t.ID, t.Title, c(t.InSonarr),
			c(rawList(t.SkyhookAniListIDs)), differs, rows))
	}
	L = append(L, "", "## Automatic watch order", "",
		"Spine = AniList prequel/sequel chain by premiere; other entries go after the last spine entry that "+
			"premiered before them; TVDB-only specials use TVDB airsBefore/After. *opt* = optional.", "",
		"| # | Entry | Format | Date | Role | TVDB (Sonarr numbering) | TMDb | Flags | Notes |",
		"|---|---|---|---|---|---|---|---|---|")
	ents := map[string]*Entry{}
	for _, e := range r.Entries {
		ents[e.Key] = e
	}
	for _, w := range r.WatchOrder {
		var notes []string
		if len(w.Alternates) > 0 {
			notes = append(notes, "alternate: "+strings.Join(w.Alternates, ", "))
		}
		if w.AlternateOf != "" {
			s := "alternate of " + w.AlternateOf
			if w.Route != "" {
				s += fmt.Sprintf(" (route of %s)", w.Route)
			}
			notes = append(notes, s)
		}
		if w.TVDBPlacesAfter != "" {
			notes = append(notes, "TVDB places after "+w.TVDBPlacesAfter)
		}
		if w.TVDBNote != "" {
			notes = append(notes, w.TVDBNote)
		}
		if e := ents[w.Key]; e != nil && e.TVDB != nil && e.TVDB.Method != "" && e.TVDB.Method != "scudlee" {
			notes = append(notes, "mapping: "+e.TVDB.Method)
		}
		role := w.Role
		if w.Optional != nil && *w.Optional {
			role += " *opt*"
		}
		part := ""
		if str(w.Part) != "" {
			part = fmt.Sprintf(" (%s)", *w.Part)
		}
		n := "-"
		if w.N != nil && *w.N != 0 {
			n = strconv.Itoa(*w.N)
		}
		L = append(L, fmt.Sprintf("| %s | %s%s `%s` | %s | %s | %s | %s | %s | %s | %s |", n, c(w.Title), part, w.Key,
			pyStr(w.Format), c(w.Date), role, c(w.TVDB), c(w.TMDb), c(w.Flags), c(strings.Join(notes, "; "))))
	}
	so := r.StoryOrder
	src := ""
	if so.Source != nil && so.Source != "" {
		src = fmt.Sprintf(" (source: %s)", c(anyList(so.Source)))
	}
	L = append(L, "", "## Story order (candidate)", "",
		fmt.Sprintf("Basis: %s. Curated moves applied from `%s`: %d%s.", so.Basis, overridesPath, so.MovesApplied, src), "")
	if len(so.MovesFailed) > 0 {
		L = append(L, "Failed moves: "+movesRepr(so.MovesFailed), "")
	}
	for _, row := range so.Order {
		part := ""
		if str(row.Part) != "" {
			part = fmt.Sprintf(" (%s)", *row.Part)
		}
		mv := ""
		if row.MovedByOverride != "" {
			mv = " - override: " + row.MovedByOverride
		}
		pb := ""
		if row.PlacedBy != "" && row.PlacedBy != "release-date" {
			pb = fmt.Sprintf(" [%s]", row.PlacedBy)
		}
		opt := ""
		if row.Optional != nil && *row.Optional {
			opt = " *opt*"
		}
		L = append(L, fmt.Sprintf("%d. %s%s `%s`%s%s%s", *row.N, pyStr(row.Title), part, row.Key, opt, pb, mv))
	}
	L = append(L, "", "## Release order", "", "| # | Entry | Format | Premiere | Premiered during |", "|---|---|---|---|---|")
	for _, x := range r.ReleaseOrder {
		L = append(L, fmt.Sprintf("| %d | %s `%s` | %s | %s | %s |", *x.N, c(x.Title), x.Key, pyStr(x.Format), c(x.Date),
			c(x.PremieredDuring)))
	}
	L = append(L, "", "## Entries and mapping detail", "",
		"| Key | Title | Format | Start | Eps | AniDB | TVDB | Method | Target | TMDb check | anibridge agrees | Notes |",
		"|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, e := range r.Entries {
		var tms []string
		for _, ck := range e.TMDbCheck {
			title := ""
			if !ck.LookupFailed {
				title = pyStr(ck.Title)
			}
			ic := str(ck.InCinemas)
			if len(ic) > 10 {
				ic = ic[:10]
			}
			s := fmt.Sprintf("%d %s (%s)", ck.TMDbID, title, ic)
			if ck.Warning != "" {
				s += " WARN " + ck.Warning
			}
			tms = append(tms, s)
		}
		tvs, method := "", ""
		if tv := e.TVDB; tv != nil {
			tvs = fmt.Sprintf("%d %s", tv.SeriesID, tv.Range)
			if len(tv.MissingOnTVDB) > 0 {
				tvs += fmt.Sprintf(" (missing on TVDB: %s)", seTupleList(tv.MissingOnTVDB))
			}
			if l, ok := tv.MissingInSonarr.([]SE); ok && len(l) > 0 {
				tvs += fmt.Sprintf(" (not in Sonarr: %d)", len(l))
			}
			method = tv.Method
		}
		t := e.TitleEn
		if str(t) == "" {
			t = e.Title
		}
		L = append(L, fmt.Sprintf("| `%s` | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |", e.Key, c(t),
			pyStr(e.Format), c(e.Start), c(e.Episodes), c(e.AniDBID), c(tvs), c(method), c(e.Target),
			c(strings.Join(tms, "; ")), c(e.AnibridgeAgrees), c(strings.Join(e.Notes, "; "))))
	}
	if len(r.TVDBRegularUnclaimed) > 0 {
		L = append(L, "", "## TVDB regular episodes no entry claims", "")
		for _, x := range r.TVDBRegularUnclaimed {
			L = append(L, "- "+x)
		}
	}
	if len(r.ExcludedNeighbours) > 0 {
		L = append(L, "", "## Related but excluded (crossovers, cameos)", "")
		for _, x := range r.ExcludedNeighbours {
			L = append(L, fmt.Sprintf("- AniList %d %s (%s): %s", x.AniListID, pyStr(x.Title), pyStr(x.Format), x.Why))
		}
	}
	if cm := r.Community; cm != nil {
		L = append(L, "", "## Against the community order", "")
		if len(cm.Sources) > 0 && string(cm.Sources) != "null" {
			_ = decodeOrdered(cm.Sources, func(k string, v json.RawMessage) error {
				var val any
				_ = json.Unmarshal(v, &val)
				L = append(L, fmt.Sprintf("- %s: %s", k, pyStrAny(val)))
				return nil
			})
		}
		L = append(L, "")
		for i, name := range cm.Against.Keys {
			d := cm.Against.Values[i]
			miss, extra := c(d.MissingHere), c(d.ExtraHere)
			if miss == "" {
				miss = "none"
			}
			if extra == "" {
				extra = "none"
			}
			L = append(L, fmt.Sprintf("- **%s**: %d/%d community entries matched. Missing here: %s. Extra here: %s. Order differences: %d",
				name, d.Matched, d.CommunityEntries, miss, extra, len(d.OrderInversions)))
			for j, inv := range d.OrderInversions {
				if j >= 15 {
					break
				}
				L = append(L, "  - "+inv)
			}
		}
	}
	return strings.Join(L, "\n") + "\n"
}

func anyList(v any) any {
	switch x := v.(type) {
	case []any:
		return x
	case string:
		return x
	}
	return pyStrAny(v)
}
