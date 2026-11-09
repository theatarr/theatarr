package sonarr

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// TestRecordedSamples checks the JSON byte for byte, field order included, against parsedEpisodeInfo answers captured
// from Sonarr 4.0.20.3014 (GET /api/v3/parse) in testdata/samples.json.
func TestRecordedSamples(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/samples.json")
	if err != nil {
		t.Fatal(err)
	}
	var samples []struct {
		Instance          string          `json:"instance"`
		Title             string          `json:"title"`
		ParsedEpisodeInfo json.RawMessage `json:"parsedEpisodeInfo"`
	}
	if err := json.Unmarshal(b, &samples); err != nil {
		t.Fatal(err)
	}
	for _, s := range samples {
		var want bytes.Buffer
		if err := json.Compact(&want, s.ParsedEpisodeInfo); err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		enc := json.NewEncoder(&got)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(ParseTitle(s.Title)); err != nil {
			t.Fatal(err)
		}
		if g := bytes.TrimSpace(got.Bytes()); !bytes.Equal(g, want.Bytes()) {
			t.Errorf("%s %q:\n got  %s\n want %s", s.Instance, s.Title, g, want.Bytes())
		}
	}
}

// TestExpectationOps makes sure the case matcher rejects wrong answers, so a 100% rate means something.
func TestExpectationOps(t *testing.T) {
	t.Parallel()
	title := "Mad.Men.S05E01.720p.HDTV.x264-ORENJI"
	good := []testCase{
		{Expect: map[string]any{"seriesTitle": "Mad Men", "episodeNumbers.0": 1.0, "episodeNumbers.#": 1.0, "airDate": nil}},
		{Expect: map[string]any{"languages.#.name": []any{"Unknown"}, "quality.quality.name": "HDTV-720p"}},
		{Contains: map[string][]any{"languages.#.name": {"Unknown"}}},
		{Not: map[string]any{"": nil, "seasonNumber": 4.0}},
	}
	bad := []testCase{
		{Expect: map[string]any{"seriesTitle": "Mad Men 2"}},
		{Expect: map[string]any{"episodeNumbers": []any{}}},
		{Expect: map[string]any{"episodeNumbers.#": 2.0}},
		{Expect: map[string]any{"releaseGroup": nil}},
		{Expect: map[string]any{"missing.field": 0.0}},
		{Contains: map[string][]any{"languages.#.name": {"English"}}},
		{Not: map[string]any{"seasonNumber": 5.0}},
		{Compare: "cleanSeriesTitle", Expect: map[string]any{"": "Mad Men"}},
	}
	for i, c := range good {
		c.Call, c.Input = "parseTitle", &title
		if err := runCase(c); err != nil {
			t.Errorf("good %d: %v", i, err)
		}
	}
	for i, c := range bad {
		c.Call, c.Input = "parseTitle", &title
		if runCase(c) == nil {
			t.Errorf("bad %d passed, want failure", i)
		}
	}
}
