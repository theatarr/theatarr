package animemap

import (
	"strconv"
	"time"
)

// RelinkJobKind is the job that moves a series' links whose episodes changed because a franchise mapping became
// importable (docs/design/anime.md "Re-link after review"): animestore queues it when review accepts a mapping or a
// build turns one importable; the library runs it.
const RelinkJobKind = "anime_relink"

// RelinkDelay lets several accepts in a row join one job.
const RelinkDelay = time.Minute

// RelinkPayload names the TVDB series to re-check.
type RelinkPayload struct {
	TVDBID int `json:"tvdb_id"`
}

// RelinkDedupeKey is one job per series.
func RelinkDedupeKey(tvdbID int) string { return RelinkJobKind + "/tvdb:" + strconv.Itoa(tvdbID) }
