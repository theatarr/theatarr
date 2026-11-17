package web

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/library"
)

// EditFileFacts answers as the library would for one of Alpha's files in movie-remux.
func (f *fakeOps) EditFileFacts(_ context.Context, linkID int64, e library.FileFactsEdit) (*library.FileFactsEdited, error) {
	return &library.FileFactsEdited{LinkID: linkID, TitleID: 1, Path: "/movies/Alpha (2020)/Alpha.mkv",
		Versions: []string{"movie-remux"}, Before: decide.ExistingFile{Quality: "Remux-2160p", Revision: 1},
		After:  decide.ExistingFile{Quality: e.Quality, Revision: e.Revision, Languages: e.Languages, ReleaseGroup: e.ReleaseGroup},
		Scores: []int{120}}, nil
}

// TestFileFactsAPI: managers read a file's stored facts with the form's choices and save corrected ones, recorded in
// the journal as edit_file; a quality or language the title kind lacks is refused; household accounts may do neither.
func TestFileFactsAPI(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	uid := o.account("household", auth.RoleUser, auth.StatusActive, true)
	uc, ucsrf := o.session(uid)
	var facts FileFacts
	if code := o.jsonCall("GET", APIBase+"/links/901/facts", nil, o.mc, "", &facts); code != 200 ||
		facts.Quality != "Remux-2160p" || facts.Revision != 1 || !slices.Contains(facts.QualityChoices, "Bluray-1080p") ||
		!slices.Contains(facts.LanguageChoices, "French") || !slices.Equal(facts.Languages, []string{"English"}) {
		t.Fatalf("facts: %d %+v", code, facts)
	}
	if code := o.jsonCall("GET", APIBase+"/links/901/facts", nil, uc, "", nil); code != http.StatusForbidden {
		t.Errorf("household read: %d", code)
	}
	body := FileFactsEdit{Quality: "bluray-1080p", Revision: 2, Languages: []string{"french"}, ReleaseGroup: " NEW "}
	if code := o.jsonCall("PATCH", APIBase+"/links/901/facts", body, uc, ucsrf, nil); code != http.StatusForbidden {
		t.Errorf("household edit: %d", code)
	}
	for _, bad := range []FileFactsEdit{{Quality: "Bluray-9000p", Revision: 1}, {Quality: "Bluray-1080p", Revision: 1,
		Languages: []string{"Klingon"}}, {Quality: "Bluray-1080p"}} {
		if code := o.jsonCall("PATCH", APIBase+"/links/901/facts", bad, o.mc, o.mcsrf, nil); code != http.StatusBadRequest {
			t.Errorf("%+v: %d", bad, code)
		}
	}
	var got FileFactsResult
	if code := o.jsonCall("PATCH", APIBase+"/links/901/facts", body, o.mc, o.mcsrf, &got); code != 200 ||
		got.Message != "Saved: Remux-2160p → Bluray-1080p Proper; languages — → French; release group — → NEW. Score: "+
			o.web.versionLabel("movie-remux")+" 120." || len(got.Scores) != 1 || got.Scores[0].Score != 120 {
		t.Fatalf("edit: %d %+v", code, got)
	}
	// A language manual import does not offer (Unknown) round-trips: offered in the form, accepted unchanged when
	// only the release group changes.
	o.exec(`UPDATE link SET file_metadata = '{"Score":3200,"Languages":["Unknown"]}' WHERE id = 901`)
	facts = FileFacts{}
	if code := o.jsonCall("GET", APIBase+"/links/901/facts", nil, o.mc, "", &facts); code != 200 ||
		!slices.Equal(facts.Languages, []string{"Unknown"}) || facts.LanguageChoices[0] != "Unknown" {
		t.Fatalf("unknown language facts: %d %+v", code, facts)
	}
	got = FileFactsResult{}
	if code := o.jsonCall("PATCH", APIBase+"/links/901/facts", FileFactsEdit{Quality: facts.Quality, Revision: facts.Revision,
		Languages: facts.Languages, ReleaseGroup: "ONLY"}, o.mc, o.mcsrf, &got); code != 200 ||
		!strings.Contains(got.Message, "languages — → Unknown") {
		t.Fatalf("unknown language kept: %d %+v", code, got)
	}
	if n := o.count(`SELECT COUNT(*) FROM journal_action WHERE action = 'edit_file' AND title_id = 1
		AND detail LIKE 'Remux-2160p → Bluray-1080p Proper;%' AND by <> ''`); n != 1 {
		t.Errorf("journal rows %d", n)
	}
}
