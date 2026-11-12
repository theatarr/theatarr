package demo

import (
	"path"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
)

// Two copies of a series (docs/demo.md "Two copies"). With the demo overlay (examples/demo) the policy has a second
// Live library <k>-theatarr for each arr-fed series version k, as live does: Theatarr's own copy, with the arr's titles
// and monitoring, links to the arr's files (as the one-time import adopts them) and its own downloads for the gaps. Each series then has the arr's copy (its scenario) and Theatarr's (the scenario changed by its plan).

// copyPlan is how Theatarr's copy of a series differs from the arr's. The zero plan mirrors it: every file the arr
// has, adopted.
type copyPlan struct {
	skip        []int // seasons not adopted yet: missing on Theatarr's copy only
	half        bool  // the second half of the last season not adopted yet
	fill        bool  // the episodes the arr lacks are Theatarr's own imports: complete on Theatarr's copy only
	unmonitored []int // seasons Theatarr's copy does not monitor (an edit in Theatarr since the last arr import)
}

func (p copyPlan) unmonitors(season int) bool { return slices.Contains(p.unmonitored, season) }

// copyPlans by series. The downloads in flight for these copies are in downloads (activity.go).
var copyPlans = map[string]copyPlan{
	"The Harrowgate Files":        {skip: []int{3}},           // season 3's pack queued
	"Brindlewood Bakers":          {skip: []int{2}},           // season 2's pack stalled and was blocklisted
	"Tsuratsura Detective Office": {skip: []int{2}},           // nothing searched yet
	"Kiraboshi Mechanics":         {half: true},               // episode 13 downloading
	"Kindling Hall":               {fill: true},               // the arr's missing episodes imported by Theatarr
	"Wickmere":                    {unmonitored: []int{1, 2}}, // only the later seasons wanted in Theatarr's tree
}

// theatarrLatest are the airing series whose newest episode Theatarr imported itself (downloads): their copies do not adopt
// it from the arr.
var theatarrLatest = []string{"Saltmarsh Rangers", "Orbitfall", "Glasshouse Republic", "Kagerou Postal Service"}

// copyEpisodeState is the state of e in Theatarr's copy of t.
func (s *seeder) copyEpisodeState(t *title, e *episode) string {
	// Specials: the arr has no file to adopt.
	if e.season == 0 || !e.aired(s.now) {
		return "wanted"
	}
	base := s.arrEpisodeState(t, e)
	p := copyPlans[t.name]
	switch {
	case p.unmonitors(e.season), slices.Contains(p.skip, e.season):
		return "wanted"
	case p.half && e.season == len(t.seasons) && e.number > t.seasons[e.season-1]/2:
		return "wanted"
	case p.fill && base == "wanted":
		return "cutoff_met"
	}
	return base
}

// copyState is the version state of Theatarr's copy, as the library sums it up after an import: wanted while a
// monitored aired episode is wanted, else filled or cutoff_met by its files. A movie copy mirrors the arr's.
func (s *seeder) copyState(t *title, cp, followed *version) string {
	if t.kind != "series" {
		return followed.state
	}
	var filled, met bool
	for _, e := range t.episodes {
		st := s.copyEpisodeState(t, e)
		switch {
		case st == "wanted" && e.season > 0 && e.aired(s.now) && !copyPlans[t.name].unmonitors(e.season):
			return "wanted"
		case st == "filled":
			filled = true
		case st == "cutoff_met":
			met = true
		}
	}
	switch {
	case filled:
		return "filled"
	case met:
		return "cutoff_met"
	}
	return "wanted"
}

// adopt links the arr's files into Theatarr's copy of t, as the one-time import does: a link per file under the copy's root, to the arr file's mount target, with the arr's file record as its
// metadata. What the copy lacks (its plan) and what Theatarr imported itself (theatarrLatest, filled gaps) is left out.
func (s *seeder) adopt(t *title) {
	cp, followed := t.theatarrCopy()
	if cp == nil || t.kind != "series" {
		return
	}
	at := s.between(days(2.5), days(1.5))
	for _, e := range t.episodes {
		af, ok := followed.files[e.id]
		st := s.copyEpisodeState(t, e)
		if !ok || st == "wanted" || slices.Contains(theatarrLatest, t.name) && e == s.latestAired(t) {
			continue
		}
		rel, f := af.rel, af.f
		score := rel.score
		meta := decide.ExistingFile{Quality: rel.quality, Revision: 1, Score: &score,
			SceneName: strings.TrimSuffix(f.name, ".mkv"), OriginalFilePath: path.Join(rel.name, f.name),
			RelativePath: strings.TrimPrefix(af.path, path.Join(followed.vp.Library, s.folder(t))+"/"),
			ReleaseGroup: rel.group, Languages: rel.langs, Size: f.size, InfoHash: rel.hash,
			ReleaseType: map[bool]string{true: "seasonPack", false: "singleEpisode"}[len(rel.files) > 1]}
		p, target := s.libraryPath(t, cp, rel, f), s.target(rel, f)
		lid := s.exec(`INSERT INTO link (path, target, kind, title_id, release_title, quality, score, file_metadata,
			adopted_from, state, last_checked_at, created_at, updated_at) VALUES (?, ?, 'symlink', ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?)`,
			p, target, t.id, rel.name, rel.quality, rel.score, jsonOf(meta), followed.instance+" "+af.path,
			ms(s.ago(days(0.3))), ms(at), ms(at))
		s.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, ?)`, lid, cp.id)
		s.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, lid, e.id)
		s.exec(`INSERT INTO library_action (at, kind, mode, version_key, link_id, path, target, detail)
			VALUES (?, 'link', 'applied', ?, ?, ?, ?, ?)`, ms(at), cp.key, lid, p, target,
			"adopted from "+followed.instance+" "+af.path)
		if st == "filled" {
			s.exec(`UPDATE episode_version_state SET current_score = ? WHERE version_id = ? AND episode_id = ?`,
				rel.score, cp.id, e.id)
		}
	}
}
