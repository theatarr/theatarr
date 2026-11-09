package profile

import (
	"testing"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// Sonarr places the cache step after the episode steps: at equal quality and score a season pack cached on the
// version's last provider still beats a single episode cached on all of them; between two single episodes of the
// same number the cache standing decides.
func TestSonarrCachedAfterEpisodeSteps(t *testing.T) {
	t.Parallel()
	e, pack := radarrSubject(t)
	_, single := radarrSubject(t)
	_, other := radarrSubject(t)
	e.Flavour = format.Sonarr
	for _, s := range []*Subject{pack, single, other} {
		s.Profile = pack.Profile
		s.Profile.Flavour = format.Sonarr
	}
	pack.Release.FullSeason = true
	for i := range 10 {
		pack.Episodes = append(pack.Episodes, &Episode{SeasonNumber: 1, EpisodeNumber: i + 1})
	}
	single.Episodes = []*Episode{{SeasonNumber: 1, EpisodeNumber: 1}}
	other.Episodes = []*Episode{{SeasonNumber: 1, EpisodeNumber: 1}}
	pack.Cached = CacheStanding{First: 3, Count: 1}
	single.Cached = CacheStanding{First: 1, Count: 3}
	if c := e.Compare(pack, single); c <= 0 {
		t.Errorf("season pack cached on one provider lost to a single episode cached on all: %d", c)
	}
	if c := e.Compare(single, other); c <= 0 {
		t.Errorf("single episode cached on all did not beat an uncached one: %d", c)
	}
}
