package demo

import (
	"strings"

	"github.com/theatarr/theatarr/internal/ceiling"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
)

// ceilings adds auditably known, likely, higher-source and unknown examples, with no live lookups.
func (s *seeder) ceilings() error {
	// The demo imports also keep their quality order, as the ordinary arr importer does.
	for _, instance := range s.instances() {
		vp := s.pol.Versions[s.pol.Import.Arr[instance].Version]
		pr := s.pol.Profiles[vp.Profile]
		if pr == nil || pr.FromArr != "" {
			continue
		}
		fl := format.Radarr
		if vp.Kind == "series" {
			fl = format.Sonarr
		}
		var items []format.TrashQualityItem
		for _, it := range pr.Qualities {
			items = append(items, format.TrashQualityItem{Name: it.Name, Allowed: true, Items: it.Qualities})
		}
		p, err := profile.FromItems(fl, vp.Profile, items, pr.Cutoff, true)
		if err != nil {
			return err
		}
		s.exec(`UPDATE arr_quality_profile SET quality_items=? WHERE instance=?`, jsonOf(p.JSON().Items), instance)
	}
	for _, t := range s.titles {
		if !strings.HasPrefix(t.scenario, "ceiling_") {
			continue
		}
		seasons := []int{-1}
		tier := "web_2160"
		if t.kind == "series" {
			seasons = []int{1}
			tier = "web_1080"
		}
		for _, season := range seasons {
			if t.scenario != scCeilingUnknown {
				at := s.ago(days(120))
				if err := ceiling.Sight(s.ctx, s.tx, t.id, season, ceiling.Evidence{Tier: tier, Source: "search:demo-disc", Kind: "torznab", Example: t.name + " WEB", FirstSeen: at, LastSeen: at}); err != nil {
					return err
				}
			}
			if t.scenario == scCeilingHigher {
				if err := ceiling.Sight(s.ctx, s.tx, t.id, season, ceiling.Evidence{Tier: "uhd_bd", Source: "manual_catalog", Kind: "catalog", PositiveOnly: true, Example: "4K disc catalog entry; no matching release found", FirstSeen: s.now, LastSeen: s.now}); err != nil {
					return err
				}
			}
			if t.scenario == scCeilingKnown {
				for _, v := range t.versions {
					for _, daysAgo := range []int{100, 50, 1} {
						at := s.ago(days(float64(daysAgo)))
						s.exec(`INSERT INTO ceiling_search(title_id,season,version_key,searched_at,source,best_tier) VALUES(?,?,?,?,?,?)`, t.id, season, v.key, ms(at), "search", tier)
						s.exec(`INSERT INTO ceiling_search_indexer(search_id,indexer,source_kind,positive_only,complete) SELECT id,'demo-disc','torznab',0,1 FROM ceiling_search WHERE title_id=? AND season=? AND version_key=? AND searched_at=?`, t.id, season, v.key, ms(at))
					}
				}
			}
		}
		if err := ceiling.Compute(s.ctx, s.tx, s.pol, t.id, s.now); err != nil {
			return err
		}
	}
	return s.err
}
