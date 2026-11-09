package web

import (
	"context"
	"errors"
	"testing"
)

func TestNotifyTargetTest(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := e.login()
	var me Me
	e.jsonCall("GET", APIBase+"/auth/me", nil, c, "", &me)
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	path := APIBase + "/settings/notify/targets/chan/test"
	if code := e.jsonCall("POST", path, nil, c, me.CSRFToken, &out); code != 200 || out.OK || out.Error != "The notifier is not running." {
		t.Errorf("no notifier: %d %+v", code, out)
	}
	var got []string
	e.web.notifyTest = func(_ context.Context, target string) error {
		got = append(got, target)
		if len(got) > 1 {
			return errors.New("status 404")
		}
		return nil
	}
	out.Error = ""
	if code := e.jsonCall("POST", path, nil, c, me.CSRFToken, &out); code != 200 || !out.OK || out.Error != "" {
		t.Errorf("sent: %d %+v", code, out)
	}
	if code := e.jsonCall("POST", path, nil, c, me.CSRFToken, &out); code != 200 || out.OK || out.Error != "Not sent: status 404" {
		t.Errorf("failed: %d %+v", code, out)
	}
	if len(got) != 2 || got[0] != "chan" {
		t.Errorf("targets %v", got)
	}
}
