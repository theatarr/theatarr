package web

import (
	"context"
	"net/http"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/store"
)

// UndoOffers offers Undo for replacement 5 only; UndoUpgrade undoes 5 and refuses 6 as the library would.
func (f *fakeOps) UndoOffers(_ context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	for _, id := range ids {
		if id == 5 {
			out[id] = library.UndoWords("Alpha.2020.2160p.WEB-GRP")
		}
	}
	return out, nil
}

func (f *fakeOps) UndoUpgrade(_ context.Context, id int64, _ string) (*library.Undone, error) {
	if id == 6 {
		return nil, &library.ReturnRefused{Sentence: "The library changed since; nothing was undone"}
	}
	return &library.Undone{TitleID: 1, Version: "movie-remux", Release: "Alpha.2020.2160p.WEB-GRP"}, nil
}

// History offers Undo on a manager's replaced files, says "Upgrade undone" for an undone row and "Replaced" for gone
// files replaced by a lower quality; the POST is a manager's, and a refusal answers 409 with its sentence.
func TestHistoryUndo(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	now := store.Millis(testNow)
	o.exec(`INSERT INTO history (ref, at, event, kind, title_id, version_key, release, quality, files, replaced, detail)
		VALUES ('lib:5', ?, 'deleted', 'link', 1, 'movie-remux', 'Alpha.2020.2160p.WEB-GRP', 'WEBDL-2160p', 1,
			json_array(json_object('id', 5, 'path', '/movies/Alpha/old.mkv', 'release', 'Alpha.old', 'episodes', json('[]'))), ''),
		('lib:6', ?, 'deleted', 'link', 1, 'movie-remux', 'Alpha.2020.720p.WEB-GRP', 'WEBDL-720p', 1,
			json_array(json_object('id', 6, 'path', '/movies/Alpha/gone.mkv', 'release', 'Alpha.gone', 'episodes', json('[]'),
				'dead_lower', json('true'))), ''),
		('lib:7', ?, 'undone', 'link', 1, 'movie-remux', 'Alpha.2020.2160p.WEB-GRP', 'WEBDL-2160p', 1, NULL,
			'upgrade undone by mgr: moved aside to /trash/x.mkv')`, now, now+1, now+2)
	var page ActivityPage
	if code := o.jsonCall("GET", APIBase+"/activity?state=history", nil, o.mc, "", &page); code != 200 {
		t.Fatalf("history: %d", code)
	}
	byRef := map[string]ActivityEvent{}
	for _, x := range page.History {
		byRef[x.ID] = x
	}
	if x := byRef["lib:5"]; x.Words != "Upgraded" || len(x.Replaced) != 1 ||
		x.Replaced[0].Undo != "Undo upgrade: puts the earlier file back and blocks Alpha.2020.2160p.WEB-GRP in this library" {
		t.Errorf("upgrade row %+v", x)
	}
	if x := byRef["lib:6"]; x.Words != "Replaced" || len(x.Replaced) != 1 || x.Replaced[0].Undo != "" {
		t.Errorf("gone file's row %+v", x)
	}
	if x := byRef["lib:7"]; x.Words != "Upgrade undone" || x.By != "mgr" {
		t.Errorf("undone row %+v", x)
	}
	var res UndoResult
	if code := o.jsonCall("POST", APIBase+"/activity/replaced/5/undo", nil, o.mc, o.mcsrf, &res); code != 200 ||
		res.Message == "" {
		t.Errorf("undo: %d %+v", code, res)
	}
	var e apiError
	if code := o.jsonCall("POST", APIBase+"/activity/replaced/6/undo", nil, o.mc, o.mcsrf, &e); code != http.StatusConflict ||
		e.Error != "The library changed since; nothing was undone" {
		t.Errorf("refused undo: %d %+v", code, e)
	}
	uid := o.account("household", auth.RoleUser, auth.StatusActive, true)
	uc, ucsrf := o.session(uid)
	if code := o.jsonCall("POST", APIBase+"/activity/replaced/5/undo", nil, uc, ucsrf, nil); code != http.StatusForbidden {
		t.Errorf("household undo: %d", code)
	}
}
