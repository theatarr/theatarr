package importarr

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Selection controls Theatarr does not have : release profiles (must contain /
// must not contain, ReleaseRestrictionsSpecification) and delay profiles (DelaySpecification and pending releases).
// A typical arr setup uses neither (no release profile; only the default delay profile, with no delay), so they are
// not imported. Each run reads both and warns about any that would change what the arr grabs, so the gap shows the
// day an arr gains one instead of decisions quietly differing.

// arrDelayProfile is DelayProfileResource (the same in Radarr and Sonarr).
type arrDelayProfile struct {
	ID                             int    `json:"id"`
	EnableUsenet                   bool   `json:"enableUsenet"`
	EnableTorrent                  bool   `json:"enableTorrent"`
	PreferredProtocol              string `json:"preferredProtocol"`
	UsenetDelay                    int    `json:"usenetDelay"`
	TorrentDelay                   int    `json:"torrentDelay"`
	BypassIfHighestQuality         bool   `json:"bypassIfHighestQuality"`
	BypassIfAboveCustomFormatScore bool   `json:"bypassIfAboveCustomFormatScore"`
	MinimumCustomFormatScore       int    `json:"minimumCustomFormatScore"`
	Tags                           []int  `json:"tags"`
}

// arrReleaseProfile is ReleaseProfileResource. Required and Ignored are term lists; older arrs wrote one
// comma-separated string, which the API still accepts.
type arrReleaseProfile struct {
	ID        int             `json:"id"`
	Name      string          `json:"name"`
	Enabled   bool            `json:"enabled"`
	Required  json.RawMessage `json:"required"`
	Ignored   json.RawMessage `json:"ignored"`
	IndexerID int             `json:"indexerId"`
	Tags      []int           `json:"tags"`
}

// fetchSelection reads the instance's delay and release profiles into s.Selection. They only feed warnings, so a
// failed read is itself a warning and never stops the import.
func fetchSelection(ctx context.Context, g Getter, s *Snapshot) error {
	var delays []arrDelayProfile
	if err := g.GetJSON(ctx, "delayprofile", nil, &delays); err != nil {
		s.Selection = []string{fmt.Sprintf("delay profiles not checked: %v", err)}
		return nil
	}
	var releases []arrReleaseProfile
	if err := g.GetJSON(ctx, "releaseprofile", nil, &releases); err != nil {
		s.Selection = []string{fmt.Sprintf("release profiles not checked: %v", err)}
		return nil
	}
	s.Selection = unsupportedSelection(delays, releases, s.Tags)
	return nil
}

// unsupportedSelection lists the profiles that would make the arr decide differently from Theatarr, one line each. Theatarr
// grabs torrents only, as soon as found, with no term filters, so a delay profile counts when it delays torrents or
// turns them off; its usenet settings do not (Theatarr never routes usenet), nor do the bypass switches of a profile with
// no torrent delay. A release profile counts when it is enabled and has terms (the arr reads enabled ones only).
func unsupportedSelection(delays []arrDelayProfile, releases []arrReleaseProfile, tags map[int]string) []string {
	var out []string
	sort.Slice(delays, func(i, j int) bool { return delays[i].ID < delays[j].ID })
	for _, d := range delays {
		var what []string
		if !d.EnableTorrent {
			what = append(what, "torrents disabled")
		}
		if d.EnableTorrent && d.TorrentDelay > 0 {
			w := fmt.Sprintf("torrent delay %d min", d.TorrentDelay)
			if d.BypassIfHighestQuality {
				w += ", bypass if highest quality"
			}
			if d.BypassIfAboveCustomFormatScore {
				w += fmt.Sprintf(", bypass at score %d", d.MinimumCustomFormatScore)
			}
			what = append(what, w)
		}
		if len(what) > 0 {
			out = append(out, fmt.Sprintf("delay profile %d (%s; %s) is not supported: Theatarr grabs without delay",
				d.ID, tagScope(d.Tags, tags), strings.Join(what, ", ")))
		}
	}
	sort.Slice(releases, func(i, j int) bool { return releases[i].ID < releases[j].ID })
	for _, r := range releases {
		req, ign := terms(r.Required), terms(r.Ignored)
		if !r.Enabled || len(req)+len(ign) == 0 {
			continue
		}
		scope := tagScope(r.Tags, tags)
		if r.IndexerID != 0 {
			scope += fmt.Sprintf(", indexer %d", r.IndexerID)
		}
		out = append(out, fmt.Sprintf("release profile %d %q (%s; %d must contain, %d must not contain) is not "+
			"supported: Theatarr applies no release profile terms", r.ID, r.Name, scope, len(req), len(ign)))
	}
	return out
}

// tagScope names the tags a profile applies to, "all titles" without tags.
func tagScope(ids []int, labels map[int]string) string {
	if len(ids) == 0 {
		return "all titles"
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if l, ok := labels[id]; ok {
			names = append(names, l)
		} else {
			names = append(names, fmt.Sprint(id))
		}
	}
	sort.Strings(names)
	return "tags " + strings.Join(names, ",")
}

// terms decodes a term list: a JSON array of strings, or one comma-separated string.
func terms(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		var one string
		if json.Unmarshal(raw, &one) != nil {
			return nil
		}
		list = strings.Split(one, ",")
	}
	out := list[:0]
	for _, t := range list {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}
