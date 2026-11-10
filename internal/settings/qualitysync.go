package settings

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"go.yaml.in/yaml/v3"
)

// QualitySyncConfig describes a reviewed upstream import. Profiles maps local profile keys to TRaSH score sets.
// A Recyclarr config supplies its own scores and profiles. Retired formats are always retained.
//
// Origin says where the TRaSH tree comes from: empty is the local THEATARR_TRASH directory (when unset, the snapshot
// bundled with the binary), or the archive pinned by
// ArchiveURL/Commit; "github" tracks Repo's Branch (default TRaSH-Guides/Guides master): each run asks GitHub for the
// branch's newest commit and fetches that commit's archive only when it differs from the last successful run's.
type QualitySyncConfig struct {
	Kind       string            `json:"kind" yaml:"kind"`
	Flavour    string            `json:"flavour" yaml:"flavour"`
	Origin     string            `json:"origin,omitempty" yaml:"origin,omitempty"`
	Repo       string            `json:"repo,omitempty" yaml:"repo,omitempty"`
	Branch     string            `json:"branch,omitempty" yaml:"branch,omitempty"`
	ArchiveURL string            `json:"archive_url,omitempty" yaml:"archive_url,omitempty"`
	Commit     string            `json:"commit,omitempty" yaml:"commit,omitempty"`
	TrashIDs   []string          `json:"trash_ids,omitempty" yaml:"trash_ids,omitempty"`
	Profiles   map[string]string `json:"profiles,omitempty" yaml:"profiles,omitempty"`
	Recyclarr  string            `json:"recyclarr,omitempty" yaml:"recyclarr,omitempty"`
	Schedule   string            `json:"schedule" yaml:"schedule"`
	Mode       string            `json:"mode" yaml:"mode"`
	Enabled    bool              `json:"enabled" yaml:"enabled"`
	// ScoreState is Theatarr's memory of the bound profiles' scores (runs and score decisions write it; the editor
	// never does): local profile key -> its score set and, per score key, TRaSH's last score and our choice.
	ScoreState map[string]ProfileScoreState `json:"score_state,omitempty" yaml:"-"`
	// QualitySizes applies a Recyclarr config's quality_definition (TRaSH's quality sizes) to its profiles; unset is
	// on. Off, the preview notes the definition as skipped. Recyclarr sources only.
	QualitySizes *bool `json:"quality_sizes,omitempty" yaml:"quality_sizes,omitempty"`
	// FieldState is Theatarr's memory of a Recyclarr source's managed profile fields (qualitysync_fields.go): local
	// profile key -> field -> the source's last value and our choice. Runs and decisions write it; the editor never does.
	FieldState map[string]map[string]FieldMemory `json:"field_state,omitempty" yaml:"-"`
}

// sizesOn reports whether a Recyclarr source applies quality_definition.
func (c QualitySyncConfig) sizesOn() bool { return c.QualitySizes == nil || *c.QualitySizes }

// ProfileScoreState is what a source remembers of a profile bound to the TRaSH score set Set. A rebinding to another
// set starts over, as a first run does.
type ProfileScoreState struct {
	Set    string                     `json:"set"`
	Scores map[string]TrashScoreState `json:"scores"`
}

// TrashScoreState is one score of a bound profile: TRaSH's score when it was last taken into account, and whether an
// administrator kept ours instead (asked again only when TRaSH changes that score again).
type TrashScoreState struct {
	Trash  int  `json:"trash"`
	Pinned bool `json:"pinned,omitempty"`
}

type QualitySyncSource struct {
	Record
	Config QualitySyncConfig `json:"config"`
}

var commitRE = regexp.MustCompile(`^[a-f0-9]{40}$`)

// Tracked sources and the apply modes.
const (
	OriginGitHub = "github"
	// ModeAutoDefinitions applies a run on its own only when every change is a specification change of a format the
	// settings already hold (what the household's weekly Recyclarr did) or a bound score that follows TRaSH; added or
	// retired formats and any other score or profile change wait as a pending review, our own scores as held scores.
	ModeAutoDefinitions = "auto_definitions"
	DefaultTrackedRepo  = "TRaSH-Guides/Guides"
	DefaultTrackedRef   = "master"
	trackedSchedule     = "168h"
	// trackedRetry is how soon a tracked source tries again after a failed run, when its schedule is longer.
	trackedRetry = 6 * time.Hour
)

var (
	repoRE   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	branchRE = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_./-]*$`)
	// GitHub's hosts; tests point them at a fake.
	githubAPI      = "https://api.github.com"
	githubCodeload = "https://codeload.github.com"
)

// TrackedCommitURL is the commits API request for a tracked source's newest commit id.
func TrackedCommitURL(c QualitySyncConfig) string {
	return githubAPI + "/repos/" + c.Repo + "/commits/" + escapeRef(c.Branch)
}

// TrackedArchiveURL is the archive pinned to one commit of a tracked source (codeload: the gateway does not follow
// github.com's redirect to it).
func TrackedArchiveURL(c QualitySyncConfig, commit string) string {
	return githubCodeload + "/" + c.Repo + "/tar.gz/" + commit
}

func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func (c *QualitySyncConfig) validate() error {
	bad := func(path, msg string) error {
		return &policy.Errors{List: []policy.FieldError{{Path: "sync_sources." + path, Msg: msg}}}
	}
	if c.Kind != "trash" && c.Kind != "recyclarr" {
		return bad("kind", "must be trash or recyclarr")
	}
	if c.Flavour != "radarr" && c.Flavour != "sonarr" {
		return bad("flavour", "must be radarr or sonarr")
	}
	mode, schedule := "review", "24h"
	switch c.Origin {
	case "":
		if c.Repo != "" || c.Branch != "" {
			return bad("repo", "only a github origin tracks a repository branch")
		}
	case OriginGitHub:
		if c.ArchiveURL != "" || c.Commit != "" {
			return bad("archive_url", "a github origin tracks its branch; leave the pinned archive and commit empty")
		}
		if c.Repo == "" {
			c.Repo = DefaultTrackedRepo
		}
		if c.Branch == "" {
			c.Branch = DefaultTrackedRef
		}
		if !repoRE.MatchString(c.Repo) || strings.Contains(c.Repo, "..") {
			return bad("repo", "must be owner/repository")
		}
		if !branchRE.MatchString(c.Branch) || strings.Contains(c.Branch, "..") || strings.HasSuffix(c.Branch, "/") || len(c.Branch) > 200 {
			return bad("branch", "must be a branch name")
		}
		mode, schedule = ModeAutoDefinitions, trackedSchedule
	default:
		return bad("origin", "must be empty (local or pinned archive) or github")
	}
	if c.Mode == "" {
		c.Mode = mode
	}
	if c.Mode != "review" && c.Mode != "auto" && c.Mode != ModeAutoDefinitions {
		return bad("mode", "must be review, auto or auto_definitions")
	}
	if c.Schedule == "" {
		c.Schedule = schedule
	}
	d, err := time.ParseDuration(c.Schedule)
	if err != nil || d < time.Minute {
		return bad("schedule", "must be a duration of at least 1m (default 24h; 168h for a github origin)")
	}
	if c.ArchiveURL != "" {
		u, err := url.Parse(c.ArchiveURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !commitRE.MatchString(c.Commit) || !strings.Contains(u.Path, c.Commit) {
			return bad("archive_url", "must be a credential-free git archive URL pinned to the full commit")
		}
	}
	if c.Kind != "recyclarr" {
		c.QualitySizes = nil // a Recyclarr switch; a TRaSH source has no quality_definition
	}
	if c.Kind == "recyclarr" {
		// Recyclarr ignores service credentials; do not retain them in settings, previews or audit.
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(c.Recyclarr), &node); err != nil {
			return bad("recyclarr", "invalid YAML")
		}
		stripRecyclarrCredentials(&node)
		clean, err := yaml.Marshal(&node)
		if err != nil {
			return bad("recyclarr", "invalid YAML")
		}
		c.Recyclarr = string(clean)
		if len(node.Content) == 0 {
			return bad("recyclarr", "configuration is required")
		}
	}
	return nil
}
func stripRecyclarrCredentials(n *yaml.Node) {
	if n.Kind == yaml.MappingNode {
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if key == "api_key" || key == "base_url" {
				n.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: ""}
			}
		}
	}
	for _, c := range n.Content {
		stripRecyclarrCredentials(c)
	}
}

type FormatChange struct {
	Key     string `json:"key"`
	TrashID string `json:"trash_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}
type ScoreChange struct {
	Profile string `json:"profile"`
	Format  string `json:"format"`
	Before  int    `json:"before"`
	After   int    `json:"after"`
}

// HeldScore is a score of a bound profile that waits for an administrator: ours (it differs from TRaSH's last score,
// or was kept) while TRaSH changed it, or, on the first run after binding (Last nil), any score that differs from
// TRaSH's. Ours is nil when the profile does not score the format yet (a format added by the source's TRaSH IDs).
type HeldScore struct {
	Profile string `json:"profile"`
	Format  string `json:"format"`
	Ours    *int   `json:"ours"`
	Last    *int   `json:"last"`
	Trash   int    `json:"trash"`
}

// ReviewScores is the kind of a review that holds only held scores and held fields, resolved one by one
// (ResolveScores), never applied as a candidate.
const ReviewScores = "scores"

type QualityPreview struct {
	ID         int64                `json:"id"`
	Generation int64                `json:"generation"`
	Source     string               `json:"source"`
	Commit     string               `json:"commit"`
	Kind       string               `json:"kind,omitempty"`
	Added      []FormatChange       `json:"added"`
	Changed    []FormatChange       `json:"spec_changed"`
	Retired    []FormatChange       `json:"retired"`
	Scores     []ScoreChange        `json:"scores"`
	Held       []HeldScore          `json:"held"`
	HeldFields []HeldField          `json:"held_fields"`
	Fields     []ProfileFieldChange `json:"fields"`
	Profiles   []string             `json:"profiles_changed"`
	Notes      []string             `json:"notes"`
	Impact     DecisionImpact       `json:"impact"`
	State      string               `json:"state"`
	candidate  Records
	// draft marks a preview of a config that is not the saved source's (the editor's "Preview draft import"): it
	// applies TRaSH's scores to bound profiles as a whole review, holds nothing and remembers nothing.
	draft bool
	// scoreState marks a candidate that changes the source's ScoreState or FieldState.
	scoreState bool
}

func (p *QualityPreview) HasChanges() bool {
	return len(p.Added)+len(p.Changed)+len(p.Scores)+len(p.Profiles) > 0
}

// FollowsOnly reports a preview whose score changes all belong to profiles bound to a TRaSH score set (scores that
// follow TRaSH; ours are held apart), with no format added or retired and no other profile change.
func (p *QualityPreview) FollowsOnly(c QualitySyncConfig) bool {
	if len(p.Added)+len(p.Retired)+len(p.Profiles) > 0 {
		return false
	}
	for _, s := range p.Scores {
		if _, bound := c.Profiles[s.Profile]; !bound {
			return false
		}
	}
	return len(p.Changed)+len(p.Scores) > 0 || p.scoreState
}

// ArchiveFetcher must fetch through the gateway. Tests inject an httptest client; settings never dials HTTP.
type ArchiveFetcher func(context.Context, QualitySyncConfig) ([]byte, error)

// CommitFetcher returns a tracked source's newest commit id (TrackedCommitURL), through the gateway like ArchiveFetcher.
type CommitFetcher func(context.Context, QualitySyncConfig) (string, error)

type QualitySync struct {
	Store       *Store
	Coordinator *RuntimeCoordinator
	TrashRoot   string
	// ConvertRecyclarr converts a Recyclarr config (raw, TRaSH root, flavour, whether quality_definition applies).
	ConvertRecyclarr func(raw []byte, root, flavour string, sizes bool) (RecyclarrImport, error)
	Impact           func(context.Context, *policy.Policy, *policy.Policy) (DecisionImpact, error)
	FetchArchive     ArchiveFetcher
	FetchCommit      CommitFetcher
	mu               sync.Mutex
	// linkTree is the TRaSH tree of the last link preview, for its apply.
	linkMu   sync.Mutex
	linkTree *linkTree
}

// resolve turns a tracked source into the archive pinned to its branch's newest commit. Other sources pass through.
func (s *QualitySync) resolve(ctx context.Context, c QualitySyncConfig) (QualitySyncConfig, error) {
	if c.Origin != OriginGitHub {
		return c, nil
	}
	if s.FetchCommit == nil {
		return c, errors.New("settings: GitHub gateway unavailable")
	}
	sha, err := s.FetchCommit(ctx, c)
	if err != nil {
		return c, err
	}
	sha = strings.ToLower(strings.TrimSpace(sha))
	if !commitRE.MatchString(sha) {
		return c, errors.New("settings: GitHub returned no commit id")
	}
	c.ArchiveURL, c.Commit = TrackedArchiveURL(c, sha), sha
	return c, nil
}
