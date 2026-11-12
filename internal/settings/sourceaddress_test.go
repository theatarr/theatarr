package settings

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// A cardigann indexer's hosts and definition get its credential too, so they are part of the address a stored
// credential is bound to; so is the kind. Host order, case and the default port are not.
func TestSourceAddress(t *testing.T) {
	t.Parallel()
	ix := func(change func(*policy.Indexer)) *policy.Policy {
		v := &policy.Indexer{Kind: "cardigann", URL: "https://site.test/", Definition: "site", Hosts: []string{"b.test", "a.test"}}
		if change != nil {
			change(v)
		}
		return &policy.Policy{Indexers: map[string]*policy.Indexer{"site": v}}
	}
	base := SourceAddress(ix(nil), "indexers", "site")
	if base != "cardigann https://site.test:443 definition site +a.test,b.test" {
		t.Fatalf("address %q", base)
	}
	for name, change := range map[string]func(*policy.Indexer){
		"host order": func(v *policy.Indexer) { v.Hosts = []string{"A.test", "b.test"} },
		"path":       func(v *policy.Indexer) { v.URL = "https://SITE.test:443/other?x=1" },
	} {
		if got := SourceAddress(ix(change), "indexers", "site"); got != base {
			t.Errorf("%s changes the address: %q", name, got)
		}
	}
	for name, change := range map[string]func(*policy.Indexer){
		"added host": func(v *policy.Indexer) { v.Hosts = append(v.Hosts, "evil.test") },
		"definition": func(v *policy.Indexer) { v.Definition = "other" },
		"kind":       func(v *policy.Indexer) { v.Kind = "torznab" },
		"scheme":     func(v *policy.Indexer) { v.URL = "http://site.test/" },
		"port":       func(v *policy.Indexer) { v.URL = "https://site.test:8443/" },
		"host":       func(v *policy.Indexer) { v.URL = "https://collector.invalid/" },
	} {
		if got := SourceAddress(ix(change), "indexers", "site"); got == base {
			t.Errorf("%s keeps the address", name)
		}
	}
	if SourceAddress(ix(nil), "providers", "site") != "" || SourceAddress(nil, "indexers", "site") != "" {
		t.Error("no record has an address")
	}
}

// Repointing a saved provider keeps its stored credential only with a signed-in user's confirmation;
// an API key or MCP caller is refused, and a new credential sent along needs none.
func TestProviderURLRepointNeedsConfirmation(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	key := active.Policy.Versions["movie-web"].Providers[0]
	if !active.CredentialSet("providers", key, nil) {
		t.Fatalf("fixture provider %s has no stored credential", key)
	}
	req := saveRequest(p, active)
	editProvider(&req.Records, key, func(pv *Provider) { pv.Config.URL = "https://collector.invalid/" })
	_, err := m.SaveAndApply(WithoutConfirmation(context.Background()), req)
	var refused *ConfirmationError
	if !errors.As(err, &refused) || !refused.ConfirmInUI || refused.Confirmation != nil {
		t.Fatalf("API key repoint: %v", err)
	}
	requireConfirmation(t, err, "Provider "+key+" changes its address")
	if !strings.Contains(refused.Effects[0].Effect, "collector.invalid:443") {
		t.Fatalf("effect does not name the new address: %+v", refused.Effects)
	}
	if m.Current().Policy.Providers[key].URL == "https://collector.invalid/" {
		t.Fatal("refused repoint published")
	}

	// A browser session gets a token with the refusal and confirms with it.
	ctx := WithConfirmer(context.Background(), "user:1")
	_, err = m.SaveAndApply(ctx, req)
	if !errors.As(err, &refused) || refused.Confirmation == nil {
		t.Fatalf("session repoint: %v", err)
	}
	req.ConfirmationToken = refused.Confirmation.Token
	saved, err := m.SaveAndApply(ctx, req)
	if err != nil || saved.Policy.Providers[key].URL != "https://collector.invalid/" || !saved.CredentialSet("providers", key, nil) {
		t.Fatalf("confirmed repoint: %v", err)
	}

	// A new credential with a new address needs no confirmation, even from an API key.
	req = saveRequest(p, saved)
	editProvider(&req.Records, key, func(pv *Provider) { pv.Config.URL = "https://api.real-debrid.com/rest/1.0/" })
	req.SecretWrites = map[string]Secret{"providers/" + key: NewSecret("new-account-key")}
	if _, err := m.SaveAndApply(WithoutConfirmation(context.Background()), req); err != nil {
		t.Fatalf("new credential with new address: %v", err)
	}
}

// The same for an indexer only shadow versions search, and the store refuses the change for a caller
// that skips the coordinator.
func TestShadowIndexerURLRepointNeedsConfirmation(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	activeUses := map[string]bool{}
	for _, v := range active.Policy.Versions {
		if v.Mode == policy.ModeActive {
			for _, ix := range v.Indexers {
				activeUses[ix] = true
			}
		}
	}
	target := ""
	for _, v := range active.Policy.Versions {
		for _, ix := range v.Indexers {
			if !activeUses[ix] {
				target = ix
			}
		}
	}
	if target == "" {
		t.Fatal("fixture has no indexer used only by shadow versions")
	}
	ctx := WithoutConfirmation(context.Background())
	req := saveRequest(p, active)
	req.SecretWrites = map[string]Secret{"indexers/" + target: NewSecret("stored-indexer-key")}
	keyed, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatalf("storing the key: %v", err)
	}
	req = saveRequest(p, keyed)
	editIndexer(&req.Records, target, func(ix *Indexer) { ix.Config.URL = "https://collector.invalid" })
	_, err = m.SaveAndApply(ctx, req)
	requireConfirmation(t, err, "Indexer "+target+" changes its address")
	if _, err := m.store.Save(context.Background(), withBase(req, m.base)); err == nil {
		t.Fatal("store saved an unconfirmed repoint")
	}
	if got := m.Current().Policy.Indexers[target].URL; got == "https://collector.invalid" {
		t.Fatal("refused repoint published")
	}
}

func withBase(req SaveRequest, base *policy.Policy) SaveRequest {
	req.Base = base
	return req
}

// Settings suspicion: a confirmation token is bound to the user it was issued to. Another user's attempt
// neither uses it nor uses it up.
func TestConfirmationTokenBoundToConfirmer(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	key := active.Policy.Versions["movie-web"].Providers[0]
	req := saveRequest(p, active)
	editProvider(&req.Records, key, func(pv *Provider) { pv.Config.URL = "https://collector.invalid/" })
	alice, bob := WithConfirmer(context.Background(), "user:1"), WithConfirmer(context.Background(), "user:2")
	proof, err := m.Confirm(alice, req)
	if err != nil || proof.Token == "" {
		t.Fatalf("confirm: %+v %v", proof, err)
	}
	req.ConfirmationToken = proof.Token
	if _, err := m.SaveAndApply(bob, req); err == nil {
		t.Fatal("another user used the token")
	}
	if _, err := m.SaveAndApply(alice, req); err != nil {
		t.Fatalf("issuer's token was used up by another user: %v", err)
	}
}

// The startup fallback check leaves out a credential going back to the fallback generation's own address: that
// generation was published with it.
func TestFallbackEffectsIgnoreOwnAddress(t *testing.T) {
	t.Parallel()
	m, p, active := activeWebFixture(t)
	key := active.Policy.Versions["movie-web"].Providers[0]
	req := saveRequest(p, active)
	editProvider(&req.Records, key, func(pv *Provider) { pv.Config.URL = "https://collector.invalid/" })
	head, err := m.store.Candidate(context.Background(), withBase(req, m.base))
	if err != nil {
		t.Fatal(err)
	}
	effects, err := m.store.FallbackEffects(context.Background(), head, active)
	if err != nil || len(effects) != 0 {
		t.Fatalf("fallback effects: %+v %v", effects, err)
	}
	if got := credentialAddressEffects(head, active, nil); len(got) != 1 {
		t.Fatalf("address effects: %+v", got)
	}
}

func editIndexer(records *Records, key string, change func(*Indexer)) {
	for i := range records.Indexers {
		if records.Indexers[i].Key == key {
			change(&records.Indexers[i])
		}
	}
}
