package indexer

import (
	"encoding/base32"
	"encoding/hex"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// Release is one search result, the same shape for every source.
type Release struct {
	// Indexer is the policy key of the source; Kind its kind (torznab, dmm, ...).
	Indexer string `json:"indexer"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	// InfoHash is the lower-case hex BitTorrent v1 hash, or "" when the source gives none.
	InfoHash string `json:"info_hash,omitempty"`
	// Size in bytes; 0 when unknown.
	Size int64 `json:"size"`
	// Seeders and Peers (seeders plus leechers, as Torznab counts them); -1 when the source gives none.
	Seeders int `json:"seeders"`
	Peers   int `json:"peers"`
	// PublishDate is zero when unknown.
	PublishDate time.Time `json:"publish_date,omitzero"`
	// Privacy is a label for display (the item's own, else the indexer's from the policy); nothing routes on it.
	Privacy policy.Privacy `json:"privacy"`
	// Categories are Newznab category ids.
	Categories []int `json:"categories,omitempty"`
	// Flags are indexer flags (Torznab tags such as freeleech, internal, scene; seadex and seadex-best).
	Flags []string `json:"flags,omitempty"`
	// Languages are the languages the source gave for the release, as it wrote them (Torznab language attributes:
	// Prowlarr writes the language's English name). The decision engine resolves them as the arrs'
	// IsoLanguages.FindByName does. Empty when the source gave none.
	Languages []string `json:"languages,omitempty"`
	// IDs the source attached, when any.
	IMDb string `json:"imdb,omitempty"`
	TMDb int    `json:"tmdb,omitempty"`
	TVDB int    `json:"tvdb,omitempty"`
	// Extras are source-specific facts: cached_on (dmm), file, file_idx, source, size_is_file (torrentio), seadex
	// details, volume factors.
	Extras map[string]string `json:"extras,omitempty"`
	// CachedOn are the debrid providers ("rd", "ad", "tb", in that order) that evidence says hold the release cached:
	// its own cached_on, the same hash in a DMM feed of the same search, stored evidence, TorBox's check. CachedVia
	// says where each came from (an indexer key, feed:<indexer>, feed:<indexer>/availability, check:torbox). Empty means unknown, never uncached.
	// Filled by Service.ApplyCachedOn and Service.CheckCached, not by the adapters.
	CachedOn  []string          `json:"cached_on,omitempty"`
	CachedVia map[string]string `json:"cached_via,omitempty"`

	// GUID is the source's item id. It can be a URL, so it stays in memory like the links.
	GUID string `json:"-"`
	// Link is the download link (a .torrent URL, often with a key); Magnet is a magnet link. In memory only.
	Link   Link `json:"-"`
	Magnet Link `json:"-"`
	// Additional identities supplied by an adapter (including duplicate attributes), checked before filtering.
	identityHashes []string
	// unidentifiedMagnet says an adapter saw a magnet that yields no v1 identity (ReleaseIdentity fails closed).
	unidentifiedMagnet bool
}

// Link is a download link or magnet held in memory only. It prints as a placeholder, marshals to null and logs
// redacted, so it cannot leak a key or passkey into logs, JSON or the database by accident. URL returns the value.
type Link struct {
	u string
}

// NewLink wraps a link.
func NewLink(u string) Link { return Link{u: strings.TrimSpace(u)} }

// URL returns the link itself. Never log or store it when Keyed.
func (l Link) URL() string { return l.u }

// Empty reports whether there is no link.
func (l Link) Empty() bool { return l.u == "" }

// IsMagnet reports whether the link is a magnet URI.
func (l Link) IsMagnet() bool { return strings.HasPrefix(strings.ToLower(l.u), "magnet:") }

// String never shows the link.
func (l Link) String() string {
	switch {
	case l.u == "":
		return ""
	case l.IsMagnet():
		return "<magnet>"
	default:
		return "<link>"
	}
}

// GoString keeps %#v from showing it too.
func (l Link) GoString() string { return l.String() }

// LogValue keeps slog from showing it.
func (l Link) LogValue() slog.Value { return slog.StringValue(l.String()) }

// MarshalJSON writes null: links are never serialised.
func (l Link) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

// MarshalText also hides it (text encoders, yaml).
func (l Link) MarshalText() ([]byte, error) { return []byte(l.String()), nil }

var longToken = regexp.MustCompile(`[A-Za-z0-9_-]{20,}`)

// Keyed reports whether the link may carry a credential: an http(s) link with a query string or a long token in
// its path, or a magnet whose trackers do. Fails closed: anything unparseable counts as keyed. A bare magnet and a
// plain public download path (https://nyaa.si/download/123.torrent) are not keyed.
func (l Link) Keyed() bool {
	if l.u == "" {
		return false
	}
	u, err := url.Parse(l.u)
	if err != nil {
		return true
	}
	if strings.EqualFold(u.Scheme, "magnet") {
		raw := u.RawQuery
		if raw == "" {
			raw = u.Opaque[strings.Index(u.Opaque, "?")+1:]
		}
		q, err := url.ParseQuery(raw)
		if err != nil {
			return true
		}
		for _, tr := range q["tr"] {
			if (Link{u: tr}).Keyed() {
				return true
			}
		}
		return false
	}
	if u.User != nil || u.RawQuery != "" {
		return true
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if longToken.MatchString(seg) || strings.Contains(seg, "=") {
			return true
		}
	}
	return false
}

// BareMagnet is magnet:?xt=urn:btih:<hash> with no trackers, the only form that goes to debrid (docs/design/providers.md).
func BareMagnet(hash string) Link {
	h := NormalizeHash(hash)
	if h == "" {
		return Link{}
	}
	return Link{u: "magnet:?xt=urn:btih:" + h}
}

var hexHash = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
var b32Hash = regexp.MustCompile(`^[A-Za-z2-7]{32}$`)

// NormalizeHash returns a v1 info hash as lower-case hex: it accepts hex and base32 forms and returns "" for
// anything else.
func NormalizeHash(h string) string {
	h = strings.TrimSpace(h)
	switch {
	case hexHash.MatchString(h):
		return strings.ToLower(h)
	case b32Hash.MatchString(h):
		b, err := base32.StdEncoding.DecodeString(strings.ToUpper(h))
		if err != nil || len(b) != 20 {
			return ""
		}
		return hex.EncodeToString(b)
	}
	return ""
}

// isMagnetURI reports whether s is a magnet URI (scheme compared in any case, surrounding space ignored).
func isMagnetURI(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) >= 7 && strings.EqualFold(s[:7], "magnet:")
}

// magnetHashes parses every decoded v1 exact-topic identity, including base32 hashes. It parses leniently, not as
// a URL: a literal control character or an unescaped '#' must not hide an xt, so it takes everything
// after the first '?', splits it on '&' and '#' (never fragment semantics), compares keys in any case (xt and
// xt.N), unescapes each key and value with the raw text as fallback, and reads the hash as the run of letters and
// digits after urn:btih:.
func magnetHashes(m string) []string {
	m = strings.TrimSpace(m)
	if !isMagnetURI(m) {
		return nil
	}
	i := strings.IndexByte(m, '?')
	if i < 0 {
		return nil
	}
	unescape := func(s string) string {
		if u, err := url.QueryUnescape(s); err == nil {
			return u
		}
		return s
	}
	var out []string
	for _, part := range strings.FieldsFunc(m[i+1:], func(r rune) bool { return r == '&' || r == '#' }) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(unescape(k)))
		if k != "xt" && !strings.HasPrefix(k, "xt.") {
			continue
		}
		v = strings.TrimSpace(unescape(v))
		if len(v) < 9 || !strings.EqualFold(v[:9], "urn:btih:") {
			continue
		}
		v = v[9:]
		n := strings.IndexFunc(v, func(r rune) bool {
			return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
		})
		if n >= 0 {
			v = v[:n]
		}
		if h := NormalizeHash(v); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// addMagnetIdentity records the identities a magnet supplied by an adapter names, or that it names none.
func (r *Release) addMagnetIdentity(m string) {
	if !isMagnetURI(m) {
		return
	}
	hs := magnetHashes(m)
	if len(hs) == 0 {
		r.unidentifiedMagnet = true
	}
	r.identityHashes = append(r.identityHashes, hs...)
}

// HashFromMagnet returns the magnet's unambiguous v1 identity, or "".
func HashFromMagnet(m string) string {
	var hash string
	for _, h := range magnetHashes(m) {
		if hash != "" && h != hash {
			return ""
		}
		hash = h
	}
	return hash
}

// ReleaseIdentity is a release's one normalised torrent identity: its reported info hash, else the hash its magnet
// (or a magnet download link) names. conflict says the identity is conflicting or unverifiable (a reported hash and a
// magnet, or two magnets, disagree or cannot be compared): such a release is rejected, never routed by either
// hash. Decisions, the journal, shadow and the router all use this identity.
//
// It fails closed: a magnet that is present but yields no v1 identity (unparseable, or no urn:btih: exact topic)
// conflicts with any other identity the release reports, since it may name a different torrent.
func ReleaseIdentity(r Release) (hash string, conflict bool) {
	hashes := append([]string{NormalizeHash(r.InfoHash)}, r.identityHashes...)
	unidentified := r.unidentifiedMagnet
	for _, l := range []Link{r.Magnet, r.Link} {
		if !isMagnetURI(l.URL()) {
			continue
		}
		hs := magnetHashes(l.URL())
		if len(hs) == 0 {
			unidentified = true
		}
		hashes = append(hashes, hs...)
	}
	for _, h := range hashes {
		switch {
		case h == "":
		case hash == "":
			hash = h
		case h != hash:
			return hash, true
		}
	}
	return hash, unidentified && hash != ""
}

// NormalizeIdentities gives each release its identity (ReleaseIdentity) and drops the releases whose identity
// conflicts, returned apart so the caller can report them.
func NormalizeIdentities(rs []Release) (kept, conflicting []Release) {
	kept = rs[:0:0]
	for _, r := range rs {
		h, conflict := ReleaseIdentity(r)
		if conflict {
			conflicting = append(conflicting, r)
			continue
		}
		r.InfoHash = h
		kept = append(kept, r)
	}
	return kept, conflicting
}

// Names Real-Debrid refuses on add. The eleven strings are the published rule (Zurg Patreon post of 2026-10-10,
// confirmed by DMM's notes of 2026-10-13). Since RD's change of 2026-10-14
// (measured by zurg's maintainers), three of them are matched in exact case only: "WEB-DL", "WEB.x264" and
// "WEB.H264" are refused, "web-dl", "Web-Dl" and "WEB.h264" are taken. The others are refused in any case, and the
// DTS tag needs a resolution before it ("1080p.BluRay.DTS"; "UHD.BluRay.DTS" is taken). RD judges the torrent's
// root name.
var (
	rdRefusedExactCase = []string{"WEB-DL", "WEB.x264", "WEB.H264"}
	rdRefusedAnyCase   = []string{"webrip", "bdrip", "hdrip", "dvdrip", "hdtv.x264", "hdtv.xvid", "bluray.x264",
		"p.bluray.dts"}
	// spacedTitleKinds are sources that replace a name's dots with spaces (Zilean; TheRARBG too, through Prowlarr).
	spacedTitleKinds = map[string]bool{"zilean": true}
)

// RealDebridRefuses reports whether Real-Debrid refuses an add with this root name. loose also matches a name whose
// dots an indexer replaced with spaces.
func RealDebridRefuses(name string, loose bool) bool {
	if loose {
		name = strings.ReplaceAll(name, " ", ".")
	}
	for _, s := range rdRefusedExactCase {
		if strings.Contains(name, s) {
			return true
		}
	}
	lower := strings.ToLower(name)
	for _, s := range rdRefusedAnyCase {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// RealDebridRefusesRelease applies the rule loosely for sources that mangle dots. A title is the source's name for
// the release; the root name RD judges can differ, so the router still handles a refusal.
func RealDebridRefusesRelease(r *Release) bool {
	loose := spacedTitleKinds[r.Kind] || r.Kind == "zilean" || r.Kind == "torrentio" ||
		slices.Contains(r.CachedOn, "rd") || r.Kind == "dmm" && slices.Contains(parseCachedOn(r.Extras["cached_on"]), "rd")
	return RealDebridRefuses(r.Title, loose)
}
