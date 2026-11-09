package indexer

import (
	"encoding/base32"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

func TestMagnetIdentities(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	bytes, _ := hex.DecodeString(b)
	b32 := base32.StdEncoding.EncodeToString(bytes)
	for name, magnet := range map[string]string{
		"encoded":  "magnet:?xt=urn%3Abtih%3A" + b,
		"multiple": "magnet:?xt=urn:btih:" + a + "&xt=urn:btih:" + b,
		"base32":   "magnet:?xt=urn%3Abtih%3A" + b32,
	} {
		t.Run(name, func(t *testing.T) {
			if _, conflict := ReleaseIdentity(Release{InfoHash: a, Magnet: NewLink(magnet)}); !conflict {
				t.Fatal("conflicting identity accepted")
			}
		})
	}
	for _, m := range []string{"magnet:?xt=urn%3Abtih%3A" + b32, "magnet:?xt=urn:btih:" + b + "&xt=urn:btih:" + b32} {
		if hash, conflict := ReleaseIdentity(Release{Magnet: NewLink(m)}); conflict || hash != b {
			t.Fatalf("normalization: %s %v", hash, conflict)
		}
	}
	if HashFromMagnet("https://example.invalid/?xt=urn:btih:"+a) != "" {
		t.Fatal("non-magnet accepted")
	}
}

func TestSearchKeepsAllIdentities(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for name, body := range map[string]string{
		"encoded":        `<torznab:attr name="infohash" value="` + a + `"/><torznab:attr name="magneturl" value="magnet:?xt=urn%3Abtih%3A` + b + `"/>`,
		"link":           `<link>magnet:?xt=urn:btih:` + a + `</link><torznab:attr name="infohash" value="` + a + `"/><torznab:attr name="magneturl" value="magnet:?xt=urn:btih:` + b + `"/>`,
		"enclosure":      `<link>magnet:?xt=urn:btih:` + a + `</link><enclosure url="magnet:?xt=urn:btih:` + b + `"/>`,
		"duplicate":      `<torznab:attr name="magneturl" value="magnet:?xt=urn:btih:` + a + `"/><torznab:attr name="magneturl" value="magnet:?xt=urn:btih:` + b + `"/>`,
		"duplicate_hash": `<torznab:attr name="infohash" value="` + a + `"/><torznab:attr name="infohash" value="` + b + `"/>`,
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
