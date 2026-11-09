package cardigann

// Not in Mediarium: whether a definition can feed the RSS sync.

import "fmt"

// rssCategories are the Newznab categories the RSS sync asks of a feed (movies, TV).
var rssCategories = []int{2000, 5000}

// RSSProblem says why a definition cannot feed the RSS sync, or "" when it can. As in Prowlarr, a definition's RSS is
// its search without keywords: the definitions pick their "latest" page themselves when .Keywords is empty (TPB's
// top-100 recent list, Nyaa's first two pages, TheRARBG's last ten days). There is no offset to page further back.
// What rules a definition out: one the engine can't run (usenet included), a site with no movie or TV category or no
// search path for them, and search templates that fail without keywords.
func RSSProblem(def *Definition) string {
	if p := def.Problems(); len(p) > 0 {
		return "the engine can't run this definition"
	}
	siteCats := def.siteCategories(rssCategories)
	if len(siteCats) == 0 {
		return "the site maps no movie or TV category"
	}
	wanted := false
	for _, p := range def.Search.Paths {
		wanted = wanted || pathWanted(p, siteCats)
	}
	if !wanted {
		return "no search path covers movies or TV"
	}
	if err := renderEmptySearch(def, siteCats); err != nil {
		return fmt.Sprintf("its search does not render without keywords: %v", err)
	}
	return ""
}

// renderEmptySearch runs the definition's keyword filters and search templates with no keywords and the default
// settings, as an RSS read would (no request is made).
func renderEmptySearch(def *Definition, siteCats []string) error {
	base := "https://site.invalid/"
	if len(def.Links) > 0 {
		base = def.Links[0]
	}
	s, err := newSession(&Manager{}, def, Instance{Name: def.ID, BaseURL: base})
	if err != nil {
		return err
	}
	vars := s.vars()
	vars.Query["Q"] = ""
	vars.Query["Keywords"] = ""
	if vars.Keywords, err = applyFilters("", def.Search.KeywordsFilters, vars); err != nil {
		return fmt.Errorf("keywords: %w", err)
	}
	vars.Categories = siteCats
	for _, p := range def.Search.Paths {
		if !pathWanted(p, siteCats) {
			continue
		}
		if _, err := applyTemplate(p.Path, vars, urlPathEscape); err != nil {
			return fmt.Errorf("path: %w", err)
		}
		for _, k := range p.Inputs.Keys {
			if _, err := applyTemplate(p.Inputs.Values[k], vars, nil); err != nil {
				return fmt.Errorf("input %s: %w", k, err)
			}
		}
	}
	for _, k := range def.Search.Inputs.Keys {
		if _, err := applyTemplate(def.Search.Inputs.Values[k], vars, nil); err != nil {
			return fmt.Errorf("input %s: %w", k, err)
		}
	}
	return nil
}
