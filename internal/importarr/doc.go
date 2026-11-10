// Package importarr reads arr instances (GET only) into our database: `theatarr import-arr` (docs/migration.md).
//
// # What is imported
//
// Per instance (policy import.arr names the instance, its URL, key variable and version):
//   - system/status, root folders, tags and quality profiles (name, cutoff, format scores), for reference;
//   - every movie: our Title by TMDb id (one Title however many instances hold it), a Version with the instance's
//     policy key, the arr's record of it (arr_title: monitored, profile, path, tags, file id), release dates, the
//     TMDb collection, and the Seerr alias (arr_alias movie and moviefile ids);
//   - the current file per movie (version_file), from GET moviefile, tied to its import event, download id,
//     the grab's release title and the registry content;
//   - every series (Sonarr): our Title by TVDB id with its series type, scene numbering flag, runtime and original
//     language; the arr's scene mappings (alternateTitles, title_scene_mapping); every episode (episode, by season
//     and number) with its absolute and scene numbers (episode_number), the Seerr alias of each episode, and the
//     version's per-episode state (episode_version_state); every episode file (version_file, one per arr file) with
//     the episodes it holds (version_file_episode) and its release type;
//   - the whole history, sanitised (arr_history), newest first until a page is all known;
//   - the registry seeded from history: a content per torrent info hash and a content_version per hash and version
//     with the grab's release title and the first grab, import and failure.
//
// # Mapping rules
//
//   - Title: matched on (kind, TMDb id). A movie without a TMDb id is unmapped; with --enrich, an IMDb id is
//     looked up through TMDb find first. Title fields are only filled when empty, never overwritten; a different
//     IMDb id is reported. title.monitored is set when any of its versions is monitored.
//   - Version state: no file is wanted; a file with qualityCutoffNotMet is filled; any other file is cutoff_met.
//     Version monitored is the movie's monitored flag. A series version is wanted while a monitored episode that has
//     aired has no file (or no episode has one), filled while a file is below the cutoff, else cutoff_met; each
//     episode has the same three states from its file.
//   - Series: matched on TVDB id. The series type is recorded per instance (arr_title.series_type); the one every
//     reader uses is title.series_type, the first import's when the title had none. Episodes are shared
//     by the title's versions; an episode no instance lists any more (and no link holds) is deleted. Scene mappings
//     and episode numbers carry the instance as source and are replaced per instance.
//   - Ownership: the first instance to import a version owns its state (version_source). Another instance mapped to
//     the same version still gets its arr_title and version_file rows, but does not change the version; differing
//     files are reported by [Conflicts].
//   - Info hash: data.torrentInfoHash, else a 40-hex download id (qBittorrent-type clients and decypharr use the
//     hash), never for a usenet grab. Usenet download ids are kept as download ids only.
//   - Release title of a file: the source title of the first grab with the file's download id; for a manual import
//     without a download id (a script's), the import event's source title.
//   - Idempotence: every write compares first and changes only what differs; a re-run with nothing new upstream
//     changes no row (one import_run row records each run). Registry rows merge (earliest dates, first title), so
//     two instances converge whatever order they run in. Titles gone from the arr lose their arr_title,
//     version_file and version_source rows; our Title and Version stay.
//
// # Deviations from the design
//
//   - Existing files are stored as version_file rows, not as Link rows: a Link needs the symlink target, which
//     only the library scan (readlink) can give. version_file.path is the key the scan joins on.
//   - Grabs from history are not stored as Release, Download and Decision rows: those need indexer GUIDs, which
//     are dropped as possible secrets. arr_history holds what the shadow re-scoring needs (title, indexer, size,
//     quality, the arr's custom format score).
//   - Content rows from history get state 'unknown' (not 'present'): nothing has looked at a provider yet.
//
// import_run keeps a week of runs plus each instance's latest ([PruneRuns], from theatarr serve's nightly job).
package importarr
