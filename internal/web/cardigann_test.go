package web

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
)

type fakeCatalog struct {
	approved []string
	refreshs int
}

func (f *fakeCatalog) Catalog() ([]cardigann.Summary, error) {
	return []cardigann.Summary{
		{ID: "alpha", Name: "Alpha", Privacy: "public", Status: cardigann.StatusRuns, Language: "en-US"},
		{ID: "beta", Name: "Beta Tracker", Privacy: "private", Status: cardigann.StatusNeedsAccount, Language: "fr-FR"},
	}, nil
}

func (f *fakeCatalog) Summary(id string) (cardigann.Summary, bool) {
	if id == "alpha" {
		return cardigann.Summary{ID: "alpha"}, true
	}
	return cardigann.Summary{}, false
}

func (f *fakeCatalog) Status() cardigann.Status {
	return cardigann.Status{Commit: "abc", Definitions: 2}
}
func (f *fakeCatalog) Holds() []cardigann.Hold {
	return []cardigann.Hold{{ID: "beta", Reasons: []string{cardigann.HoldNewRequiredSettings}}}
}

func (f *fakeCatalog) Approve(id string) error {
	if id != "beta" {
		return cardigann.ErrNoHold
	}
	f.approved = append(f.approved, id)
	return nil
}

func (f *fakeCatalog) Refresh(context.Context) error { f.refreshs++; return errors.New("offline") }
func (f *fakeCatalog) ProwlarrProposals(context.Context) ([]cardigann.Proposal, error) {
	return []cardigann.Proposal{{Status: cardigann.ProposalProposed, Key: "alpha"}}, nil
}

func TestCardigannAPI(t *testing.T) {
	t.Parallel()
	cat := &fakeCatalog{}
	e := newEnvWith(t, func(c *Config) { c.Cardigann = cat })
	key := e.apiKey()
	var list CardigannList
	if code := e.getKey(APIBase+"/cardigann/definitions?q=tracker", key, &list); code != 200 || list.Total != 2 ||
		len(list.Definitions) != 1 || list.Definitions[0].ID != "beta" || list.Status.Commit != "abc" {
		t.Fatalf("search: %d %+v", code, list)
	}
	if code := e.getKey(APIBase+"/cardigann/definitions?privacy=public&language=en", key, &list); code != 200 ||
		len(list.Definitions) != 1 || list.Definitions[0].ID != "alpha" {
		t.Fatalf("filter: %d %+v", code, list)
	}
	if code := e.getKey(APIBase+"/cardigann/definitions/nope", key, nil); code != 404 {
		t.Errorf("unknown definition: %d", code)
	}
	var holds []cardigann.Hold
	if code := e.getKey(APIBase+"/cardigann/holds", key, &holds); code != 200 || len(holds) != 1 {
		t.Errorf("holds: %d %+v", code, holds)
	}
	post := func(path string) int {
		r := httptest.NewRequest("POST", path, nil)
		r.Header.Set(auth.APIKeyHeader, key)
		return e.do(r).Code
	}
	if code := post(APIBase + "/cardigann/holds/beta/approve"); code != 200 || len(cat.approved) != 1 {
		t.Errorf("approve: %d %v", code, cat.approved)
	}
	if code := post(APIBase + "/cardigann/holds/alpha/approve"); code != 404 {
		t.Errorf("approve without a hold: %d", code)
	}
	if code := post(APIBase + "/cardigann/refresh"); code != 502 || cat.refreshs != 1 {
		t.Errorf("refresh: %d", code)
	}
	var props []cardigann.Proposal
	if code := e.getKey(APIBase+"/cardigann/prowlarr", key, &props); code != 200 || len(props) != 1 {
		t.Errorf("prowlarr: %d %+v", code, props)
	}

	off := newEnv(t)
	offKey := off.apiKey()
	var empty []cardigann.Hold
	if code := off.getKey(APIBase+"/cardigann/holds", offKey, &empty); code != 200 || empty == nil || len(empty) != 0 {
		t.Fatalf("disabled catalog holds: %d %+v", code, empty)
	}
	// The catalog off is a state, not an error: an empty list with the reason and the way to turn it on.
	var offList CardigannList
	if code := off.getKey(APIBase+"/cardigann/definitions", offKey, &offList); code != 200 || offList.Definitions == nil ||
		len(offList.Definitions) != 0 || !strings.Contains(offList.Unavailable, "Settings › Indexers › Catalog") {
		t.Errorf("catalog off: %d %+v", code, offList)
	}
}
