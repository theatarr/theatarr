// Package profile is the quality half of the decision: the arr qualities and quality definitions, quality
// profiles (allowed qualities and groups, cutoff, upgrades, format scores, language), the decision specifications
// that judge one release against a profile and the files on disk, and the comparer that ranks releases.
//
// It ports Radarr src/NzbDrone.Core/{Qualities,Profiles/Qualities,DecisionEngine} @ a96bf7c and the Sonarr
// directories @ cab419a. The quality lists come from the parser ports; the format part of a profile is
// format.Profile, embedded in Profile. Import: arr API JSON (qualityprofile, qualitydefinition) and TRaSH JSON
// (quality profiles, quality sizes) with recyclarr's semantics.
//
// Ported specifications (Engine.Specs): AcceptableSize, CustomFormatAllowedbyProfile, Language (Radarr),
// MaximumSize, NotSample, QualityAllowedByProfile, RawDisk, Repack, AnimeVersionUpgrade (Sonarr), UpgradeAllowed,
// UpgradeDisk, with UpgradableSpecification and DownloadDecisionComparer; for Sonarr also the episode and pack
// specifications (FullSeason, MultiSeason, SplitEpisode, SameEpisodesGrab, SceneMapping, SeasonPackOnly,
// RssSync/MonitoredEpisode, Search/EpisodeRequested, Search/SeasonMatch, Search/SingleEpisodeSearchMatch; series.go),
// which read the mapped episodes, the search and the scene mapping on the Subject. Specifications that need live
// state (queue, blocklist, free space, delay profiles) are not here yet. AlreadyImported reads the supplied latest
// grab/import, TorrentSeeding the indexer's minimum, and RSS-only Proper the held files' added dates.
// EpisodeTitleCheck ports Sonarr's import-time title hold; it is not registered as a grab rule. The library import
// calls it with the naming requirement and whole-series episode dates and holds the download while it rejects.
//
// Config.AcceptReleaseAnyUpgradable ports the actuallyevan Sonarr fork's ACCEPT_RELEASE_ANY_UPGRADABLE (fork
// v4.0.20.3014-1 @ 9a75b78, UpgradeDiskSpecification only): a Sonarr release is accepted when any of its episodes has
// no file or a file it upgrades. Off, UpgradeDisk is plain Sonarr's. The decision engine turns it on for every series
// version.
//
// Deviations:
//   - Specifications are listed by class name. The arrs resolve them by assembly scanning; within a priority group
//     the order only decides the order of rejection messages.
//   - A panic in a specification becomes a DecisionError rejection, as an exception does in
//     DownloadDecisionMaker.EvaluateSpec. Where the arr would throw on a file id with no file loaded (Repack,
//     UpgradeDisk in Sonarr), we have no such state: a missing file is no file.
//   - Language identity, quality identity and format identity are by id, as in the arrs. Format messages list
//     the formats in the order given (the arr's is the calculation's name order).
//   - Config.PreferredProtocol stands for DelayProfileService.BestForTags(tags).PreferredProtocol: the caller
//     resolves the delay profile.
//   - Prioritize orders the releases of one title. Radarr's service also groups by movie and appends unmapped
//     releases; that is the caller's.
//   - Definitions.Get falls back to the default definition for a quality the snapshot lacks, as the arr inserts
//     missing definitions at startup.
//   - SizeSuffix computes the magnitude with Go's math.Log, which may differ from .NET's in the last bit; exact
//     powers of 1024 are tested.
//   - TRaSH import assumes the arrs' increased size limits (Radarr 2000/1999, Sonarr 1000/995 MB per minute),
//     builds a profile as recyclarr would in an empty arr from the guide alone (no recyclarr
//     config overrides, "top" quality sort, no preferred ratio), and ignores recyclarr's per-profile sizes, which
//     these arr versions do not have.
//
// Native opt-in: Config.PreferSeaDex puts QualityModel.SeaDexPreference before quality in ranking and upgrade
// checks among allowed qualities. Size and all other admission specifications still run;
// with the option off, the ported behavior is unchanged. Actual source and resolution are never rewritten.
package profile
