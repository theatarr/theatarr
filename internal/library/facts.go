package library

import (
	"context"
	"path"
	"path/filepath"
	"slices"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
)

// Each file of a release is imported as what it is, not as the release says: the arrs' import
// aggregation (decide.AggregateSeriesFile, AggregateMovieFile) gives every file its own quality, revision, release
// group, release hash, languages and scene name from its name, the release folder, the grabbed release title and its
// probe. That one result names the link, is stored as the link's file inputs (link.quality, file_metadata), and is
// what the upgrade check and the cutoff judge. The release-level provenance stays the download's: indexer flags,
// SeaDex standing, info hash, release type, and the decision's formats and score.

// fileFacts aggregates one file of the download.
func (l *Library) fileFacts(d *importDownload, plan *Plan, f *PlannedFile, episodeTitles []string) (decide.FileFacts, error) {
	folder := filepath.Base(plan.Folder)
	if m := hash6Suffix.FindStringSubmatch(folder); m != nil {
		folder = m[1] // zurg's " {hash6}" is not part of the name the arr would see
	}
	in := decide.ImportFile{Path: path.Join(folder, filepath.ToSlash(f.Rel)), Folder: folder, Client: d.ReleaseTitle,
		Grabbed: d.ReleaseTitle, OtherVideoFiles: nonSamples(plan, d.Version.Title) > 1, EpisodeTitles: episodeTitles}
	if p := f.Probe; p != nil {
		m := &decide.MediaInfo{Title: p.Title}
		if p.Video != nil {
			m.Width, m.Height = p.Video.Width, p.Video.Height
		}
		for _, a := range p.Audio {
			if a.Language != "" {
				m.AudioLanguages = append(m.AudioLanguages, a.Language)
			}
		}
		in.Media = m
	}
	v := &d.Version
	if v.Title.Kind == "movie" {
		facts := decide.AggregateMovieFile(in)
		d.Manual[f.Rel].Overrides.Apply(&facts)
		return facts, nil
	}
	facts, err := decide.AggregateSeriesFile(in)
	d.Manual[f.Rel].Overrides.Apply(&facts)
	return facts, err
}

// nonSamples counts the release's video files that the arrs' DetectSample does not take for samples (the
// title's runtime; an unprobed file counts), as ImportDecisionMaker's nonSampleVideoFileCount.
func nonSamples(plan *Plan, t titleInfo) int {
	expected := t.Runtime
	if t.Kind != "movie" && expected == 0 {
		expected = 45
	}
	n := 0
	for _, f := range plan.Files {
		if f.Probe == nil || f.Probe.RunTimeSeconds >= float64(arrMinimumRuntime(expected)) {
			n++
		}
	}
	return n
}

// withFacts is the download's file inputs (linkedMetadata) for one file: its own aggregated quality, revision,
// group, hash, languages and scene name over the release-level provenance, with its path, target and size.
func withFacts(base decide.ExistingFile, f decide.FileFacts, rel, target string, size int64) decide.ExistingFile {
	x := base
	if f.Quality != "" {
		x.Quality = f.Quality
	}
	x.Revision, x.Real = max(f.Revision, 1), f.Real
	x.IsRepack = f.IsRepack
	x.ReleaseGroup, x.ReleaseHash, x.SceneName = f.ReleaseGroup, f.ReleaseHash, f.SceneName
	if f.Edition != "" {
		x.Edition = f.Edition
	}
	if len(f.Languages) > 0 {
		x.Languages = append([]string(nil), f.Languages...)
	}
	x.Size, x.RelativePath, x.OriginalFilePath = size, rel, target
	return x
}

// manualMetadata scores the client's file choices, rather than the release-level parse.
func (l *Library) manualMetadata(ctx context.Context, d *importDownload, facts decide.FileFacts, rel, target string,
	size int64) (*decide.ExistingFile, error) {
	base, title, err := l.linkedMetadata(ctx, d)
	if err != nil {
		return nil, err
	}
	file := withFacts(*base, facts, rel, target, size)
	if o := d.Manual[rel].Overrides; o != nil {
		if o.IndexerFlags != nil {
			file.IndexerFlags = *o.IndexerFlags
		}
		if l.engine != nil && title != nil {
			err = l.engine.ScoreFile(title, decide.Version{ID: d.Version.VersionID, Key: d.Version.Key, Policy: d.Version.Policy}, &file)
		}
	}
	return &file, err
}

func (l *Library) fileFormats(ctx context.Context, d *importDownload, f *PlannedFile, facts decide.FileFacts) ([]string, error) {
	if d.Manual[f.Rel].Overrides == nil {
		if !d.HasFormats && l.matched != nil && l.engine != nil {
			// No formats saved with the download: name from the formats the link's metadata keeps (the release
			// decided with its size and saved inputs), so a rename right after import proposes nothing.
			base, _, err := l.linkedMetadata(ctx, d)
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(base.Formats))
			for _, cf := range base.Formats {
				names = append(names, cf.Name)
			}
			return l.matched(ctx, d.Version.Key, names), nil
		}
		return l.renameFormats(ctx, d), nil
	}
	file, err := l.manualMetadata(ctx, d, facts, f.Rel, l.mount.ToLink(f.Path), f.Size)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, cf := range file.Formats {
		names = append(names, cf.Name)
	}
	if l.matched != nil {
		return l.matched(ctx, d.Version.Key, names), nil
	}
	if l.engine == nil {
		return nil, nil
	}
	fl := format.Radarr
	if d.Version.Policy.Kind == policy.KindSeries {
		fl = format.Sonarr
	}
	p := l.engine.Profile(d.Version.Policy.Profile, fl)
	var included []string
	for _, cf := range p.Formats {
		if cf.IncludeWhenRenaming && slices.Contains(names, cf.Name) {
			included = append(included, cf.Name)
		}
	}
	return included, nil
}
