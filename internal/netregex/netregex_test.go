package netregex

import (
	"testing"

	"github.com/dlclark/regexp2"
)

func TestDotNetSemantics(t *testing.T) {
	cases := []struct {
		name  string
		expr  string
		opts  regexp2.RegexOptions
		input string
		want  bool
	}{
		{"lookbehind", `(?<!\d)2160p`, IgnoreCase, "Title.2160P.WEB", true},
		{"lookahead", `x26[45](?!\d)`, IgnoreCase, "Title.x2650", false},
		// .NET \d matches any Unicode decimal digit; RE2's matches only ASCII.
		{"unicode digit", `^\d+$`, None, "١٢٣", true},
		{"unicode word boundary", `\bé\b`, None, "a é b", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := MustCompile(c.expr, c.opts).MatchString(c.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("%q on %q = %v, want %v", c.expr, c.input, got, c.want)
			}
		})
	}
}

func TestRejectsRE2Option(t *testing.T) {
	if _, err := Compile(`a`, regexp2.RE2); err == nil {
		t.Fatal("Compile accepted the RE2 option")
	}
}

func TestMatchTimeoutSet(t *testing.T) {
	if got := MustCompile(`a`, None).MatchTimeout; got != MatchTimeout {
		t.Fatalf("MatchTimeout = %v, want %v", got, MatchTimeout)
	}
}
