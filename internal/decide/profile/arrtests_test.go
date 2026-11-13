package profile

// Runs the arr DecisionEngineTests and Qualities fixtures extracted to testdata/arr_*.json.
//
// Case schema (all fields optional unless noted):
//
//	fixture, test, args   the arr fixture class, test method and [TestCase] arguments as written (identification)
//	flavour               "radarr" | "sonarr" (required)
//	spec                  what to run (required):
//	                        a specification by short name: AcceptableSize, CustomFormatAllowedByProfile, Language,
//	                          MaximumSize, NotSample, QualityAllowedByProfile, RawDisk, Repack, AnimeVersionUpgrade,
//	                          UpgradeAllowed, UpgradeDisk  -> expect.accepted (+ reason, message when given)
//	                        IsUpgradable                    -> expect.upgradeable (reason name) or expect.result
//	                                                           (true when the reason is None)
//	                        CutoffNotMet, QualityCutoffNotMet, IsUpgradeAllowed, IsRevisionUpgrade -> expect.result
//	                        QualityModelCompare             -> expect.compare (sign of Compare(left, right));
//	                                                           left/right are qualities, groupOrder sets
//	                                                           respectGroupOrder
//	                        RevisionCompare                 -> expect.compare, left/right revisions in left/right
//	                        QualityIndexCompare             -> expect.compare, left/right {index, groupIndex},
//	                                                           groupOrder sets respectGroupOrder
//	                        Prioritize                      -> releases, expect.first (index of the best) and/or
//	                                                           expect.order (indices best first)
//	profile               items, cutoff, upgradeAllowed, minFormatScore, cutoffFormatScore, minUpgradeFormatScore,
//	                      formatItems [{name, score}], language (name), name
//	                        items: a list of {quality, allowed} and {id, name, allowed, items: [quality names]},
//	                        lowest first; or {"getDefaultQualities": [allowed names]} for the arr test helper
//	                        QualityFixture.GetDefaultQualities(allowed...) (empty list: all allowed);
//	                        or {"defaultProfile": {"cutoff": name, "allowed": [names]}} for
//	                        QualityProfileService.GetDefaultProfile
//	                        cutoff: a quality name or a numeric id (quality or group)
//	definitions           [{quality, min, max, preferred}] replacing the default definition of that quality; a
//	                      missing or null size is null (unlimited)
//	config                {propers: preferAndUpgrade|doNotUpgrade|doNotPrefer, maximumSize (MB),
//	                      preferIndexerFlags, preferredProtocol: usenet|torrent, acceptReleaseAnyUpgradable (the
//	                      actuallyevan fork's switch)}; Moq defaults when absent (preferAndUpgrade, 0, false, unknown)
//	media                 {title, runtime, originalLanguage, anime, file, pilot}
//	release               {title, size (bytes), container, quality, releaseGroup, languages [names], formats [names],
//	                      formatScore (else computed from the profile), protocol, indexerPriority, indexerFlags,
//	                      seeders, peers, ageHours, fullSeason, special}
//	episodes              [{season, episode, runtime, airDateHours (relative to now; negative is past), file}]
//	releases              Prioritize: [{release, episodes, media}] (media overrides the case media)
//	current, new          IsUpgradable & co: qualities; currentFormats, newFormats: format names
//	left, right           compare cases
//	quality               {quality: name, version (default 1), real, isRepack}
//	file                  {quality, releaseGroup, formats [names]}
//	expect                {accepted, reason, message, result, upgradeable, compare, first, order}
//	knownFailure          why the case is expected to fail; it is then run and must fail
//
// Formats are identified by name; each distinct name gets an id in order of first appearance.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
)

type caseFile struct {
	Source string  `json:"source"`
	Cases  []tcase `json:"cases"`
}

type tcase struct {
	Fixture string `json:"fixture"`
	Test    string `json:"test"`
	Args    string `json:"args,omitempty"`
	Flavour string `json:"flavour"`
	Spec    string `json:"spec"`

	Profile     *tprofile    `json:"profile,omitempty"`
	Definitions []tdef       `json:"definitions,omitempty"`
	Config      tconfig      `json:"config"`
	Media       *tmedia      `json:"media,omitempty"`
	Release     *trelease    `json:"release,omitempty"`
	Episodes    []tepisode   `json:"episodes,omitempty"`
	Releases    []tcandidate `json:"releases,omitempty"`

	Current        *tquality       `json:"current,omitempty"`
	CurrentFormats []string        `json:"currentFormats,omitempty"`
	New            *tquality       `json:"new,omitempty"`
	NewFormats     []string        `json:"newFormats,omitempty"`
	Left           json.RawMessage `json:"left,omitempty"`
	Right          json.RawMessage `json:"right,omitempty"`
	GroupOrder     bool            `json:"groupOrder,omitempty"`

	Expect       texpect `json:"expect"`
	KnownFailure string  `json:"knownFailure,omitempty"`
	Note         string  `json:"note,omitempty"`
}

type tprofile struct {
	Name                  string          `json:"name"`
	Items                 json.RawMessage `json:"items"`
	Cutoff                json.RawMessage `json:"cutoff"`
	UpgradeAllowed        bool            `json:"upgradeAllowed"`
	MinFormatScore        int             `json:"minFormatScore"`
	CutoffFormatScore     int             `json:"cutoffFormatScore"`
	MinUpgradeFormatScore int             `json:"minUpgradeFormatScore"`
	FormatItems           []struct {
		Name  string `json:"name"`
		Score int    `json:"score"`
	} `json:"formatItems"`
	Language string `json:"language"`
}

type titem struct {
	Quality string          `json:"quality"`
	ID      int             `json:"id"`
	Name    string          `json:"name"`
	Allowed bool            `json:"allowed"`
	Items   json.RawMessage `json:"items"`
}

type tdef struct {
	Quality   string   `json:"quality"`
	Min       *float64 `json:"min"`
	Max       *float64 `json:"max"`
	Preferred *float64 `json:"preferred"`
}

type tconfig struct {
	Propers            string `json:"propers"`
	MaximumSize        int    `json:"maximumSize"`
	PreferIndexerFlags bool   `json:"preferIndexerFlags"`
	PreferredProtocol  string `json:"preferredProtocol"`
	// AcceptReleaseAnyUpgradable is the fork's ACCEPT_RELEASE_ANY_UPGRADABLE.
	AcceptReleaseAnyUpgradable bool `json:"acceptReleaseAnyUpgradable"`
}

type tquality struct {
	Quality  string `json:"quality"`
	Version  *int   `json:"version"`
	Real     int    `json:"real"`
	IsRepack bool   `json:"isRepack"`
}

type tfile struct {
	Quality      tquality `json:"quality"`
	ReleaseGroup string   `json:"releaseGroup"`
	Formats      []string `json:"formats"`
}

type tmedia struct {
	Title            string    `json:"title"`
	Runtime          int       `json:"runtime"`
	OriginalLanguage string    `json:"originalLanguage"`
	Anime            bool      `json:"anime"`
	File             *tfile    `json:"file"`
	Pilot            *tepisode `json:"pilot"`
}

type tepisode struct {
	Season       int      `json:"season"`
	Episode      int      `json:"episode"`
	Runtime      int      `json:"runtime"`
	AirDateHours *float64 `json:"airDateHours"`
	File         *tfile   `json:"file"`
}

type trelease struct {
	Title           string    `json:"title"`
	Size            int64     `json:"size"`
	Container       string    `json:"container"`
	Quality         *tquality `json:"quality"`
	ReleaseGroup    string    `json:"releaseGroup"`
	Languages       []string  `json:"languages"`
	Formats         []string  `json:"formats"`
	FormatScore     *int      `json:"formatScore"`
	Protocol        string    `json:"protocol"`
	IndexerPriority int       `json:"indexerPriority"`
	IndexerFlags    int       `json:"indexerFlags"`
	Seeders         *int      `json:"seeders"`
	Peers           *int      `json:"peers"`
	AgeHours        float64   `json:"ageHours"`
	FullSeason      bool      `json:"fullSeason"`
	Special         bool      `json:"special"`
}

type tcandidate struct {
	Release  trelease   `json:"release"`
	Episodes []tepisode `json:"episodes"`
	Media    *tmedia    `json:"media"`
}

type texpect struct {
	Accepted    *bool  `json:"accepted"`
	Reason      string `json:"reason"`
	Message     string `json:"message"`
	Result      *bool  `json:"result"`
	Upgradeable string `json:"upgradeable"`
	Compare     *int   `json:"compare"`
	First       *int   `json:"first"`
	Order       []int  `json:"order"`
}

// builder turns a case into our types.
type builder struct {
	fl      format.Flavour
	now     time.Time
	formats map[string]*format.Format
	nextID  int
}

func (b *builder) format(name string) *format.Format {
	if f, ok := b.formats[name]; ok {
		return f
	}
	b.nextID++
	f := &format.Format{ID: b.nextID, Name: name, Flavour: b.fl}
	b.formats[name] = f
	return f
}

func (b *builder) formatList(names []string) []*format.Format {
	out := []*format.Format{}
	for _, n := range names {
		out = append(out, b.format(n))
	}
	return out
}

func (b *builder) quality(name string) Quality {
	q, ok := QualityByName(b.fl, name)
	if !ok {
		panic(fmt.Sprintf("no %s quality %q", b.fl, name))
	}
	return q
}

func (b *builder) model(t *tquality) QualityModel {
	if t == nil {
		return NewQualityModel(b.quality("Unknown"))
	}
	m := QualityModel{Quality: b.quality(t.Quality), Revision: Revision{Version: 1, Real: t.Real, IsRepack: t.IsRepack}}
	if t.Version != nil {
		m.Revision.Version = *t.Version
	}
	return m
}

func (b *builder) language(name string) int {
	id, ok := b.fl.LanguageID(name)
	if !ok {
		panic(fmt.Sprintf("no %s language %q", b.fl, name))
	}
	return id
}

// testDefaultQualities is the arr test helper QualityFixture.GetDefaultQualities's list.
var testDefaultQualities = map[format.Flavour][]string{
	format.Radarr: {"Unknown", "CAM", "TELECINE", "DVDSCR", "REGIONAL", "SDTV", "DVD", "DVD-R", "HDTV-720p", "HDTV-1080p",
		"HDTV-2160p", "WEBDL-480p", "WEBDL-720p", "WEBDL-1080p", "WEBDL-2160p", "Bluray-480p", "Bluray-576p",
		"Bluray-720p", "Bluray-1080p", "Bluray-2160p", "BR-DISK", "Raw-HD"},
	format.Sonarr: {"Unknown", "SDTV", "WEBDL-480p", "DVD", "HDTV-720p", "HDTV-1080p", "HDTV-2160p", "Raw-HD",
		"WEBDL-720p", "WEBDL-1080p", "WEBDL-2160p", "Bluray-720p", "Bluray-1080p", "Bluray-2160p"},
}

// getDefaultQualities ports QualityFixture.GetDefaultQualities: the disallowed qualities first (list order), then
// the allowed ones in the order given.
func (b *builder) getDefaultQualities(allowed []string) []Item {
	all := testDefaultQualities[b.fl]
	if len(allowed) == 0 {
		allowed = all
	}
	isAllowed := map[string]bool{}
	for _, a := range allowed {
		isAllowed[b.quality(a).Name] = true
	}
	var items []Item
	seen := map[string]bool{}
	for _, n := range all { // qualities.Except(allowed): distinct, in order
		if !isAllowed[n] && !seen[n] {
			seen[n] = true
			q := b.quality(n)
			items = append(items, Item{Quality: &q})
		}
	}
	for _, n := range allowed { // Concat(allowed)
		q := b.quality(n)
		items = append(items, Item{Quality: &q, Allowed: true})
	}
	return items
}

func (b *builder) profile(t *tprofile) *Profile {
	if t == nil {
		t = &tprofile{}
	}
	p := &Profile{}
	raw := strings.TrimSpace(string(t.Items))
	switch {
	case raw == "" || raw == "null":
		p.Items = b.getDefaultQualities(nil)
	case strings.HasPrefix(raw, "["):
		var items []titem
		must(json.Unmarshal(t.Items, &items))
		for _, it := range items {
			if it.Quality != "" {
				q := b.quality(it.Quality)
				p.Items = append(p.Items, Item{Quality: &q, Allowed: it.Allowed})
				continue
			}
			var names []string
			must(json.Unmarshal(it.Items, &names))
			g := Item{ID: it.ID, Name: it.Name, Allowed: it.Allowed}
			for _, n := range names {
				q := b.quality(n)
				g.Items = append(g.Items, Item{Quality: &q, Allowed: it.Allowed})
			}
			p.Items = append(p.Items, g)
		}
	default:
		var o struct {
			GetDefaultQualities *[]string `json:"getDefaultQualities"`
			DefaultProfile      *struct {
				Cutoff  string   `json:"cutoff"`
				Allowed []string `json:"allowed"`
			} `json:"defaultProfile"`
		}
		must(json.Unmarshal(t.Items, &o))
		switch {
		case o.GetDefaultQualities != nil:
			p.Items = b.getDefaultQualities(*o.GetDefaultQualities)
		case o.DefaultProfile != nil:
			var cut *Quality
			if o.DefaultProfile.Cutoff != "" {
				q := b.quality(o.DefaultProfile.Cutoff)
				cut = &q
			}
			var allowed []Quality
			for _, n := range o.DefaultProfile.Allowed {
				allowed = append(allowed, b.quality(n))
			}
			d := DefaultProfile(b.fl, t.Name, cut, allowed, nil)
			p.Items, p.Cutoff = d.Items, d.Cutoff
		default:
			panic("unknown items form " + raw)
		}
	}
	if c := strings.TrimSpace(string(t.Cutoff)); c != "" && c != "null" {
		var id int
		if json.Unmarshal(t.Cutoff, &id) == nil {
			p.Cutoff = id
		} else {
			var name string
			must(json.Unmarshal(t.Cutoff, &name))
			p.Cutoff = b.quality(name).ID
		}
	}
	p.Name = t.Name
	p.Flavour = b.fl
	p.UpgradeAllowed = t.UpgradeAllowed
	p.MinFormatScore = t.MinFormatScore
	p.CutoffFormatScore = t.CutoffFormatScore
	p.MinUpgradeFormatScore = t.MinUpgradeFormatScore
	for _, fi := range t.FormatItems {
		f := b.format(fi.Name)
		p.FormatItems = append(p.FormatItems, format.FormatItem{Format: f.ID, Name: f.Name, Score: fi.Score})
	}
	if t.Language != "" {
		p.Language = b.language(t.Language)
	}
	return p
}

func (b *builder) file(t *tfile) *File {
	if t == nil {
		return nil
	}
	return &File{Quality: b.model(&t.Quality), ReleaseGroup: t.ReleaseGroup, Formats: b.formatList(t.Formats)}
}

func (b *builder) episode(t tepisode) *Episode {
	ep := &Episode{SeasonNumber: t.Season, EpisodeNumber: t.Episode, Runtime: t.Runtime, File: b.file(t.File)}
	if t.AirDateHours != nil {
		at := b.now.Add(time.Duration(*t.AirDateHours * float64(time.Hour)))
		ep.AirDateUtc = &at
	}
	return ep
}

func (b *builder) media(t *tmedia) *Media {
	if t == nil {
		return &Media{}
	}
	m := &Media{Title: t.Title, Runtime: t.Runtime, Anime: t.Anime, MovieFile: b.file(t.File)}
	if t.OriginalLanguage != "" {
		m.OriginalLanguage = b.language(t.OriginalLanguage)
	}
	if t.Pilot != nil {
		m.Pilot = b.episode(*t.Pilot)
	}
	return m
}

func protocol(s string) Protocol {
	switch strings.ToLower(s) {
	case "usenet":
		return ProtocolUsenet
	case "torrent":
		return ProtocolTorrent
	}
	return ProtocolUnknown
}

func (b *builder) release(t *trelease) *Release {
	if t == nil {
		t = &trelease{}
	}
	r := &Release{Title: t.Title, Size: t.Size, Container: t.Container, Quality: b.model(t.Quality),
		ReleaseGroup: t.ReleaseGroup, Formats: b.formatList(t.Formats), Protocol: protocol(t.Protocol),
		IndexerPriority: t.IndexerPriority, IndexerFlags: t.IndexerFlags, Seeders: t.Seeders, Peers: t.Peers,
		PublishDate: b.now.Add(-time.Duration(t.AgeHours * float64(time.Hour))), FullSeason: t.FullSeason,
		Special: t.Special}
	for _, l := range t.Languages {
		r.Languages = append(r.Languages, b.language(l))
	}
	return r
}

func (b *builder) subject(p *Profile, m *Media, r *trelease, eps []tepisode) *Subject {
	s := &Subject{Profile: p, Media: m, Release: b.release(r)}
	for _, e := range eps {
		s.Episodes = append(s.Episodes, b.episode(e))
	}
	if r != nil && r.FormatScore != nil {
		s.Release.FormatScore = *r.FormatScore
	} else {
		s.Score()
	}
	return s
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// runCase returns "" when the case passes, else what differed.
func runCase(c tcase) (fail string) {
	defer func() {
		if r := recover(); r != nil {
			fail = fmt.Sprintf("panic: %v", r)
		}
	}()
	fl := format.Flavour(c.Flavour)
	if !fl.Valid() {
		return "bad flavour " + c.Flavour
	}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	b := &builder{fl: fl, now: now, formats: map[string]*format.Format{}}
	p := b.profile(c.Profile)
	defs := DefaultDefinitions(fl)
	for _, d := range c.Definitions {
		defs.Set(Definition{Quality: b.quality(d.Quality), Title: d.Quality, MinSize: d.Min, MaxSize: d.Max, PreferredSize: d.Preferred})
	}
	e := &Engine{Flavour: fl, Definitions: defs, Now: func() time.Time { return now }}
	if c.Config.Propers != "" {
		v, err := ParseProperDownloadTypes(c.Config.Propers)
		if err != nil {
			return err.Error()
		}
		e.Config.DownloadPropersAndRepacks = v
	}
	e.Config.MaximumSize = c.Config.MaximumSize
	e.Config.PreferIndexerFlags = c.Config.PreferIndexerFlags
	e.Config.PreferredProtocol = protocol(c.Config.PreferredProtocol)
	e.Config.AcceptReleaseAnyUpgradable = c.Config.AcceptReleaseAnyUpgradable

	boolResult := func(got bool) string {
		if c.Expect.Result == nil {
			return "expect.result missing"
		}
		if got != *c.Expect.Result {
			return fmt.Sprintf("result %v, want %v", got, *c.Expect.Result)
		}
		return ""
	}
	cur := func() QualityModel { return b.model(c.Current) }
	newQ := func() QualityModel { return b.model(c.New) }

	switch c.Spec {
	case "IsUpgradable":
		got := e.IsUpgradable(p, cur(), b.formatList(c.CurrentFormats), newQ(), b.formatList(c.NewFormats))
		if c.Expect.Upgradeable != "" && got.String() != c.Expect.Upgradeable {
			return fmt.Sprintf("upgradeable %s, want %s", got, c.Expect.Upgradeable)
		}
		if c.Expect.Result != nil {
			return boolResult(got == UpgradeNone)
		}
		if c.Expect.Upgradeable == "" {
			return "expect.upgradeable or expect.result missing"
		}
		return ""
	case "CutoffNotMet":
		var nq *QualityModel
		if c.New != nil {
			m := newQ()
			nq = &m
		}
		return boolResult(e.CutoffNotMet(p, cur(), b.formatList(c.CurrentFormats), nq))
	case "QualityCutoffNotMet":
		var nq *QualityModel
		if c.New != nil {
			m := newQ()
			nq = &m
		}
		return boolResult(e.QualityCutoffNotMet(p, cur(), nq))
	case "IsUpgradeAllowed":
		return boolResult(e.IsUpgradeAllowed(p, cur(), b.formatList(c.CurrentFormats), newQ(), b.formatList(c.NewFormats)))
	case "IsRevisionUpgrade":
		return boolResult(e.IsRevisionUpgrade(cur(), newQ()))
	case "QualityModelCompare":
		var l, r tquality
		must(json.Unmarshal(c.Left, &l))
		must(json.Unmarshal(c.Right, &r))
		got := sign(p.CompareModels(b.model(&l), b.model(&r), c.GroupOrder))
		if c.Expect.Compare == nil || got != sign(*c.Expect.Compare) {
			return fmt.Sprintf("compare %d, want %v", got, c.Expect.Compare)
		}
		return ""
	case "RevisionCompare":
		var l, r Revision
		must(json.Unmarshal(c.Left, &l))
		must(json.Unmarshal(c.Right, &r))
		got := sign(l.CompareTo(r))
		if c.Expect.Compare == nil || got != sign(*c.Expect.Compare) {
			return fmt.Sprintf("compare %d, want %v", got, c.Expect.Compare)
		}
		return ""
	case "QualityIndexCompare":
		type qi struct {
			Index      int `json:"index"`
			GroupIndex int `json:"groupIndex"`
		}
		var l, r qi
		must(json.Unmarshal(c.Left, &l))
		must(json.Unmarshal(c.Right, &r))
		got := QualityIndex(l).CompareTo(QualityIndex(r), c.GroupOrder)
		if c.Expect.Compare == nil || got != *c.Expect.Compare {
			return fmt.Sprintf("compare %d, want %v", got, c.Expect.Compare)
		}
		return ""
	case "Prioritize":
		var subs []*Subject
		idx := map[*Subject]int{}
		for i, cand := range c.Releases {
			m := c.Media
			if cand.Media != nil {
				m = cand.Media
			}
			cr := cand.Release
			s := b.subject(p, b.media(m), &cr, cand.Episodes)
			subs = append(subs, s)
			idx[s] = i
		}
		got := e.Prioritize(subs)
		var order []int
		for _, s := range got {
			order = append(order, idx[s])
		}
		if c.Expect.First != nil && order[0] != *c.Expect.First {
			return fmt.Sprintf("first %d, want %d (order %v)", order[0], *c.Expect.First, order)
		}
		if c.Expect.Order != nil && fmt.Sprint(order) != fmt.Sprint(c.Expect.Order) {
			return fmt.Sprintf("order %v, want %v", order, c.Expect.Order)
		}
		if c.Expect.First == nil && c.Expect.Order == nil {
			return "expect.first or expect.order missing"
		}
		return ""
	}

	sp, ok := e.Spec(c.Spec)
	if !ok {
		return "unknown spec " + c.Spec
	}
	s := b.subject(p, b.media(c.Media), c.Release, c.Episodes)
	d := e.Run(sp, s)
	if c.Expect.Accepted == nil {
		return "expect.accepted missing"
	}
	if d.Accepted != *c.Expect.Accepted {
		return fmt.Sprintf("accepted %v, want %v (%s: %s)", d.Accepted, *c.Expect.Accepted, d.Reason, d.Message)
	}
	if c.Expect.Reason != "" && string(d.Reason) != c.Expect.Reason {
		return fmt.Sprintf("reason %s, want %s", d.Reason, c.Expect.Reason)
	}
	if c.Expect.Message != "" && d.Message != c.Expect.Message {
		return fmt.Sprintf("message %q, want %q", d.Message, c.Expect.Message)
	}
	return ""
}

func TestArrFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/arr_*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skip("no extracted arr cases")
	}
	type tally struct{ total, pass, known int }
	per := map[string]*tally{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var cf caseFile
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cf); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for i, c := range cf.Cases {
			key := c.Flavour + "/" + c.Fixture
			if per[key] == nil {
				per[key] = &tally{}
			}
			tl := per[key]
			tl.total++
			name := fmt.Sprintf("%s#%d %s.%s(%s)", filepath.Base(f), i, c.Fixture, c.Test, c.Args)
			fail := runCase(c)
			switch {
			case fail == "" && c.KnownFailure == "":
				tl.pass++
			case fail == "" && c.KnownFailure != "":
				t.Errorf("%s: listed as a known failure (%s) but passes", name, c.KnownFailure)
			case fail != "" && c.KnownFailure != "":
				tl.known++
			default:
				t.Errorf("%s: %s", name, fail)
			}
		}
	}
	keys := make([]string, 0, len(per))
	for k := range per {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var total, pass, known int
	for _, k := range keys {
		tl := per[k]
		t.Logf("%-60s %4d/%-4d known failures %d", k, tl.pass, tl.total, tl.known)
		total += tl.total
		pass += tl.pass
		known += tl.known
	}
	t.Logf("arr fixtures: %d/%d pass (%.1f%%), %d known failures", pass, total, 100*float64(pass)/float64(max(total, 1)), known)
}
