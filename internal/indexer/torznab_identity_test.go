package indexer

import (
	"net/http"
	"strings"
	"testing"
)

// Magnet identities are parsed leniently (uppercase keys, control characters, an unescaped '#') and
// a magnet that yields no identity beside another reported identity fails closed.
func TestLenientMagnetIdentities(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	for name, magnet := range map[string]string{
		"uppercase_key":  "magnet:?XT=urn:btih:" + b,
		"numbered_key":   "magnet:?Xt.1=urn:btih:" + b,
		"tab":            "magnet:?xt=urn:btih:" + b + "&dn=Show\tS01",
		"newline":        "magnet:?dn=Show\nS01&xt=urn:btih:" + b,
		"del":            "magnet:?dn=Show\x7fS01&xt=urn:btih:" + b,
		"hash_before_xt": "magnet:?dn=Show #1 [1080p]&xt=urn:btih:" + b,
	} {
		t.Run(name, func(t *testing.T) {
			if HashFromMagnet(magnet) != b {
				t.Fatalf("HashFromMagnet = %q, want %q", HashFromMagnet(magnet), b)
			}
			if h, conflict := ReleaseIdentity(Release{InfoHash: a, Magnet: NewLink(magnet)}); !conflict {
				t.Fatalf("conflicting identity accepted as %s", h)
			}
			if h, conflict := ReleaseIdentity(Release{Magnet: NewLink(magnet)}); conflict || h != b {
				t.Fatalf("magnet-only identity = %s %v", h, conflict)
			}
		})
	}
	// Two identities split by an unescaped '#' are both seen.
	for _, m := range []string{
		"magnet:?xt=urn:btih:" + b + "&dn=x#&xt=urn:btih:" + c,
		"magnet:?xt=urn:btih:" + b + "#xt=urn:btih:" + c,
	} {
		if h, conflict := ReleaseIdentity(Release{Magnet: NewLink(m)}); !conflict {
			t.Fatalf("%q: two identities accepted as %s", m, h)
		}
		if HashFromMagnet(m) != "" {
			t.Fatalf("%q: ambiguous magnet gave %s", m, HashFromMagnet(m))
		}
	}
	// Fail closed: a magnet present but without a v1 identity conflicts with any other identity.
	for _, m := range []string{"magnet:?dn=Show", "magnet:", "magnet:?xt=urn:btih:zz", "magnet:?xt=urn:btmh:1220" + a + a[:24]} {
		if h, conflict := ReleaseIdentity(Release{InfoHash: a, Magnet: NewLink(m)}); !conflict {
			t.Fatalf("%q: unidentified magnet accepted beside a reported hash as %s", m, h)
		}
		if h, conflict := ReleaseIdentity(Release{Link: NewLink(m), identityHashes: []string{a}}); !conflict {
			t.Fatalf("%q: unidentified magnet link accepted beside an adapter identity as %s", m, h)
		}
		if h, conflict := ReleaseIdentity(Release{Magnet: NewLink(m)}); conflict || h != "" {
			t.Fatalf("%q: magnet alone = %s %v, want hashless", m, h, conflict)
		}
	}
	// Accepted: the same identity in any spelling, and links that are not magnets.
	for _, r := range []Release{
		{InfoHash: a, Magnet: NewLink("magnet:?XT=urn:btih:" + strings.ToUpper(a))},
		{InfoHash: a, Magnet: NewLink("MAGNET:?xt=URN:BTIH:" + a + "&dn=Show\tS01&tr=udp://t:1")},
		{InfoHash: a, Magnet: NewLink(" magnet:?dn=A #1&xt=urn%3Abtih%3A" + a + "&xt=urn:btih:" + a)},
		{InfoHash: a, Link: NewLink("https://example.invalid/t.torrent?xt=urn:btih:" + b)},
		{InfoHash: a},
	} {
		if h, conflict := ReleaseIdentity(r); conflict || h != a {
			t.Fatalf("%+v: identity = %s %v, want %s", r, h, conflict, a)
		}
	}
	if HashFromMagnet("https://example.invalid/?XT=urn:btih:"+a) != "" {
		t.Fatal("non-magnet accepted")
	}
}

func TestTorznabMagnetIdentities(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for name, body := range map[string]string{
		"uppercase":    `<torznab:attr name="infohash" value="` + a + `"/><torznab:attr name="magneturl" value="magnet:?XT=urn:btih:` + b + `"/>`,
		"hash_mark":    `<torznab:attr name="infohash" value="` + a + `"/><torznab:attr name="magneturl" value="magnet:?dn=Show #1&amp;xt=urn:btih:` + b + `"/>`,
		"unidentified": `<torznab:attr name="infohash" value="` + a + `"/><torznab:attr name="magneturl" value="magnet:?dn=Show"/>`,
		"enclosure":    `<link>https://example.invalid/a.torrent</link><torznab:attr name="infohash" value="` + a + `"/><enclosure url="magnet:?dn=Show"/>`,
	} {
		t.Run(name, func(t *testing.T) {
			loadFixtures(t, "prowlarr_caps.xml")
			n := newNet(t)
			serveProwlarr(n, &recorder{}, "prowlarr_caps.xml", func(*http.Request) string {
				return `<?xml version="1.0"?><rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><item><title>Conflict.2160p</title><guid>one</guid>` + body + `</item></channel></rss>`
			})
			res := search(t, newHarness(t, n, testPolicy(), false), "prowlarr", Query{Type: QueryText, Title: "Conflict"})
			if len(res.Releases) != 0 {
				t.Fatalf("conflicting release kept: %+v", res.Releases)
			}
		})
	}
}
