// Package decide is the decision engine's one call (docs/design/decide.md): a release, a title, one of its versions and the
// version's current file in; a Decision out (accepted or the rejections with the arr's reason names and messages,
// quality, matched formats with each one's score, total score, and whether and why it upgrades the file).
//
// [New] builds the policy's profiles into engine profiles: a policy profile's qualities (best first), cutoff,
// scores and sizes, with its formats from TRaSH (by trash_id) or arr-shaped JSON files; or a whole arr profile
// (from_arr: <instance>/<profile>) with that instance's formats, quality definitions and settings, for shadow
// parity. Problems come back as *policy.Errors with their policy paths.
//
// [Engine.Decide] runs, for a movie version, what Radarr's DownloadDecisionMaker does for a searched movie:
//   - parse with the Radarr port (no title: UnableToParse);
//   - map to the title (movie.go: indexer IMDb id, then TMDb id, then the search-criteria title match with the
//     arr's year tolerance; no match: UnknownMovie; an id naming another title of ours: WrongMovie);
//   - IndexerBase.CleanupReleases' multi-language step, then the format input with AggregateLanguages' indexer
//     branch (adapt.RadarrRelease), with the release's size and indexer flags;
//   - every format of the profile's set, the profile score, and profile.Engine.Evaluate with the existing file;
//   - our own rule, which the arrs do not have: the version's indexer list (IndexerNotInVersion). It is listed
//     first and marked Ours.
//
// [Rank] orders decisions per version with the arr's comparer (accepted first, best first).
//
// For a series version it runs Sonarr's DownloadDecisionMaker (series.go, episodes.go): parse with the Sonarr port
// (with ParseSpecialEpisodeTitle's fallback for specials), map with ParsingService.Map (scene mapping, series by
// title, all titles, title and year, the indexer's TVDB or IMDb id; episodes by season and number, scene numbering,
// absolute numbers for anime, air date for daily series, whole seasons for packs; UnknownSeries, UnknownEpisode),
// aggregate languages (adapt.SonarrRelease, with the episode titles), score, and run the Sonarr specifications with
// every mapped episode's file: a season pack is judged against every episode's existing file, as the arr does.
// [Engine.DecideSearch] passes the search criteria (the requested episodes, the kind of search) that the arr's
// search specifications read; Decide is the RSS path.
//
// Deviations:
//   - The per-version PreferSeaDex setting opts into curator preference before quality order, for known-quality
//     releases. It may replace higher-resolution files, while preserving real quality and other admission guards.
//     The same preference applies at import; zero/default settings preserve the arr quality-first behavior.
//   - Movie decisions with a known size and runtime-based limits wait with a temporary UnknownRuntime rejection
//     until real runtime is available. The lower-level Radarr port retains its 110-minute fallback for parity.
//   - Manual import previews reapply explicit title/episode, quality/revision, language, group and flag choices
//     before scoring and profile checks. Final manual imports bypass upgrade specifications, as the arrs do.
//   - Mapping ports TryGetMovieBySearchCriteria (a decision is always for one title), not the RSS path
//     (MovieService.FindByTitle over the library). The arr's last IMDb comparison there compares the indexer's
//     integer id with the movie's "tt…" id and never matches; that is kept.
//   - A release mapped to another title is rejected as WrongMovie only; the arr would also score it against that
//     title's profile.
//   - Title data: alternative titles and translations (Title.Aliases) are the union of what the arrs' movie
//     resources and TMDb (alternative_titles, translated titles) give, from title_alias; the arr's come from its
//     metadata server, which takes them from TMDb too, so the two mostly overlap. The secondary year is the arr's
//     (title.secondary_year); TMDb has none. A title only TMDb knows has no secondary year.
//   - Policy profiles: quality items are organized from the arr's default profile as recyclarr does (unlisted
//     qualities stay listed, not allowed); the profile language is the policy's (Radarr: any by default, original,
//     or a language); sizes override the arr's default quality definitions for that profile only (the arrs have
//     one global table).
//   - A movie's original language is TMDb's original_language from the cached TMDb answer, mapped as Radarr's
//     SkyHookProxy does (IsoLanguages.Find, else English); without a cached answer, the arr's name import-arr
//     stored (title.original_language); with neither it is Unknown, where Radarr's would be English (the
//     MovieMetadata default): an "original" profile then rejects every release with a known language rather
//     than taking an English one for a film that may not be English.
//   - A series' original language is the arr's name import-arr stored (title.original_language); without one, the
//     metadata's code (title_meta.original_language) mapped as Sonarr does (IsoLanguages.Find, else English).
//   - The engine settings (propers, maximum size, prefer indexer flags) are the arr defaults, except propers when
//     the policy profile sets it; a from_arr profile takes the instance's config when the snapshot has it.
//   - Indexer flags are computed from torznab tags and volume factors as TorznabRssParser.GetFlags does.
//   - The existing file's formats follow ParseCustomFormat(MovieFile): release title from the scene name, else the
//     original file name, else the relative path's file name; an empty group or edition counts as none.
//   - AlreadyImported uses the version's latest grab and matching import from Theatarr and imported arr history;
//     followers use only their own version's history. Arr history retains quality names, not revisions, so
//     revision-only grab/import differences cannot be reconstructed there.
//   - Movie search callers use WithSearch to skip RSS-only Proper; series use DecideSearch. Minimum availability
//     is enforced by acquisition and release routing, except for interactive/user-invoked searches and grabs.
//   - Deferred arr specifications (queue, blocklist and the rest listed in docs/design/profiles.md) are not run.
//     Delay and release profiles are not supported at all: a typical arr setup uses
//     none, and theatarr import-arr warns if an arr gains one.
//
// Series deviations (docs/design/series.md):
//   - Sonarr's import-time EpisodeTitle hold is applied before download as a temporary rejection. It reads the
//     selected version's standard/daily/anime naming; missing from_arr naming snapshots skip the check.
//   - The library is the one series under decision. A release naming another series is UnknownSeries, where the
//     arr would map it there and reject it as WrongSeries or MatchesAnotherSeries; the verdict is the same.
//   - Scene mappings are what Sonarr's API exposes (alternateTitles): no filter regex, no mapping type. Mappings
//     with a filter regex (comment "Limited matching") are left out; the XemService season-1 rule cannot apply.
//     TheXEM's scene numbers come through the episodes (scene season, episode, absolute) as the arr has them.
//   - The arr's SingleOrDefault exceptions (two episodes with one absolute number, two with one air date) become
//     its "Unexpected error processing release" rejection (reason Error).
//   - Episode title matching for specials compares lower-cased text; the arr uses the current culture.
package decide
