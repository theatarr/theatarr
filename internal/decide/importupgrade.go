package decide

import (
	"fmt"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
)

// ImportUpgrade is the import's UpgradeSpecification (Sonarr's EpisodeImport and Radarr's MovieImport
// Specifications/UpgradeSpecification.cs, the same in v4 and v5): may a file being imported replace
// a file the version holds? Both files are scored from their inputs the way a decision scores an existing file
// (seriesFile, existingFile), with the version's profile and format set.
//
// As the arrs, a lower quality (profile order, groups equal), a lower revision of the same quality (unless the
// profile's propers setting is do_not_prefer) and a lower format score at the same quality refuse it, with the arr's
// message. Where the arrs import a file that is merely no worse, Theatarr replaces only on a gain: a better quality, a
// better revision under prefer_and_upgrade, or a better format score. Equal quality, revision and format score is
// accepted, as the arrs' UpgradeSpecification does not reject a file merely for tying the current file.
//
// An existing file whose quality is not one of the flavour's (an arr row without one) is replaced, as before. Only
// Year and OriginalLanguage of t are read.
func (e *Engine) ImportUpgrade(t *Title, v Version, nw, cur *ExistingFile) (bool, string, error) {
	if v.Policy == nil {
		v.Policy = e.Policy.Versions[v.Key]
	}
	if v.Policy == nil {
		return false, "", fmt.Errorf("decide: unknown version %q", v.Key)
	}
	fl := flavourOf(v.Policy.Kind)
	p := e.Profile(v.Policy.Profile, fl)
	if p == nil || p.Profile == nil || p.Engine == nil {
		return false, "", fmt.Errorf("decide: version %q: profile %q was not built", v.Key, v.Policy.Profile)
	}
	if v.Policy.PreferSeaDex && SameTorrent(nw, cur) {
		x := *cur
		x.SeaDexPreference, x.SeaDex = nw.SeaDexPreference, nw.SeaDex
		cur = &x
	}
	original := languageID(fl, t.OriginalLanguage)
	score := func(x *ExistingFile) (*profile.File, error) {
		if fl == format.Sonarr {
			return seriesFile(x, t, original, p)
		}
		return existingFile(fl, x, t, original, p)
	}
	n, err := score(nw)
	if err != nil {
		return false, "", err
	}
	if v.Policy.PreferSeaDex {
		allowed := false
		for _, q := range p.Profile.AllowedQualities() {
			if q.ID != 0 && q.ID == n.Quality.Quality.ID {
				allowed = true
				break
			}
		}
		if !allowed {
			return false, "actual file quality is not allowed by the profile", nil
		}
	}
	if cur == nil {
		return true, "first import", nil
	}
	c, err := score(cur)
	if err != nil {
		return true, "existing file has no known quality", nil
	}
	pr, mode := p.Profile, p.Engine.Config.DownloadPropersAndRepacks
	if order, ok := profile.SeaDexOrder(v.Policy.PreferSeaDex, n.Quality.SeaDexPreference, c.Quality.SeaDexPreference); ok {
		if n.Quality.Quality.ID == 0 {
			return false, "SeaDex feature quality is unknown", nil
		}
		if order < 0 {
			return false, "existing file has higher SeaDex preference", nil
		}
		if !pr.UpgradeAllowed || v.Policy.UpgradeRule() == policy.UpgradesOff {
			return false, "upgrades are disabled", nil
		}
		return true, fmt.Sprintf("preferred SeaDex release; actual quality %s over %s",
			n.Quality.Quality.Name, c.Quality.Quality.Name), nil
	}
	qc := pr.CompareQualities(n.Quality.Quality, c.Quality.Quality, false)
	if qc < 0 {
		return false, fmt.Sprintf("Not an upgrade for existing file(s). Existing quality: %s. New Quality %s.",
			c.Quality.Quality.Name, n.Quality.Quality.Name), nil
	}
	if qc > 0 {
		return true, fmt.Sprintf("better quality: %s over %s", n.Quality.Quality.Name, c.Quality.Quality.Name), nil
	}
	rc := n.Quality.Revision.CompareTo(c.Quality.Revision)
	if mode != profile.DoNotPrefer && rc < 0 {
		return false, "Not a quality revision upgrade for existing file(s)", nil
	}
	ns, cs := pr.FormatScore(n.Formats), pr.FormatScore(c.Formats)
	if ns < cs {
		return false, fmt.Sprintf("Not a Custom Format upgrade for existing file(s). New: [%s] (%d) do not improve on Existing: [%s] (%d)",
			importFormatNames(n.Formats), ns, importFormatNames(c.Formats), cs), nil
	}
	if mode == profile.PreferAndUpgrade && rc > 0 {
		return true, fmt.Sprintf("better revision: %s over %s", n.Quality.Revision, c.Quality.Revision), nil
	}
	if ns > cs {
		return true, fmt.Sprintf("better format score: %d over %d", ns, cs), nil
	}
	if rc == 0 {
		return true, fmt.Sprintf("same quality (%s) and custom format score (%d)", n.Quality.Quality.Name, ns), nil
	}
	return false, fmt.Sprintf("Not an upgrade for existing file(s): the same quality (%s) and custom format score (%d)",
		n.Quality.Quality.Name, ns), nil
}

func importFormatNames(fs []*format.Format) string {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.Name
	}
	return strings.Join(names, ", ")
}
