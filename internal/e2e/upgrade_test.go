package e2e

import (
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

func (e *environment) assertUpgradeDecision(id int64, version string, rel release, oldQuality, newQuality string) {
	e.t.Helper()
	d, err := e.ac.Decide(e.ctx, decide.Release{Release: indexer.Release{Title: rel.name, InfoHash: rel.hash,
		Indexer: "public", Size: rel.size, Seeders: 20, Privacy: policy.PrivacyPublic}}, id, version)
	must(e.t, err)
	if !d.Upgrade || d.Existing == nil || d.Existing.Quality != oldQuality ||
		d.UpgradeReason != "better quality: "+newQuality+" over "+oldQuality {
		e.t.Fatalf("upgrade decision from Theatarr link: %+v", d)
	}
}
