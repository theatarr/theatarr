// Package shadow is the live shadow runner (docs/design/shadow.md): it keeps the arr view fresh, re-decides every
// new arr grab with our engine, runs our own search for the same title, reads RSS at the lowest priority, and keeps
// the diff report. It grabs nothing and writes nothing outside our database.
//
// # Jobs
//
//   - arr.import (every shadow.import_interval): an incremental theatarr import-arr of every import.arr instance whose key
//     is set. New grabbed events after the instance's cursor become one shadow.grab job each. The first run only
//     sets the cursor, so old history is never searched.
//   - shadow.grab (priority -10): decides the arr's release for its version with the file the version had at grab
//     time, searches every indexer of the version through the planner (one search per title, reused for
//     shadow.search_reuse, at most shadow.searches_per_hour, one at a time, indexers with a spent budget or a
//     back-off skipped), decides every result, ranks them with the arr's release, and stores both decisions and the
//     diff row. Over the hourly cap or with every indexer failing, the job re-enqueues itself for later; after a day
//     it settles for the arr's release alone (searched = 0).
//   - shadow.rss (every shadow.rss_interval, priority -20): one RSS request per torznab feed the shadow versions
//     list (categories 2000 and 5000 as the versions need), SeaDex's recent feed, and one search without keywords per
//     catalog (cardigann) source (DMM, Torrentio and Zilean have no RSS). New items are decided for every monitored version that
//     is wanted or below its cutoff; accepted ones are RSS picks. An arr grab of the same version links them; a pick
//     no grab follows within shadow.link_window becomes a "we'd grab, arr didn't" row.
//
// # The diff report
//
// One row per arr grab, in three buckets, and one per unlinked RSS pick. A grab is compared by its release source
// (ClassifyGrab, docs/design/shadow.md): an RSS grab against our RSS picks within RSSWindow of it, a hand grab
// (InteractiveSearch) against our decision of the arr's release alone, any other grab against our search. On RSS
// and hand grabs our later search is kept as search_found_better, information that does not move the bucket.
//
//   - agree: our pick is the arr's release, or one with the same quality and score; or we accept an RSS grab nothing
//     of ours ranked above, or a hand grab;
//   - we_grab_another: our pick ranks above the arr's release (better quality, higher score, or a release published
//     after the grab);
//   - arr_grabbed_we_wouldnt: our engine rejects the arr's release; the reason is the first rejection's category
//     (manual_grab_rejected for a hand grab);
//   - we_grab_arr_didnt: an RSS pick no arr grab followed.
//
// Stored grabs are classified again at start (Reclassify) from their stored decisions, without a search.
//
// Agreement is agree / (agree + we_grab_another + arr_grabbed_we_wouldnt) over the grabs of the last 14 days, overall
// and per kind (Summary.Kinds): movies and series each have the 95% bar.
//
// # Series
//
// Sonarr writes one grabbed event per episode; the rows of one release (same series, download id and title, within
// two minutes) are one shadow.grab, keyed by the first row. The arr's release is decided with the search the arr had
// (its episodes; none for an RSS grab), against each episode's file at grab time (rebuilt from history, else the
// version_file the import stored if the arr added it before the grab). Our search asks for the grab's seasons
// (tvsearch by TVDB id), and every result is decided for the grab's episodes. RSS items in a TV category are mapped
// by TVDB id or by the parsed series title against the titles and scene mappings, and decided per series version.
// An RSS pick is linked to a later arr grab of the same title and version, whatever its episodes.
package shadow
