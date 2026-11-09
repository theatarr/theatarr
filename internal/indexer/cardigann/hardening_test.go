// Port of Mediarium internal/indexers/hardening_test.go @ f7d1e26 (github.com/rdborg/Mediarium, AGPL-3.0,
// Copyright (C) Ryan Borg and the Mediarium contributors). Changes are listed in the package doc.

package cardigann

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func hardeningSession(t *testing.T, base string) *cgSession {
	t.Helper()
	def := &Definition{ID: "x", Name: "X", Links: []string{base}}
	s, err := newSession(NewManager(nil, nil), def, Instance{Name: "X", BaseURL: base, Transport: direct})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTemplateRefusesRunawayConstructs(t *testing.T) {
	vars := newTmplVars()
	vars.Categories = []string{"2000", "2010", "5000"}
	vars.Keywords = "movie"
	cases := []struct {
		name    string
		src     string
		wantErr string // "" = must work
		want    string
	}{
		{"loop over categories", `{{ range .Categories }}c{{.}}=1&{{end}}`, "", "c2000=1&c2010=1&c5000=1&"},
		{"loop with variables", `{{ range $i, $e := .Categories }}{{ $e }};{{ end }}`, "", "2000;2010;5000;"},
		{"two loops nested", `{{ range .Categories }}{{ range $.Categories }}.{{ end }}{{ end }}`, "", "........."},
		{"loop over a number", `{{ range 1000000000 }}{{ end }}`, "number", ""},
		{"loop over a character", `{{ range 'a' }}{{ end }}`, "number", ""},
		{"loop over a number, no space", `{{ range(1000000000) }}{{ end }}`, "number", ""},
		{"three loops nested", `{{ range .Categories }}{{ range .Categories }}{{ range .Categories }}x{{ end }}{{ end }}{{ end }}`, "nested", ""},
		{"three loops nested, no spaces", `{{range.Categories}}{{range.Categories}}{{range.Categories}}x{{end}}{{end}}{{end}}`, "nested", ""},
		{"loops inside conditions still count", `{{ range .Categories }}{{ if .Keywords }}{{ range .Categories }}{{ range .Categories }}x{{ end }}{{ end }}{{ end }}{{ end }}`, "nested", ""},
		{"a template that calls itself", `{{ define "a" }}{{ template "a" }}{{ end }}{{ template "a" }}`, "not supported", ""},
		{"a block", `{{ block "a" . }}x{{ end }}`, "not supported", ""},
		{"too much text", `{{ range .Categories }}{{ range $.Categories }}{{ printf "%200000s" "x" }}{{ end }}{{ end }}`, "too much text", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			out, err := applyTemplate(tc.src, vars, nil)
			if time.Since(start) > 3*time.Second {
				t.Fatalf("took %v", time.Since(start))
			}
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.want != "" && out != tc.want {
					t.Fatalf("got %q, want %q", out, tc.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an error mentioning %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestFilterCannotGrowWithoutLimit(t *testing.T) {
	big := strings.Repeat("a", 20<<20)
	cases := []struct {
		name string
		f    FilterBlock
	}{
		{"replace", FilterBlock{Name: "replace", Args: []any{"a", strings.Repeat("b", 1000)}}},
		{"re_replace", FilterBlock{Name: "re_replace", Args: []any{".", strings.Repeat("b", 1000)}}},
		{"re_replace with group", FilterBlock{Name: "re_replace", Args: []any{"(a*)", "$1$1$1$1$1$1"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			out, err := applyFilters(big, []FilterBlock{tc.f}, nil)
			if err == nil || len(out) != 0 {
				t.Fatalf("a %d MB value was allowed to grow: err=%v len=%d", len(big)>>20, err, len(out))
			}
			if time.Since(start) > 3*time.Second {
				t.Fatalf("took %v", time.Since(start))
			}
		})
	}
	// ordinary use is untouched
	got, err := applyFilters("a-b-c", []FilterBlock{{Name: "replace", Args: []any{"-", "--"}}, {Name: "re_replace", Args: []any{"(b)", "[$1]"}}}, nil)
	if err != nil || got != "a--[b]--c" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestSplitAndTrimTakeWholeCharacters(t *testing.T) {
	t.Parallel()
	got, err := applyFilter("a·b·c", FilterBlock{Name: "split", Args: []any{"·", "1"}}, nil)
	if err != nil || got != "b" {
		t.Errorf("split on a multi-byte separator gave %q, %v", got, err)
	}
	got, err = applyFilter("··a··", FilterBlock{Name: "trim", Args: []any{"·"}}, nil)
	if err != nil || got != "a" {
		t.Errorf("trim of a multi-byte character gave %q, %v", got, err)
	}
}

func TestParseSizeAndCountStayInRange(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"99999999999999999999999 PB", strings.Repeat("9", 300) + " GB", "1e999 TB", "9223372036854775807 PB", "18446744073709551615"} {
		if n := parseSize(s); n < 0 {
			t.Errorf("parseSize(%q) = %d", s, n)
		}
		if n := parseCount(s); n < 0 {
			t.Errorf("parseCount(%q) = %d", s, n)
		}
	}
	if got := parseSize("1.5 GB"); got != 1610612736 {
		t.Errorf("parseSize(1.5 GB) = %d", got)
	}
}

func TestResolvePathStaysOnTheSite(t *testing.T) {
	t.Parallel()
	s := hardeningSession(t, "https://www.example.org/")
	s.def.Login = &LoginBlock{} // a site that signs in keeps to its own addresses
	cases := []struct {
		path string
		ok   bool
	}{
		{"/search?q=a", true},
		{"search/a b", true},
		{"https://www.example.org/x", true},
		{"https://api.example.org/x", true},
		{"HTTPS://API.EXAMPLE.ORG/x", true},
		{"//api.example.org/x", true},
		{"https://www.example.org./x", true}, // a trailing dot is the same host
		{"https://WWW.EXAMPLE.ORG:8443/x", true},
		{"https://www.example。org/x", false}, // an ideographic full stop is not a dot here
		{"https://xn--www-example-9d0b.org/x", false},
		{"//evil.example.net/x", false},
		{"HTTPS://evil.example.net/x", false},
		{"Http://evil.example.net", false},
		{"https://evil.example.net/x", false},
		{"https:evil.example.net", false},
		{"https://www.example.org.evil.net/", false},
		{"https://www.example.org@evil.net/", false},
		{"https://evil.net\\@www.example.org/", false},
		{"javascript:alert(1)", false},
		{"file:///etc/passwd", false},
		{"ftp://www.example.org/x", false},
		{"magnet:?xt=urn:btih:abc", false},
		{"http://127.0.0.1:8080/x", false},
		{"http://169.254.169.254/latest/meta-data/", false},
	}
	for _, tc := range cases {
		got, err := s.resolvePath(tc.path)
		if tc.ok && err != nil {
			t.Errorf("resolvePath(%q) refused: %v", tc.path, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("resolvePath(%q) = %q, want a refusal", tc.path, got)
		}
	}
}

// A login page whose form sends the password to another site must not be
// obeyed.
func TestLoginFormCannotSendCredentialsElsewhere(t *testing.T) {
	t.Parallel()
	var stolen string
	thief := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stolen = string(body)
	}))
	t.Cleanup(thief.Close)
	// same machine, different name: 127.0.0.1 is the site, localhost the thief
	thiefURL := strings.Replace(thief.URL, "127.0.0.1", "localhost", 1)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body><form id="f" method="post" action="%s/collect"><input name="username"><input name="password" type="password"></form></body></html>`, thiefURL)
	}))
	t.Cleanup(site.Close)

	def := &Definition{
		ID: "phish", Name: "Phish", Links: []string{site.URL + "/"},
		Login:  &LoginBlock{Path: "login", Method: "form", Form: "form#f", Inputs: OrderedMap[string]{Keys: []string{"password"}, Values: map[string]string{"password": "hunter2"}}},
		Search: SearchBlock{Paths: []SearchPath{{Path: "s"}}},
	}
	m := NewManager(nil, nil)
	m.DefaultDelay = 0
	s, err := newSession(m, def, Instance{Name: "Phish", BaseURL: site.URL + "/", Transport: direct})
	if err != nil {
		t.Fatal(err)
	}
	err = s.login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if stolen != "" {
		t.Fatalf("the password reached the other site: %q", stolen)
	}
}

func TestRequestDelayIsLimited(t *testing.T) {
	m := NewManager(nil, nil)
	m.DefaultDelay = 0
	for _, delay := range []float64{1e300, -5} {
		s := &cgSession{m: m, def: &Definition{RequestDelay: delay}, lastRequest: time.Now()}
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		err := s.wait(ctx)
		cancel()
		if delay < 0 && (err != nil || time.Since(start) > 200*time.Millisecond) {
			t.Errorf("a negative delay waited: %v after %v", err, time.Since(start))
		}
		if delay > 0 && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("a huge delay should wait (up to the limit), got %v", err)
		}
	}
}

func TestSelectorGivesUpOnAHostilePage(t *testing.T) {
	page := "<html><body>" + strings.Repeat("<i></i>", 150000) + "</body></html>"
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	sel, err := CompileSelector("i:nth-child(2n+1)")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	found := sel.AllWith(doc, &workBudget{left: 5_000_000})
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if len(found) >= 150000/2 {
		t.Fatalf("the whole page was read: %d matches", len(found))
	}
	// a sane page is read in full
	small, _ := html.Parse(strings.NewReader("<ul><li>a<li>b<li>c<li>d</ul>"))
	if n := len(sel.AllWith(small, newWorkBudget())); n != 0 {
		t.Errorf("unexpected matches %d", n)
	}
	odd, _ := CompileSelector("li:nth-child(odd)")
	if n := len(odd.AllWith(small, newWorkBudget())); n != 2 {
		t.Errorf("li:nth-child(odd) matched %d, want 2", n)
	}
}

func TestSelectorLongChainOnDeepPage(t *testing.T) {
	page := strings.Repeat("<div>", 500) + "<b>x</b>" + strings.Repeat("</div>", 500)
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sel, err := CompileSelector("div div div div div div div div div span")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	sel.All(doc)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestXMLReplyNestedTooDeep(t *testing.T) {
	t.Parallel()
	if _, err := parseXMLDoc([]byte(strings.Repeat("<a>", 100000))); err == nil {
		t.Fatal("a reply nested 100000 deep was accepted")
	}
	doc, err := parseXMLDoc([]byte("<rss><channel><item><title>x</title></item></channel></rss>"))
	if err != nil || textContent(doc) != "x" {
		t.Fatalf("plain feed: %v", err)
	}
}

func TestFinishDropsAbsurdDatesAndTitles(t *testing.T) {
	t.Parallel()
	s := hardeningSession(t, "https://www.example.org/")
	rb := &releaseBuilder{title: strings.Repeat("é", 2000), link: "https://www.example.org/d/1", date: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)}
	res, err := s.finish(rb)
	if err != nil {
		t.Fatal(err)
	}
	if !res.PublishDate.IsZero() {
		t.Errorf("date %v kept", res.PublishDate)
	}
	if len(res.Title) > maxTitleBytes || !strings.HasPrefix(res.Title, "é") {
		t.Errorf("title of %d bytes", len(res.Title))
	}
	rb = &releaseBuilder{title: "ok", link: "https://www.example.org/d/1", date: time.Now().Add(-time.Hour)}
	if res, _ = s.finish(rb); res.PublishDate.IsZero() {
		t.Error("a normal date was dropped")
	}
}

func TestBadAddressErrorsDoNotShowTheKey(t *testing.T) {
	t.Parallel()
	const key = "KEYKEYKEY123456"
	// a definition that builds an address with a bad escape while the person
	// keys are in the query
	s := hardeningSession(t, "https://www.example.org/")
	_, err := s.doOnce(context.Background(), cgRequest{url: "https://www.example.org/%zz?passkey=" + key})
	if err == nil || strings.Contains(err.Error(), key) {
		t.Errorf("cardigann: %v", err)
	}
}

func TestSiteCategoriesComeInTheSameOrderEveryTime(t *testing.T) {
	t.Parallel()
	def := &Definition{Caps: CapsBlock{Categories: map[string]string{}}}
	for i := 0; i < 30; i++ {
		def.Caps.Categories[fmt.Sprintf("c%02d", i)] = "Movies/HD"
	}
	want := strings.Join(def.siteCategories([]int{2000}), ",")
	if strings.Count(want, ",") != 29 {
		t.Fatalf("categories = %q", want)
	}
	for i := 0; i < 50; i++ {
		if got := strings.Join(def.siteCategories([]int{2000}), ","); got != want {
			t.Fatalf("order changed: %q then %q", want, got)
		}
	}
}

func TestLonePageFieldReadIsBounded(t *testing.T) {
	doc, err := html.Parse(strings.NewReader("<html><body>" + strings.Repeat("<i></i>", 120000) + "</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, found, _ := htmlRow{n: doc}.value(SelectorField{Selector: "i:nth-child(120000)"}, newTmplVars())
	if found {
		t.Error("the last sibling was found within the budget")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestBadLinkErrorsDoNotShowThePasskey(t *testing.T) {
	t.Parallel()
	s := hardeningSession(t, "https://www.example.org/")
	for _, path := range []string{"se%zzarch?passkey=SECRETPASS", "/a%zz/b?x=1&passkey=SECRETPASS", "https://www.example.org/%zz?passkey=SECRETPASS"} {
		_, err := s.resolvePath(path)
		if err == nil || strings.Contains(err.Error(), "SECRETPASS") {
			t.Errorf("resolvePath(%q): %v", path, err)
		}
	}
}

func TestHugeRegularExpressionIsRefused(t *testing.T) {
	huge := strings.Repeat(`\pL{1000}`, 12) + "x"
	start := time.Now()
	if _, err := compileDotNetRegex(huge); err == nil || !strings.Contains(err.Error(), "too complex") {
		t.Errorf("a 12000-step pattern was accepted: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	for _, ok := range []string{`\d+`, `(?i)(?:1080p|720p|2160p|4k)[. ]?(?:web|bluray)`, `^(\d{4})-(\d{2})-(\d{2})$`, strings.Repeat(`[a-z]{20}`, 20)} {
		if _, err := compileDotNetRegex(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

// An RSS feed has <link>text</link>. Read with the HTML rules <link> is a
// void element, so the closing tag was an error (nekoBT's feed).
func TestParseXMLDocKeepsRSSLinkElements(t *testing.T) {
	t.Parallel()
	feed := `<?xml version="1.0"?>
<rss version="1.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
  <channel>
    <title>Feed</title>
    <item>
      <title>A release</title>
      <link>magnet:?xt=urn:btih:abc&amp;dn=A%20release</link>
      <torznab:attr name="seeders" value="10"/>
    </item>
  </channel>
</rss>`
	doc, err := parseXMLDoc([]byte(feed))
	if err != nil {
		t.Fatal(err)
	}
	var link string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "link" && n.FirstChild != nil {
			link = n.FirstChild.Data
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if link != "magnet:?xt=urn:btih:abc&dn=A%20release" {
		t.Errorf("link = %q", link)
	}
}

// A definition that cleans a title in several languages can have dozens of
// filters on one field (Mazepa has 38): that is fine, a runaway list is not.
func TestFilterListLimit(t *testing.T) {
	t.Parallel()
	list := func(n int) []FilterBlock {
		fs := make([]FilterBlock, n)
		for i := range fs {
			fs[i] = FilterBlock{Name: "trim"}
		}
		return fs
	}
	v := &defValidator{def: &Definition{}}
	v.filters("field title", list(40))
	if len(v.problems) != 0 {
		t.Errorf("40 filters were refused: %v", v.problems)
	}
	v = &defValidator{def: &Definition{}}
	v.filters("field title", list(maxFilters+1))
	if len(v.problems) == 0 {
		t.Error("a list longer than the limit was accepted")
	}
}
