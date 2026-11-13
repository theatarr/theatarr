package web

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
)

// activeTestVersion creates test-version as an active copy of movie-web through the versions confirm flow.
func activeTestVersion(t *testing.T) (*settingsEnv, string) {
	t.Helper()
	area := t.TempDir()
	se := newQualityEnvBase(t, func(p *policy.Policy) { p.Library.Dangling.Roots = []string{area} })
	record := se.want(se.call("GET", "/versions/movie-web", nil), 200, "get")["record"].(map[string]any)
	config := record["config"].(map[string]any)
	root := filepath.Join(area, "test-version")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	config["library"], config["mode"] = root, "active"
	body := map[string]any{"generation": se.generation(), "key": "test-version", "config": config, "roots": []string{root}}
	proof := se.want(se.call("POST", "/versions/confirm", body), 200, "confirm")
	body["confirmation_token"] = proof["token"]
	se.want(se.call("POST", "/versions", body), 200, "active create")
	return se, root
}

// confirmRefusal checks a 422 names the effect and carries a token for it.
func confirmRefusal(t *testing.T, refusal map[string]any, contains string) string {
	t.Helper()
	if text := fmt.Sprint(refusal["effects"]); !strings.Contains(text, contains) {
		t.Fatalf("effects lack %q: %s", contains, text)
	}
	confirmation, _ := refusal["confirmation"].(map[string]any)
	token, _ := confirmation["token"].(string)
	if token == "" {
		t.Fatalf("422 without a confirmation token: %+v", refusal)
	}
	return token
}

// The version-delete token travels in a header, never the URL; the query form is refused.
func TestSettingsVersionDeleteTokenNotInURL(t *testing.T) {
	t.Parallel()
	se, root := activeTestVersion(t)
	for _, query := range []string{
		`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(4242,'movie','Fixture',0,0)`,
		`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(4242,4242,'test-version',0,0)`,
		fmt.Sprintf(`INSERT INTO link(id,path,target,title_id,created_at,updated_at) VALUES(4242,'%s/x.mkv','fixture-target',4242,0,0)`, root),
		`INSERT INTO link_version(link_id,version_id) VALUES(4242,4242)`,
	} {
		if _, err := se.st.Writer().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	path := fmt.Sprintf("/versions/test-version?generation=%d", se.generation())
	token := confirmRefusal(t, se.want(se.call("DELETE", path, nil), 422, "unconfirmed delete"),
		"is removed with its entries on every title (monitoring, wanted state, history). 1 linked files stay on disk")
	se.want(se.call("DELETE", path+"&confirmation_token="+token, nil), 400, "token in the URL")
	se.want(confirmedDelete(se, path, token), 200, "confirmed delete")
}

// confirmedDelete sends a DELETE with the confirmation token in its header.
func confirmedDelete(se *settingsEnv, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("DELETE", APIBase+"/settings"+path, nil)
	r.AddCookie(se.cookie)
	r.Header.Set(auth.CSRFHeader, se.csrf)
	r.Header.Set("X-Confirmation-Token", token)
	return se.do(r)
}

// Editing a profile a Live library uses saves at once: no 422, no token.
func TestSettingsLiveProfileEditSavesDirectly(t *testing.T) {
	t.Parallel()
	se, _ := activeTestVersion(t)
	profile := se.coord.Current().Policy.Versions["test-version"].Profile
	id := qualityID(t, se, "profiles", profile)
	path := fmt.Sprintf("/profiles/%d", id)
	record := se.want(se.call("GET", path, nil), 200, "profile")
	config := record["config"].(map[string]any)
	config["upgrades"] = false
	se.want(se.call("PUT", path, map[string]any{"generation": se.generation(), "key": profile, "config": config}), 200, "upgrades off")
	config["upgrades"] = true
	config["min_score"] = -10000
	se.want(se.call("PUT", path, map[string]any{"generation": se.generation(), "key": profile, "config": config}), 200, "upgrades on, min_score lowered")
}
