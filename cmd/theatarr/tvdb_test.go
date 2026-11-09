package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestNewTVDBClient(t *testing.T) {
	t.Setenv("THEATARR_TEST_TVDB_KEY", "")
	pol := &policy.Policy{Metadata: &policy.Metadata{Sources: map[string]*policy.MetadataSource{
		"tvdb": {URL: "https://api4.thetvdb.com/v4", APIKeyEnv: "THEATARR_TEST_TVDB_KEY"}}}}
	if c, err := newTVDBClient(pol, nil, nil); c != nil || err != nil {
		t.Fatalf("no key: %v %v", c, err)
	}
	if c, err := newTVDBClient(&policy.Policy{}, nil, nil); c != nil || err != nil {
		t.Fatalf("no source: %v %v", c, err)
	}
}

func TestPrintTVDBEpisodes(t *testing.T) {
	t.Parallel()
	n, b, e, a := "Recap", 2, 1, "2020-12-20"
	var buf bytes.Buffer
	printTVDBEpisodes(&buf, &tvdb.SeriesBase{ID: 1, Name: "X", Year: "2020", Status: tvdb.Status{Name: "Ended"}},
		[]tvdb.Episode{{ID: 7, Number: 1, Name: &n, Aired: &a, AirsBeforeSeason: &b, AirsBeforeEpisode: &e}})
	if !strings.Contains(buf.String(), "S00E01") || !strings.Contains(buf.String(), "before S02E01") {
		t.Errorf("%s", buf.String())
	}
}
