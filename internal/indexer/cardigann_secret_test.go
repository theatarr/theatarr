package indexer

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// An unexpected reply is reported without the site's body, which can hold a credential, and a site error message
// that quotes a credential is masked before it is returned or logged.
func TestCardigannErrorsCarryNoSecrets(t *testing.T) {
	const pw = "pw-secret-7731"
	n := newNet(t)
	n.serve("site.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"error":"invalid passkey ` + pw + `","hint":"sensitive body text"}`))
	}))
	h := cardigannHarness(t, n, "public", nil, map[string]string{"site": `{"username":"alice","password":"` + pw + `"}`},
		false)
	_, err := h.svc.Search(context.Background(), "site", Query{Type: QueryText, Title: "Show"})
	if err == nil {
		t.Fatal("no error for a reply without results")
	}
	if strings.Contains(err.Error(), pw) || strings.Contains(err.Error(), "sensitive body text") {
		t.Errorf("error carries the reply: %v", err)
	}
}
