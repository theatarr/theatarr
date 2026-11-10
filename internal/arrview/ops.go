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

// Operator actions for the web UI (docs/design/ui.md "Recovery actions"), on the same parts the arr views' grab, queue
// and file endpoints use: the decision engine, the router and the library. They work on one title's versions
// directly, not through a view. internal/web calls them (web.Ops) and records each in the journal.
//
// Nothing here talks to a provider: a grab and a retry go to the router, which writes only for an active version
// while router.enabled is true and checks the blocklist on every candidate; an unlink moves only one of our
// own symlinks aside through the library's gate. Debrid torrents are never deleted (they are shared by every
// library), and no regular file is ever touched.

// Refused is an operator action that was not done, with the reason in words. Web answers it 409.
type Refused struct{ Reason string }

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
// applies to any route: the gate (shadow and router.enabled), the blocklist, pacing and budgets.
func (s *Service) GrabRelease(ctx context.Context, titleID int64, version string, rel indexer.Release, override bool,
	source string) (*Grabbed, error) {
	if s.pol.Indexers[rel.Indexer] == nil {
		return nil, refused("release indexer is no longer enabled: %s", rel.Indexer)
	}
	return s.grab(ctx, titleID, version, rel, override, source)
}

// AddRelease routes a release a person names by its hash for a version of a title (the release panel's "Add by
// magnet or hash"): a release no search found, so it has no indexer and carries policy.ManualIndexer in its place
// (the adders refuse a magnet that names no source). It is GrabRelease without the indexer check and nothing else:
// decided now for the version, refused when rejected unless override, then the router's own rules (gate, blocklist,
// provider order, pacing, cached-only, the record of our own adds).
func (s *Service) AddRelease(ctx context.Context, titleID int64, version string, rel indexer.Release, override bool,
	source string) (*Grabbed, error) {
	rel.Indexer, rel.Kind, rel.Privacy = policy.ManualIndexer, "", ""
	return s.grab(ctx, titleID, version, rel, override, source)
}

func (s *Service) grab(ctx context.Context, titleID int64, version string, rel indexer.Release, override bool,
	source string) (*Grabbed, error) {
	if s.decider == nil {
		return nil, refused("the decision engine is not running")
	}
	if s.router == nil {
		return nil, refused("the router is not running")
	}
	if indexer.NormalizeHash(rel.InfoHash) == "" {
		return nil, refused("the release has no info hash: only magnets by hash are routed")
	}
	d, err := s.decider.Decide(ctx, decide.Release{Release: rel}, titleID, version)
	if err != nil {
		return nil, err
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
	c := router.Candidate{Release: rel, Quality: d.Quality, Score: d.Score, Episodes: d.EpisodeIDs,
		Formats: append([]decide.FormatScore{}, d.Formats...), EpisodeMap: d.EpisodeMapping()}
	if vp := s.pol.Versions[version]; len(c.Episodes) == 0 && vp != nil && vp.Kind == policy.KindSeries {
		return out, refused("the release matches no episode of the title")
	}
	req := router.Request{TitleID: titleID, VersionID: d.VersionID, Version: version,
		Candidates: []router.Candidate{c}, Source: source}
	out.Job, err = s.router.Enqueue(ctx, req, time.Time{})
	if err != nil {
		return out, fmt.Errorf("arrview: routing %s for %s: %w", rel.Title, version, err)
	}
	s.log.Info("release grabbed by hand", "title", titleID, "version", version, "release", rel.Title,
		"accepted", d.Accepted, "job", out.Job, "source", source)
	return out, nil
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
// download row; refused while the router is disabled, the version is in shadow, or the hash is blocklisted), using
// the stored hash, never a link. A download that finished but is not linked yet
// (ready, importing) is imported now, without waiting for the import's back-off. Anything still on its way is
// refused: there is nothing to retry.
func (s *Service) RetryDownload(ctx context.Context, id int64, source string) (*Retried, error) {
	var state, version, hash, rel string
	var titleID int64
	err := s.st.Reader().QueryRowContext(ctx, `SELECT d.state, d.title_id, d.version_key, d.info_hash, d.release_title
		FROM download d WHERE d.id = ?`, id).Scan(&state, &titleID, &version, &hash, &rel)
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
	default:
		return out, refused("nothing to retry: the download is %s and still on its way", state)
	}
	if s.router == nil {
		return out, refused("the router is not running")
	}
	if err := s.refuseDryRunRoute(out.Version); err != nil {
		return out, err
	}
	var why string
	err = s.st.Reader().QueryRowContext(ctx, `SELECT reason FROM router_blocklist WHERE version_key = ? AND info_hash = ?`,
		out.Version, hash).Scan(&why)
	if err == nil {
		return out, refused("the release is blocklisted for %s (%s): unblock it first", out.Version, why)
	}
	if !errors.Is(err, sql.ErrNoRows) {
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
		req.Candidates[0].Release.Indexer != policy.ManualIndexer && !slices.Contains(vp.Indexers, req.Candidates[0].Release.Indexer) {
		return out, refused("indexer %s is not one of version %s's indexers", req.Candidates[0].Release.Indexer, out.Version)
	}
	req.DownloadID, req.DecisionRev, req.Source = 0, "", source
	out.Job, err = s.router.Enqueue(ctx, *req, time.Time{})
	if err != nil {
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
		return refused("router.enabled is false; no download will start")
	}
	vp := s.pol.Versions[version]
	if vp == nil {
		return refused("there is no active policy for %s; no download will start", version)
	}
	if vp.Mode != policy.ModeActive {
		return refused("the %s copy is in shadow mode; no download will start", version)
	}
	if !vp.Grab {
		return refused("the %s copy has grab off; no download will start", version)
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
// of ours: a regular file or hardlink is refused, and so is a link the gate will not let change (library disabled,
// version in shadow), with the gate's reason and nothing changed.
func (s *Service) UnlinkLink(ctx context.Context, linkID int64, reason string) (*Unlinked, error) {
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
	if state != "active" {
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
