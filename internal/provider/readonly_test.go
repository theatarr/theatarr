package provider

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// This package is read-only towards providers: debrid torrents are shared by every library, so nothing here may add, select,
// delete, restart or unrestrict. These tests keep it that way.

// No HTTP method but GET appears in the package's code.
func TestOnlyGET(t *testing.T) {
	bad := regexp.MustCompile(`http\.Method(Post|Put|Patch|Delete|Head|Options|Connect|Trace)|"(POST|PUT|PATCH|DELETE)"`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if bad.MatchString(line) {
				t.Errorf("%s:%d: a write method: %s", n, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// The Provider interface and every client have no write-like method.
func TestNoWriteMethods(t *testing.T) {
	writey := regexp.MustCompile(`(?i)add|select|delete|remove|unrestrict|upload|restart|create|control|put|post|` +
		`write|request(dl|link)|unlock|magnet`)
	types := []reflect.Type{reflect.TypeFor[Provider](), reflect.TypeFor[*realDebrid](), reflect.TypeFor[*allDebrid](),
		reflect.TypeFor[*torBox](), reflect.TypeFor[*getter]()}
	for _, typ := range types {
		for i := range typ.NumMethod() {
			if m := typ.Method(i).Name; writey.MatchString(m) {
				t.Errorf("%v has a write-like method %s", typ, m)
			}
		}
	}
	// getter's unexported methods aren't in the method set above; check its source instead.
	b, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`func \(g \*getter\) (\w+)`).FindAllStringSubmatch(string(b), -1) {
		if m[1] != "get" && m[1] != "getJSON" {
			t.Errorf("getter has a method %s besides get and getJSON", m[1])
		}
	}
}
