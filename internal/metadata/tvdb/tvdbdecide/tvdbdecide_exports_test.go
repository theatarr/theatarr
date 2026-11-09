package tvdbdecide

import (
	"context"
	"errors"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/policy"
	"strconv"
	"strings"
)

// Source is what Fill reads: *tvdb.Client.
type Source interface {
	anime.TVDB
	Series(ctx context.Context, id int) (*tvdb.Series, error)
}

// Fill completes a series title from TVDB where it lacks data: its episodes when it has none (each takes the
// title's Monitored), and its name, year, runtime and aliases (TVDB aliases and name translations) when empty. It
// reports whether it changed anything. A title without a TVDB id, or not a series, is left alone.
func Fill(ctx context.Context, c Source, t *decide.Title) (bool, error) {
	if t == nil || t.TVDB <= 0 || t.Kind != policy.KindSeries {
		return false, nil
	}
	changed := false
	if len(t.Episodes) == 0 {
		eps, err := Episodes(ctx, c, t.TVDB)
		if err != nil {
			return false, err
		}
		for i := range eps {
			eps[i].Monitored = t.Monitored
		}
		t.Episodes, changed = eps, len(eps) > 0
	}
	if t.Name != "" && len(t.Aliases) > 0 && t.Year != 0 {
		return changed, nil
	}
	s, err := c.Series(ctx, t.TVDB)
	if err != nil {
		if changed && !errors.Is(err, context.Canceled) {
			return true, nil // the episodes are what matters; names stay the arr's
		}
		return changed, err
	}
	if t.Name == "" {
		t.Name, changed = s.TranslatedName("eng"), true
	}
	if t.Year == 0 {
		if y, err := strconv.Atoi(s.Year); err == nil && y > 0 {
			t.Year, changed = y, true
		}
	}
	if t.Runtime == 0 && s.AverageRuntime != nil {
		t.Runtime, changed = *s.AverageRuntime, true
	}
	if len(t.Aliases) == 0 {
		seen := map[string]bool{strings.ToLower(t.Name): true}
		add := func(n string) {
			if k := strings.ToLower(strings.TrimSpace(n)); k != "" && !seen[k] {
				seen[k] = true
				t.Aliases = append(t.Aliases, strings.TrimSpace(n))
				changed = true
			}
		}
		add(s.Name)
		for _, a := range s.Aliases {
			add(a.Name)
		}
		if s.Translations != nil {
			for _, tr := range s.Translations.NameTranslations {
				add(tr.Name)
			}
		}
	}
	return changed, nil
}
