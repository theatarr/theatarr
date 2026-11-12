package importarr

import (
	"context"
	"fmt"
)

// ReadTitles reads one Radarr or Sonarr instance for a one-time import: its version, root folders, quality profiles
// and titles with their files (and, for Sonarr, episodes), in the same shape FetchRadarr/FetchSonarr fill. It reads
// nothing else (no tags, custom formats, naming, delay or release profiles, collections, history or blocklist).
// progress, when not nil, is called per series (Sonarr) or once at the end (Radarr).
func ReadTitles(ctx context.Context, g Getter, flavour string, progress func(done, total int)) (*Snapshot, error) {
	var app string
	switch flavour {
	case "radarr":
		app = "Radarr"
	case "sonarr":
		app = "Sonarr"
	default:
		return nil, fmt.Errorf("unknown arr flavour %q", flavour)
	}
	s := &Snapshot{Flavour: flavour, Tags: map[int]string{}}
	if err := readStatus(ctx, g, s, app); err != nil {
		return nil, err
	}
	if err := readRootFolders(ctx, g, s); err != nil {
		return nil, err
	}
	if _, err := readProfiles(ctx, g, s); err != nil {
		return nil, err
	}
	if flavour == "sonarr" {
		if err := readSonarrSeries(ctx, g, s, progress); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err := readRadarrMovies(ctx, g, s); err != nil {
		return nil, err
	}
	if progress != nil {
		progress(len(s.Titles), len(s.Titles))
	}
	return s, nil
}
