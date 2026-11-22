package arrview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
)

// PreviewRename and ApplyRename expose the library rename operation to the native web API.
func (s *Service) PreviewRename(ctx context.Context, titleID int64, season *int) (*library.RenamePreview, error) {
	rl, ok := s.library.(renameLibrary)
	if !ok {
		return nil, refused("the library rename operation is unavailable")
	}
	return rl.PreviewRename(ctx, titleID, season)
}

func (s *Service) ApplyRename(ctx context.Context, titleID int64, season *int) (*library.RenamePreview, error) {
	rl, ok := s.library.(renameLibrary)
	if !ok {
		return nil, refused("the library rename operation is unavailable")
	}
	return rl.ApplyRename(ctx, titleID, season)
}

type reassignLibrary interface {
	ReassignEpisodes(ctx context.Context, linkID int64, episodeIDs []int64, replace bool) (*library.Reassigned, error)
}

// ReassignEpisodes exposes the library's episode correction of one link (an admin's "Change episodes") to the
// native web API.
func (s *Service) ReassignEpisodes(ctx context.Context, linkID int64, episodeIDs []int64, replace bool) (*library.Reassigned, error) {
	rl, ok := s.library.(reassignLibrary)
	if !ok {
		return nil, refused("the library episode change is unavailable")
	}
	return rl.ReassignEpisodes(ctx, linkID, episodeIDs, replace)
}

type factsLibrary interface {
	EditFileFacts(ctx context.Context, linkID int64, e library.FileFactsEdit) (*library.FileFactsEdited, error)
}

// EditFileFacts exposes the library's correction of an installed file's facts (a manager's "Edit file facts") to the
// native web API.
func (s *Service) EditFileFacts(ctx context.Context, linkID int64, e library.FileFactsEdit) (*library.FileFactsEdited, error) {
	fl, ok := s.library.(factsLibrary)
	if !ok {
		return nil, refused("editing file facts is unavailable")
	}
	return fl.EditFileFacts(ctx, linkID, e)
}

type undoLibrary interface {
	UndoOffers(ctx context.Context, ids []int64) (map[int64]string, error)
	UndoUpgrade(ctx context.Context, actionID int64, by string) (*library.Undone, error)
}

// UndoOffers and UndoUpgrade expose the library's Undo of a History replacement (library return.go) to the native
// web API; without the library none is offered.
func (s *Service) UndoOffers(ctx context.Context, ids []int64) (map[int64]string, error) {
	ul, ok := s.library.(undoLibrary)
	if !ok {
		return map[int64]string{}, nil
	}
	return ul.UndoOffers(ctx, ids)
}

func (s *Service) UndoUpgrade(ctx context.Context, actionID int64, by string) (*library.Undone, error) {
	ul, ok := s.library.(undoLibrary)
	if !ok {
		return nil, refused("Undo is unavailable")
	}
	return ul.UndoUpgrade(ctx, actionID, by)
}

// Operator actions for the web UI (docs/design/ui.md "Recovery actions"), on the same parts the arr views' grab, queue
// and file endpoints use: the decision engine, the router and the library. They work on one title's versions
// directly, not through a view. internal/web calls them (web.Ops) and records each in the journal.
//
// Nothing here talks to a provider: a grab and a retry go to the router, which writes only for an active version
// while router.enabled is true and checks the blocklist on every candidate; an unlink moves only one of our
// own symlinks aside through the library's gate. Debrid torrents are never deleted (they are shared by every
// library), and no regular file is ever touched.

// Refused is an operator action that was not done, with the reason in words. Web answers it 409.
type Refused struct {
	Reason string
	// Block is set when the only refusal is the library's blocklist: a grab by hand may take the release off it.
	Block *Block
}

// Block is the blocklist row that refuses a grab by hand: the library (its key and name) and why it was blocked.
type Block struct {
	Version, Library, Source, Reason string
}

// BlockedBy says the blocklist row behind a refusal (web answers it 409 with the library and the reason).
func (e *Refused) BlockedBy() (library, source, reason string, ok bool) {
	if e.Block == nil {
		return "", "", "", false
	}
	return e.Block.Library, e.Block.Source, e.Block.Reason, true
}

func (e *Refused) Error() string { return e.Reason }

// Refused marks the error for callers that cannot import this package (errors.As with an interface).
func (e *Refused) Refused() bool { return true }

func refused(format string, args ...any) error { return &Refused{Reason: fmt.Sprintf(format, args...)} }

// Grabbed is what GrabRelease did.
type Grabbed struct {
	// Decision is the release decided again now; Overridden is true when it was rejected and grabbed anyway.
	Decision   *decide.Decision
	Overridden bool
	Job        int64
}

// GrabRelease routes one release a live search found for a version of a title (the release panel's Grab, as POST
// release does for the arr clients). The release is decided again now for the version (so the answer reflects the
// file and blocklist as they are, not as they were at the search); a rejected one is grabbed only with override, as
// the arrs let a person grab a rejected release from an interactive search. The router then applies every rule it
// applies to any route: the gate (router.enabled), the blocklist, pacing and budgets. A release on the version's
// blocklist is refused with its Block unless unblockBy names who confirmed the grab: then, when nothing else refuses
// it, the release is taken off the blocklist (routeByHand) as the route is queued. whole: a release of the
// whole-series Interactive search, which an anime series decides again with that search's criterion (the criterion
// travels on the candidate); any other series and a movie as from any search.
func (s *Service) GrabRelease(ctx context.Context, titleID int64, version string, rel indexer.Release, override, whole bool,
	unblockBy, source string) (*Grabbed, error) {
	if s.pol.Indexers[rel.Indexer] == nil {
		return nil, refused("release indexer is no longer enabled: %s", rel.Indexer)
	}
	return s.grab(ctx, titleID, version, rel, override, unblockBy, source, whole)
}

// AddRelease routes a release a person names by its hash for a version of a title (the release panel's "Add by
// magnet or hash"): a release no search found, so it has no indexer and carries policy.ManualIndexer in its place
// (the adders refuse a magnet that names no source). It is GrabRelease without the indexer check and nothing else:
// decided now for the version, refused when rejected unless override, then the router's own rules (gate, blocklist,
// provider order, pacing, cached-only, the record of our own adds); a blocklisted one as in GrabRelease.
func (s *Service) AddRelease(ctx context.Context, titleID int64, version string, rel indexer.Release, override bool,
	unblockBy, source string) (*Grabbed, error) {
	rel.Indexer, rel.Kind, rel.Privacy = policy.ManualIndexer, "", ""
	return s.grab(ctx, titleID, version, rel, override, unblockBy, source, false)
}

func (s *Service) grab(ctx context.Context, titleID int64, version string, rel indexer.Release, override bool,
	unblockBy, source string, whole bool) (*Grabbed, error) {
	if s.decider == nil {
		return nil, refused("the decision engine is not running")
	}
	if s.router == nil {
		return nil, refused("the router is not running")
	}
	if indexer.NormalizeHash(rel.InfoHash) == "" {
		return nil, refused("the release has no info hash: only magnets by hash are routed")
	}
	var d *decide.Decision
	var ss *decide.SeriesSearch
	var err error
	if wd, ok := s.decider.(WholeDecider); whole && ok {
		d, ss, err = wd.DecideWhole(ctx, decide.Release{Release: rel}, titleID, version)
	} else if whole {
		return nil, refused("the decision engine cannot decide a whole-series release")
	} else {
		d, err = s.decider.Decide(ctx, decide.Release{Release: rel}, titleID, version)
	}
	if err != nil {
		return nil, err
	}
	// A release added by hand is known by its name alone: one that maps to no episode of a series cannot be routed,
	// with or without override, so it is refused here, before its rejections are offered for an override. Usually
	// it is a season pack whose name carries no season (the parser cannot read it).
	if vp := s.pol.Versions[version]; policy.IsManualAdd(rel.Indexer) && vp != nil && vp.Kind == policy.KindSeries &&
		len(d.EpisodeIDs) == 0 {
		why := ""
		if len(d.Rejections) > 0 {
			why = " (" + rejectionText(d) + ")"
		}
		return nil, refused("the release name matches no episode of the title%s. The season belongs in the "+
			"release name: name a season pack with its season (Show.S02.1080p...) and an episode with its number (S02E05)", why)
	}
	out := &Grabbed{Decision: d}
	if !d.Accepted {
		if !override {
			return out, refused("rejected for %s: %s", version, rejectionText(d))
		}
		out.Overridden = true
	}
	if d.VersionID == 0 {
		return out, refused("the title has no %s copy", version)
	}
	if err := s.refuseDryRunRoute(version); err != nil {
		return out, err
	}
	c := router.Candidate{Release: rel, Quality: d.Quality, Score: d.Score, Episodes: d.DownloadEpisodes(),
		Formats: append([]decide.FormatScore{}, d.Formats...), EpisodeMap: d.EpisodeMapping()}
	if ss != nil {
		// Decided with the whole-series criterion: a search's candidate, which a re-decision decides with it again.
		c.Search, c.Criteria = true, []*decide.SeriesSearch{ss}
	}
	if d.Pack.Span() {
		c.Release = decide.ClearSeaDex(c.Release) // a pack of several seasons travels without SeaDex standing
	}
	if vp := s.pol.Versions[version]; len(c.Episodes) == 0 && vp != nil && vp.Kind == policy.KindSeries {
		return out, refused("the release matches no episode of the title")
	}
	// A grab by hand asks the providers again: stored negative cache evidence from before now skips none.
	req := router.Request{TitleID: titleID, VersionID: d.VersionID, Version: version,
		Candidates: []router.Candidate{c}, Source: source, ManualSince: s.now()}
	// What the route would skip without opening a download is said now, as a plain refusal (no rejections: nothing
	// to grab anyway).
	out.Job, err = s.routeByHand(ctx, req, unblockBy)
	if err != nil {
		var ref *Refused
		if errors.As(err, &ref) {
			return nil, err
		}
		return out, fmt.Errorf("arrview: routing %s for %s: %w", rel.Title, version, err)
	}
	s.log.Info("release grabbed by hand", "title", titleID, "version", version, "release", rel.Title,
		"accepted", d.Accepted, "job", out.Job, "source", source)
	return out, nil
}

// routeByHand queues the route of a grab by hand (req, one candidate) after the route's own screens that open no
// download (router.ScreenManual): one that would open none is refused in words and nothing changes. The blocklist
// comes last, so only a grab nothing else refuses meets it: refused with its Block when unblockBy is "", otherwise
// taken off the blocklist (both its hash and its title-key rows), recorded as an unblock by unblockBy, in the
// transaction that queues the route.
func (s *Service) routeByHand(ctx context.Context, req router.Request, unblockBy string) (int64, error) {
	var profiles router.ProfileFunc
	if s.engine != nil {
		profiles = s.engine.Profile
	}
	u, err := router.ScreenManual(ctx, s.st, s.pol, profiles, s.now, req)
	if err != nil {
		return 0, err
	}
	switch {
	case u == nil:
		return s.router.Enqueue(ctx, req, time.Time{})
	case u.Block == nil:
		return 0, refused("%s", unstartableWords(s.pol, req.Version, u))
	case unblockBy == "":
		return 0, s.blockRefusal(u.Block)
	}
	rel := req.Candidates[0].Release
	var job int64
	err = s.st.WriteTx(ctx, func(tx *sqlTx) error {
		b, err := router.UnblockRelease(ctx, tx, req.Version, indexer.NormalizeHash(rel.InfoHash), rel.Title)
		if err == nil {
			_, err = journal.RecordAction(ctx, tx, journal.Action{Action: journal.ActUnblock, TitleID: b.TitleID,
				Version: b.Version, InfoHash: b.Hash, Release: b.Release, DownloadID: b.DownloadID, Target: b.Hash,
				Detail: "was blocked by " + b.Source + ": " + b.Reason + "; taken off by a grab by hand", By: unblockBy,
				At: s.now()})
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		job, err = s.router.EnqueueTx(ctx, tx, req, time.Time{})
		return err
	})
	if err == nil {
		s.log.Info("release unblocked by a grab by hand", "version", req.Version, "hash", u.Block.Hash, "by", unblockBy)
	}
	return job, err
}

func unstartableWords(pol *policy.Policy, version string, u *router.Unstartable) string {
	lib := policy.LibraryNames(pol)[version]
	switch u.Code {
	case "in_flight":
		return "Already downloading: " + u.Release
	case "upgrades_off":
		_, files, _ := strings.Cut(u.Message, ": ")
		return "Upgrades are off for " + lib + ": " + files
	}
	return "Not started: " + u.Message
}

// blockRefusal is the refusal of a grab by hand of a release on version's blocklist (b), with the row's library and
// reason.
func (s *Service) blockRefusal(b *router.Blocked) error {
	lib := policy.LibraryNames(s.pol)[b.Version]
	if lib == "" {
		lib = b.Version
	}
	return &Refused{Reason: "On " + lib + "'s blocklist: " + b.Reason,
		Block: &Block{Version: b.Version, Library: lib, Source: b.Source, Reason: b.Reason}}
}

// rejectionText joins a decision's rejection messages.
func rejectionText(d *decide.Decision) string {
	var out []string
	for _, r := range d.Rejections {
		m := r.Message
		if m == "" {
			m = r.Reason
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return "no reason given"
	}
	return strings.Join(out, "; ")
}

// Retried is what RetryDownload did.
type Retried struct {
	TitleID int64
	Version string
	Hash    string
	Release string
	// What is the outcome in words.
	What string
	// Outcome is what an import now did: imported, partial (some files linked, some rejected or not
	// matched), rejected (the download failed and is blocklisted), held (left for review), waiting (for the
	// mount) or dry_run (the gate refused the writes). Empty for a route.
	Outcome string
	Job     int64
}

// Import outcomes of Retried.Outcome.
const (
	ImportImported = "imported"
	ImportPartial  = "partial"
	ImportRejected = "rejected"
	ImportHeld     = "held"
	ImportWaiting  = "waiting"
	ImportDryRun   = "dry_run"
)

// importOutcome says what an import now did, from its plan and what the library recorded for the download: a plan
// with no links was rejected (the download failed) or held for review (it stays ready).
func (s *Service) importOutcome(ctx context.Context, id int64, p *library.Plan, out *Retried) error {
	out.Outcome, out.What = ImportImported, "imported"
	if p == nil {
		return nil
	}
	if p.Waiting {
		out.Outcome, out.What = ImportWaiting, "waiting for the files on the mount"
		return nil
	}
	if p.DryRun {
		out.Outcome, out.What = ImportDryRun, "planned only (dry run): "+p.Refused
		return nil
	}
	var imp, why, state string
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT COALESCE(li.state, ''), COALESCE(li.last_error, ''), d.state
		FROM download d LEFT JOIN library_import li ON li.download_id = d.id WHERE d.id = ?`, id).
		Scan(&imp, &why, &state); err != nil {
		return err
	}
	switch {
	case imp == "rejected" && state == router.StateFailed:
		out.Outcome, out.What = ImportRejected, "rejected, the release is blocklisted: "+why
	case imp == "rejected":
		out.Outcome, out.What = ImportHeld, "held for review: "+why
	case len(p.Links) > 0 && (len(p.Rejected) > 0 || len(p.Unmatched) > 0):
		out.Outcome = ImportPartial
		out.What = fmt.Sprintf("partly imported: %d linked, %d not", len(p.Links), len(p.Rejected)+len(p.Unmatched))
	}
	return nil
}

// RetryDownload tries a download again. A failed download's release is routed again for its version (a new
// download row; refused while the router is disabled or when the route would open no download (the grab's screens:
// upgrades off, in flight, the client-indexer rule), and while the release is blocklisted unless unblockBy names who
// confirmed taking it off: then, when nothing else refuses it, it is taken off as in GrabRelease), using
// the stored hash, never a link. A download that finished but is not linked yet
// (ready, importing) is imported now, without waiting for the import's back-off. Anything still on its way is
// refused: there is nothing to retry.
func (s *Service) RetryDownload(ctx context.Context, id int64, unblockBy, source string) (*Retried, error) {
	var state, version, hash, rel string
	var titleID int64
	var removed bool
	err := s.st.Reader().QueryRowContext(ctx, `SELECT d.state, d.title_id, d.version_key, d.info_hash, d.release_title,
		EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = d.version_id)
		FROM download d WHERE d.id = ?`, id).Scan(&state, &titleID, &version, &hash, &rel, &removed)
	if err != nil {
		return nil, err
	}
	out := &Retried{TitleID: titleID, Version: version, Hash: hash, Release: rel}
	switch state {
	case router.StateReady, router.StateImporting:
		if s.library == nil {
			return out, refused("the library is not available")
		}
		p, err := s.library.Import(ctx, id, false)
		switch {
		case errors.Is(err, library.ErrMountDown):
			return out, refused("the debrid mount is not healthy: the import waits for it")
		case errors.Is(err, library.ErrRootDown):
			return out, refused("a library root does not answer: the import waits for it (%v)", err)
		case errors.Is(err, library.ErrClaimed):
			return out, refused("an import of this download is running now")
		case err != nil:
			return out, err
		}
		return out, s.importOutcome(ctx, id, p, out)
	case router.StateFailed:
		// A copy removed since the download failed is not fetched again (the interactive Grab's refusal).
		if removed {
			return out, refused("the title was removed from library %q: add it there again first", version)
		}
	default:
		return out, refused("nothing to retry: the download is %s and still on its way", state)
	}
	if s.router == nil {
		return out, refused("the router is not running")
	}
	if err := s.refuseDryRunRoute(out.Version); err != nil {
		return out, err
	}
	// The release as the download stored it, as the router rebuilds a download it resumes (release_data: cache
	// evidence and extras order the providers alike). It is routed as a new download by hand: no download id,
	// decided under the running policy.
	req, err := router.DownloadRequest(ctx, s.st.Reader(), s.pol, id)
	if err != nil {
		return out, err
	}
	if req == nil {
		return out, sql.ErrNoRows
	}
	// The version's indexer list is the user's choice of where releases come from: a release of an indexer the
	// version no longer lists is not routed again.
	// A release added by hand (AddRelease) came from no indexer and is not held to the list, as in the decision
	// engine's rule.
	if vp := s.pol.Versions[out.Version]; vp != nil && len(req.Candidates) > 0 && len(vp.Indexers) > 0 &&
		!policy.IsManualAdd(req.Candidates[0].Release.Indexer) && !slices.Contains(vp.Indexers, req.Candidates[0].Release.Indexer) {
		return out, refused("indexer %s is not one of version %s's indexers", req.Candidates[0].Release.Indexer, out.Version)
	}
	req.DownloadID, req.DecisionRev, req.Source, req.ManualSince = 0, "", source, s.now()
	// The grab's screens (routeByHand): what the route would skip is said now, and the blocklist last, so a retry
	// nothing else refuses may take the release off it.
	out.Job, err = s.routeByHand(ctx, *req, unblockBy)
	if err != nil {
		var ref *Refused
		if errors.As(err, &ref) {
			return out, err
		}
		return out, fmt.Errorf("arrview: retrying download %d: %w", id, err)
	}
	out.What = "routed again"
	s.log.Info("download retried by hand", "download", id, "version", out.Version, "release", rel, "job", out.Job,
		"source", source)
	return out, nil
}

// refuseDryRunRoute keeps operator actions from reporting a successful route when the router can only plan.
func (s *Service) refuseDryRunRoute(version string) error {
	if s.pol == nil {
		return refused("no policy is loaded; no download will start")
	}
	if !s.pol.RouterSettings().Enabled {
		return refused("Theatarr is paused; no download will start")
	}
	if s.pol.Versions[version] == nil {
		return refused("there is no active policy for %s; no download will start", version)
	}
	return nil
}

// Unlinked is what UnlinkLink did.
type Unlinked struct {
	TitleID  int64
	Path     string
	Versions []string
	// Trash is where the link was moved aside ("" when it was already gone).
	Trash string
}

// UnlinkLink takes one of our library symlinks out of the library (DELETE moviefile/episodefile does the same for
// the arr clients): through the library's gate the link moves to the trash, what it points at is never touched, and
// the version, or the episodes the link held, are wanted again when no other file remains. Only an active symlink
// of ours: a regular file or hardlink is refused, and so is a link the gate will not let change (library disabled),
// with the gate's reason and nothing changed.
func (s *Service) UnlinkLink(ctx context.Context, linkID int64, reason string) (*Unlinked, error) {
	return s.unlinkLink(ctx, linkID, reason, false)
}

// UnlinkRemoved is UnlinkLink for a removal that deletes the files (the shared removal, internal/removal): a dead
// symlink (its target gone) is moved aside too, and the folders the link leaves empty go, strictly below the library
// root (library.RemoveEmptyDirs). The caller decides which links are the removed copies' own.
func (s *Service) UnlinkRemoved(ctx context.Context, linkID int64, reason string) (*Unlinked, error) {
	return s.unlinkLink(ctx, linkID, reason, true)
}

func (s *Service) unlinkLink(ctx context.Context, linkID int64, reason string, removal bool) (*Unlinked, error) {
	if s.library == nil {
		return nil, refused("the library is not available")
	}
	db := s.st.Reader()
	var kind, state string
	out := &Unlinked{}
	err := db.QueryRowContext(ctx, `SELECT path, kind, state, title_id FROM link WHERE id = ?`, linkID).Scan(&out.Path,
		&kind, &state, &out.TitleID)
	if err != nil {
		return nil, err
	}
	if kind != "symlink" {
		return out, refused("only Theatarr's own symlinks are unlinked; this entry is a %s and stays", kind)
	}
	if state != "active" && (!removal || state != "dead") {
		return out, refused("the link is %s already", state)
	}
	type lv struct {
		id  int64
		key string
	}
	var vs []lv
	rows, err := db.QueryContext(ctx, `SELECT v.id, v.policy_key FROM link_version x JOIN version v ON v.id = x.version_id
		WHERE x.link_id = ? ORDER BY v.policy_key`, linkID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x lv
		if err := rows.Scan(&x.id, &x.key); err != nil {
			_ = rows.Close()
			return nil, err
		}
		vs = append(vs, x)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return out, refused("the link belongs to no copy")
	}
	var eps []int64
	if err := scanIDs(ctx, db, `SELECT episode_id FROM link_episode WHERE link_id = ? ORDER BY episode_id`, linkID,
		&eps); err != nil {
		return nil, err
	}
	// The gate first, for every version, so a refusal changes nothing.
	for _, x := range vs {
		if err := s.library.Gate().CheckVersion(x.key); err != nil {
			return out, refused("%s", err.Error())
		}
		if err := s.library.Gate().CheckPath(x.key, out.Path); err != nil {
			return out, refused("%s", err.Error())
		}
	}
	for _, x := range vs {
		res, err := s.library.Unlink(ctx, x.key, out.Path, linkID, reason)
		if err != nil {
			return out, err
		}
		if res.DryRun {
			return out, refused("dry run: nothing unlinked (%s)", res.Refused)
		}
		if res.TrashPath != "" {
			out.Trash = res.TrashPath
		}
		if err := s.fileGone(ctx, &fileRow{titleID: out.TitleID, versionID: x.id, version: x.key, linkID: linkID,
			f: &vfile{path: out.Path}, episodes: eps}); err != nil {
			return out, err
		}
		out.Versions = append(out.Versions, x.key)
	}
	if removal {
		for _, x := range vs {
			s.library.RemoveEmptyDirs(x.key, out.Path)
		}
	}
	s.log.Info("link unlinked by hand", "link", linkID, "path", out.Path, "versions", strings.Join(out.Versions, ","),
		"trash", out.Trash)
	return out, nil
}

func scanIDs(ctx context.Context, db *sql.DB, q string, arg any, out *[]int64) error {
	rows, err := db.QueryContext(ctx, q, arg)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		*out = append(*out, id)
	}
	return closeRows(rows)
}
