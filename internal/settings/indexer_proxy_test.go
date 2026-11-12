package settings

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// An indexer's proxy decides which host receives its requests (and, for an http:// indexer, the
// stored key in clear). An API key (WithoutConfirmation) can change it with no effect.
func TestIndexerProxyNeedsConfirmation(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	target := ""
	for _, v := range active.Policy.Versions {
		if v.Mode == policy.ModeActive && len(v.Indexers) > 0 {
			target = v.Indexers[0]
		}
	}
	if target == "" {
		t.Fatal("no indexer")
	}
	ctx := WithoutConfirmation(context.Background())
	req := saveRequest(p, active)
	req.SecretWrites = map[string]Secret{"indexers/" + target: NewSecret("stored-indexer-key")}
	keyed, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatalf("fixture key storage: %v", err)
	}
	req = saveRequest(p, keyed)
	editIndexer(&req.Records, target, func(ix *Indexer) { ix.Config.Proxy = "http://collector.invalid:3128" })
	_, err = m.SaveAndApply(ctx, req)
	requireConfirmation(t, err, "proxy")
}

func TestProxyOrigins(t *testing.T) {
	t.Parallel()
	p := &policy.Policy{Indexers: map[string]*policy.Indexer{"site": {Kind: "torznab", URL: "https://site.example/", Proxy: "http://PROXY.example"}}}
	base := SourceAddress(p, "indexers", "site")
	if !strings.Contains(base, "proxy http://proxy.example:80") {
		t.Fatalf("proxy missing: %s", base)
	}
	p.Indexers["site"].Proxy = "http://proxy.example:80/"
	if SourceAddress(p, "indexers", "site") != base {
		t.Fatal("equivalent proxy changed address")
	}
	p.Indexers["site"].Proxy = "socks5://proxy.example:1080"
	if SourceAddress(p, "indexers", "site") == base {
		t.Fatal("proxy transport change ignored")
	}
}

func TestShadowIndexerProxyNeedsCredentialConfirmation(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	uses := map[string]bool{}
	for _, v := range active.Policy.Versions {
		if v.Mode == policy.ModeActive {
			for _, key := range v.Indexers {
				uses[key] = true
			}
		}
	}
	target := ""
	for _, key := range sortedPolicyKeys(active.Policy.Indexers) {
		if !uses[key] {
			target = key
			break
		}
	}
	if target == "" {
		t.Fatal("no unused indexer")
	}
	req := saveRequest(p, active)
	req.SecretWrites = map[string]Secret{"indexers/" + target: NewSecret("stored-indexer-key")}
	keyed, err := m.SaveAndApply(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req = saveRequest(p, keyed)
	editIndexer(&req.Records, target, func(ix *Indexer) { ix.Config.Proxy = "http://collector.invalid:3128" })
	_, err = m.SaveAndApply(WithoutConfirmation(context.Background()), req)
	requireConfirmation(t, err, "changes its address")
	if _, err := m.store.Save(context.Background(), withBase(req, m.base)); err == nil {
		t.Fatal("direct save moved the credential without confirmation")
	}
	proof, err := m.Confirm(context.Background(), req)
	if err != nil || proof.Token == "" {
		t.Fatalf("confirmation: %+v %v", proof, err)
	}
	req.ConfirmationToken = proof.Token
	saved, err := m.SaveAndApply(context.Background(), req)
	if err != nil || !saved.CredentialSet("indexers", target, nil) || saved.Policy.Indexers[target].Proxy != "http://collector.invalid:3128" {
		t.Fatalf("confirmed proxy change: %v", err)
	}
}
