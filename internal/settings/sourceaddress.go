package settings

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
)

// SourceAddress is where a provider or indexer record of p sends its credential (entity "providers" or "indexers"):
// its kind and its url's scheme, host and port, plus an indexer's proxy and a cardigann indexer's definition and
// further hosts, which get the credential too. A record whose address changes while it keeps its credential needs confirmation
// (credentialAddressEffects), and the source tests refuse to send a stored credential to another address. Paths and
// queries are not part of it. "" when p has no such record or its url has no host.
func SourceAddress(p *policy.Policy, entity, key string) string {
	if p == nil {
		return ""
	}
	var kind, raw, definition, proxy string
	var hosts []string
	switch entity {
	case "indexers":
		if ix := p.Indexers[key]; ix != nil {
			kind, raw, definition, proxy = ix.Kind, ix.URL, ix.Definition, ix.Proxy
			for _, h := range ix.Hosts {
				hosts = append(hosts, strings.ToLower(h))
			}
			slices.Sort(hosts)
			hosts = slices.Compact(hosts)
		}
	case "providers":
		if pv := p.Providers[key]; pv != nil {
			kind, raw = pv.Kind, pv.URL
		}
	}
	origin := sourceOrigin(raw)
	if origin == "" {
		return ""
	}
	addr := kind + " " + origin
	if proxy != "" {
		addr += " proxy " + sourceOrigin(proxy)
	}
	if definition != "" {
		addr += " definition " + definition
	}
	if len(hosts) > 0 {
		addr += " +" + strings.Join(hosts, ",")
	}
	return addr
}

// sourceOrigin compares destinations without credentials, paths or query strings.
func sourceOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443", "socks5": "1080", "socks5h": "1080"}[scheme]
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// keptCredential reports whether a record resolves to the same credential in both snapshots, as WithSecret would
// (environment fallback included): the stored key goes on to whatever address next gives the record.
func keptCredential(previous, next *Snapshot, entity, key string, lookup LookupSecret) bool {
	old, _ := previous.resolve(entity, key, lookup)
	now, _ := next.resolve(entity, key, lookup)
	return now != "" && now == old
}

// credentialAddressEffects reports each provider and indexer record whose address (SourceAddress) changes while it
// keeps its credential: saving it sends the stored key to the new address, so a signed-in user confirms
// it; API keys and MCP are refused. Supplying a new credential with the new address needs no confirmation, nor does
// a record without one. It applies whether or not the record is enabled or an active version uses it. A previous view
// without records has no credentials to compare.
func credentialAddressEffects(previous, next *Snapshot, lookup LookupSecret) []ConfirmationEffect {
	if previous.secrets == nil {
		return nil
	}
	var effects []ConfirmationEffect
	check := func(entity, what, key string) {
		before, after := SourceAddress(previous.Policy, entity, key), SourceAddress(next.Policy, entity, key)
		if before == "" || before == after || !keptCredential(previous, next, entity, key, lookup) {
			return
		}
		effects = append(effects, ConfirmationEffect{Effect: fmt.Sprintf(
			"%s %s changes its address from %s to %s and keeps its stored credential: Theatarr sends that credential to the new address once the change is saved.",
			what, key, before, after)})
	}
	for _, r := range next.Records.Providers {
		check("providers", "Provider", r.Key)
	}
	for _, r := range next.Records.Indexers {
		check("indexers", "Indexer", r.Key)
	}
	return effects
}
