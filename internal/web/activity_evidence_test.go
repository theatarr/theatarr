package web

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/journal"
)

func TestActivityAttemptPreviewBoundedAndInspectComplete(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	for id := 1; id <= 3; id++ {
		e.exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,state,created_at,updated_at) VALUES(?,1,1,'movie-web',?,'Fixture','dmm','queued',0,0)`, id, strings.Repeat(fmt.Sprint(id), 40))
	}
	for i := 0; i < 1000; i++ {
		e.exec(`INSERT INTO download_attempt(download_id,provider,outcome,message,attempted_at) VALUES(1,'rd','deferred',?,?)`, fmt.Sprintf("attempt %04d", i), 1000-i)
	}
	for i := 0; i < 3; i++ {
		e.exec(`INSERT INTO download_attempt(download_id,provider,outcome,message,attempted_at) VALUES(2,'tb','error','https://example.invalid/?apikey=invented',0)`)
	}
	var page struct {
		Now []struct {
			ID       int64             `json:"id"`
			Attempts []journal.Attempt `json:"attempts"`
			More     bool              `json:"attempts_more"`
		} `json:"now"`
	}
	if code := e.getKey(APIBase+"/activity", key, &page); code != 200 {
		t.Fatalf("activity: %d", code)
	}
	for _, d := range page.Now {
		switch d.ID {
		case 1:
			if len(d.Attempts) != 5 || !d.More {
				t.Errorf("preview: %d attempts, more=%t", len(d.Attempts), d.More)
				break
			}
			if d.Attempts[0].Message != "attempt 0995" || d.Attempts[4].Message != "attempt 0999" {
				t.Errorf("preview order: %+v", d.Attempts)
			}
		case 2:
			if len(d.Attempts) != 3 || d.More {
				t.Errorf("small preview: %+v", d)
			}
		case 3:
			if len(d.Attempts) != 0 || d.More {
				t.Errorf("empty preview: %+v", d)
			}
		}
	}
	var full struct {
		Attempts []journal.Attempt `json:"attempts"`
	}
	if code := e.getKey(APIBase+"/activity/downloads/1/attempts", key, &full); code != 200 {
		t.Fatalf("inspect: %d", code)
	}
	if len(full.Attempts) != 1000 || full.Attempts[0].Message != "attempt 0000" || full.Attempts[999].Message != "attempt 0999" {
		t.Fatal("inspect lost full attempt history")
	}
	if code := e.getKey(APIBase+"/activity/downloads/2/attempts", key, &full); code != 200 || len(full.Attempts) != 3 {
		t.Fatalf("redacted detail: %d %+v", code, full)
	}
	for _, a := range full.Attempts {
		if strings.Contains(a.Message, "invented") {
			t.Fatal("inspect leaked credentials")
		}
	}
	for path, want := range map[string]int{"0": 400, "bad": 400, "9999": 404} {
		if code := e.getKey(APIBase+"/activity/downloads/"+path+"/attempts", key, nil); code != want {
			t.Errorf("id %s: %d", path, code)
		}
	}
	for role, want := range map[string]int{auth.RoleUser: 403, auth.RoleManager: 200} {
		uid := e.account("attempts-"+role, role, auth.StatusActive, false)
		k, _, err := e.auth.CreateKey(context.Background(), "attempts-"+role, uid)
		if err != nil {
			t.Fatal(err)
		}
		if code := e.getKey(APIBase+"/activity/downloads/1/attempts", k, nil); code != want {
			t.Errorf("%s: %d", role, code)
		}
	}
	if code := e.getKey(APIBase+"/activity/downloads/1/attempts?as=user", key, nil); code != 403 {
		t.Errorf("as=user: %d", code)
	}
}

// A client-routed download names its client (add_via: client) where a provider download names its provider.
func TestActivityNamesTheDownloadClient(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	e.exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,client,state,progress,created_at,updated_at) VALUES(1,1,1,'movie-web',?,'Fixture','dmm','dc','downloading',40,0,0)`, strings.Repeat("a", 40))
	var page struct {
		Now []struct {
			Provider string `json:"provider"`
			Client   string `json:"client"`
			Detail   string `json:"detail"`
		} `json:"now"`
	}
	if code := e.getKey(APIBase+"/activity", key, &page); code != 200 || len(page.Now) != 1 {
		t.Fatalf("activity: %d %+v", code, page)
	}
	if d := page.Now[0]; d.Client != "dc" || d.Provider != "" || d.Detail != "Downloading on client dc (40%)" {
		t.Errorf("download %+v", d)
	}
}
