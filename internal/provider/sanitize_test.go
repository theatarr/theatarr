package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAllDebridAuthBlocked(t *testing.T) {
	f := newFakeNet(t)
	f.serve("ad.test", keyAD, func(w http.ResponseWriter, r *http.Request) {
		writeFixture(t, w, 200, "ad/error_blocked.json")
	})
	_, err := f.client("ad", keyAD).Account(context.Background())
	var ae *APIError
	if !errors.Is(err, ErrLocationBlocked) || !errors.Is(err, ErrAuth) || !errors.As(err, &ae) || ae.Code != "AUTH_BLOCKED" {
		t.Fatalf("got %v, want ErrLocationBlocked (an ErrAuth) with AUTH_BLOCKED", err)
	}
	want := "ad: HTTP 200 AUTH_BLOCKED: AllDebrid blocked this location; approve it from the email AllDebrid sent"
	if err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	if strings.Contains(err.Error()+ae.Message, "example.invalid") || strings.Contains(f.logs.String(), "example.invalid") {
		t.Error("the account's address leaked")
	}
}

// RD and TorBox error bodies get the same treatment, over HTTP errors and HTTP 200 envelopes.
func TestProviderErrorsSanitised(t *testing.T) {
	f := newFakeNet(t)
	f.serve("tb.test", keyTB, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"success":false,"error":"<i>BAD_REQUEST</i>","detail":"Contact <b>owner@example.invalid</b> ` +
			strings.Repeat("and more text ", 30) + `","data":null}`))
	})
	f.serve("rd.test", keyRD, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"account_locked <a@example.invalid>","error_code":14}`))
	})
	for _, k := range []string{"tb", "rd"} {
		key := map[string]string{"tb": keyTB, "rd": keyRD}[k]
		_, err := f.client(k, key).Account(context.Background())
		if err == nil {
			t.Fatalf("%s: no error", k)
		}
		msg := err.Error()
		if strings.Contains(msg, "example.invalid") || strings.ContainsAny(msg, "<>") || utf8.RuneCountInString(msg) > 200 {
			t.Errorf("%s: %q", k, msg)
		}
		t.Logf("%s: %s", k, msg)
	}
}

func TestCleanMessage(t *testing.T) {
	for in, want := range map[string]string{
		"An <b>email has been sent</b> to j.doe@example.invalid.": "An email has been sent to [email].",
		"&lt;b&gt;x&lt;/b&gt;  a\n\tb":                            "x a b",
		"mail <x@example.invalid> now":                            "mail now",
		"plain":                                                   "plain",
	} {
		if got := cleanMessage(in); got != want {
			t.Errorf("cleanMessage(%q) = %q, want %q", in, got, want)
		}
	}
	long := cleanMessage(strings.Repeat("é", 300))
	if n := utf8.RuneCountInString(long); n != maxMessage || !strings.HasSuffix(long, "…") {
		t.Errorf("long: %d runes", n)
	}
}

// Entity decoding and whitespace/tag normalization must happen before either field is truncated.
func TestSanitizeEncodedSecretBeforeTruncation(t *testing.T) {
	const key = "synthetic-credential-1234567890"
	encoded := strings.ReplaceAll(key, "e", "&#101;")
	redactor := NewRedactor(key)
	for _, tc := range []struct{ name, code, message string }{
		{"encoded code", encoded, "echo " + encoded},
		{"encoded boundary", "BAD", strings.Repeat("x", maxMessage-8) + encoded},
		{"raw boundary", "BAD", strings.Repeat("x", maxMessage-8) + key},
		{"tag split", "BAD", "echo " + strings.ReplaceAll(key, "credential", "cre<b></b>dential")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := redactor.Sanitize(tc.code, tc.message)
			if strings.Contains(code+msg, key) || strings.Contains(msg, key[:7]) || strings.Contains(code, "synthetic") {
				t.Fatalf("credential exposed: code %q message %q", code, msg)
			}
		})
	}
	// Raw redaction still catches a key whose own spelling would change during cleaning.
	raw := "raw-key-&amp;-123456"
	_, msg := NewRedactor(raw).Sanitize("BAD", raw)
	if msg != redactMask {
		t.Fatalf("raw credential: %q", msg)
	}
	// Exercise the actual provider error path, including code formatting in APIError.Error.
	f := newFakeNet(t)
	f.serve("ad.test", key, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": map[string]string{"code": encoded, "message": encoded}})
	})
	_, err := f.client("ad", key).Account(context.Background())
	if err == nil || strings.Contains(fmt.Sprint(err), key) {
		t.Fatalf("provider error: %v", err)
	}
}
