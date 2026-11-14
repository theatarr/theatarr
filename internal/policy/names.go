package policy

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// LibraryNames is the one rule for a library's name, used for every caller and sent to the SPA (auth/me): the
// configured name, else a default from the key, profile and kind ("4K WEB", "Anime movies", "Anime series").
// Libraries that would get the same name are told apart by words, never by key: by kind when their kinds differ
// ("4K movies", "4K series"), then by number in key order ("4K", "4K (2)").
func LibraryNames(p *Policy) map[string]string {
	names := map[string]string{}
	if p == nil {
		return names
	}
	keys := slices.Sorted(maps.Keys(p.Versions))
	base := map[string]string{}
	kinds := map[string]map[Kind]bool{}
	for _, k := range keys {
		v := p.Versions[k]
		if v == nil {
			continue
		}
		l := strings.TrimSpace(v.Name)
		if l == "" {
			l = LibraryLabel(k, v.Profile, v.Kind)
		}
		base[k] = l
		if kinds[l] == nil {
			kinds[l] = map[Kind]bool{}
		}
		kinds[l][v.Kind] = true
	}
	n := map[string]int{}
	for _, k := range keys {
		l, ok := base[k]
		if !ok {
			continue
		}
		if len(kinds[l]) > 1 {
			l = strings.TrimSpace(l + " " + kindWord(p.Versions[k].Kind))
		}
		n[l]++
		if n[l] > 1 {
			l += " (" + strconv.Itoa(n[l]) + ")"
		}
		names[k] = l
	}
	return names
}

// kindWord is a library kind in a name: "movies", "series".
func kindWord(k Kind) string {
	switch k {
	case KindMovie:
		return "movies"
	case KindSeries:
		return "series"
	}
	return ""
}

// LibraryLabel is a library's default name from its key, profile and kind: movie-web with profile web-2160p is
// "4K WEB", tv-2160p is "4K"; an anime library is "Anime movies" or "Anime series" by kind (by key when the kind is
// not known). A key with no known word is its own name.
func LibraryLabel(key, profile string, kind Kind) string {
	words := map[string]string{"2160p": "4K", "4k": "4K", "uhd": "4K", "1080p": "1080p", "720p": "720p",
		"web": "WEB", "remux": "Remux", "bluray": "Blu-ray", "anime": "Anime", "hdr": "HDR"}
	var res, rest []string
	seen := map[string]bool{}
	add := func(tok string) {
		w, ok := words[tok]
		if !ok || seen[w] {
			return
		}
		seen[w] = true
		switch w {
		case "4K", "1080p", "720p":
			res = append(res, w)
		default:
			rest = append(rest, w)
		}
	}
	keyToks := strings.Split(strings.ToLower(key), "-")
	for _, t := range keyToks {
		add(t)
	}
	if !seen["Anime"] {
		for _, t := range strings.Split(strings.ToLower(profile), "-") {
			add(t)
		}
	}
	if seen["Anime"] {
		if kind == "" {
			switch {
			case slices.Contains(keyToks, "movie"):
				kind = KindMovie
			case slices.Contains(keyToks, "tv"), slices.Contains(keyToks, "series"):
				kind = KindSeries
			}
		}
		return strings.TrimSpace("Anime " + kindWord(kind))
	}
	out := strings.Join(append(res, rest...), " ")
	if out == "" {
		return key
	}
	return out
}
