package errinbox

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Size caps, in bytes, on what a row keeps.
const (
	maxMessage = 1 << 10
	maxContext = 8 << 10
	maxStack   = 16 << 10
)

var (
	urlRE    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.\-]*://[^\s"'<>]+`)
	quotedRE = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'[^'\s]*'|` + "`[^`]*`")
	uuidRE   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	hexRE    = regexp.MustCompile(`\b(?:0x)?[0-9a-fA-F]{6,}\b`)
	numberRE = regexp.MustCompile(`\d+(?:\.\d+)?`)
	spaceRE  = regexp.MustCompile(`\s+`)
)

// Normalize reduces a message to its shape, so sightings that differ only in their values group together: URLs,
// quoted values, UUIDs, paths, hashes and numbers become placeholders and whitespace collapses. The result is at most
// 1 KB.
func Normalize(msg string) string {
	s := urlRE.ReplaceAllStringFunc(msg, func(u string) string {
		trimmed := strings.TrimRight(u, ".,:;)]")
		return "<url>" + u[len(trimmed):]
	})
	s = quotedRE.ReplaceAllString(s, `"…"`)
	s = uuidRE.ReplaceAllString(s, "<id>")
	s = normalizePaths(s)
	s = hexRE.ReplaceAllStringFunc(s, func(m string) string {
		// A hash mixes digits and letters; plain numbers are left for numberRE, words like "decade" alone.
		h := strings.TrimPrefix(m, "0x")
		if !strings.ContainsAny(h, "0123456789") || !strings.ContainsAny(h, "abcdefABCDEF") {
			return m
		}
		return "<hex>"
	})
	s = numberRE.ReplaceAllString(s, "<n>")
	s = strings.TrimSpace(spaceRE.ReplaceAllString(s, " "))
	return capString(s, maxMessage)
}

// normalizePaths replaces absolute paths with <path>. A path starts with '/' after the start, a space, '(', '=' or
// '[' and runs to the next ": " (Go's "open /path: reason"), quote, comma, semicolon, newline or the end, so a path
// with spaces in a file name stays one path.
func normalizePaths(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '/' || i+1 >= len(s) || s[i+1] == ' ' || s[i+1] == '/' || (i > 0 && !strings.ContainsRune(" (=[", rune(s[i-1]))) {
			b.WriteByte(c)
			continue
		}
		j := i + 1
		for j < len(s) && !strings.ContainsRune("\n\"',;", rune(s[j])) && !(s[j] == ':' && (j+1 == len(s) || s[j+1] == ' ')) {
			j++
		}
		b.WriteString("<path>")
		i = j - 1
	}
	return b.String()
}

// Fingerprint groups sightings: source, component, normalized message and, for a panic, the top in-repo frame.
func Fingerprint(source, component, normalized, frame string) string {
	h := sha256.Sum256([]byte(source + "\x00" + component + "\x00" + normalized + "\x00" + frame))
	return hex.EncodeToString(h[:16])
}

// modulePath marks the frames of Theatarr's own code in a stack.
const modulePath = "github.com/theatarr/theatarr/"

// TopFrame is the function of the first in-repo frame below the panic in a debug.Stack() trace: where the panic
// happened, not where it was recovered. Without a panic frame (a stack taken outside a recovery) it is the first
// in-repo frame outside this package. Line numbers are left out, so an unrelated edit keeps the fingerprint.
func TopFrame(stack string) string {
	lines := strings.Split(stack, "\n")
	start := 0
	for i, l := range lines {
		if strings.HasPrefix(l, "panic(") {
			start = i + 1
			break
		}
	}
	for _, l := range lines[start:] {
		if strings.HasPrefix(l, "\t") || !strings.Contains(l, modulePath) {
			continue
		}
		fn := l
		if i := strings.LastIndexByte(fn, '('); i > 0 {
			fn = fn[:i]
		}
		if start == 0 && strings.HasPrefix(fn, modulePath+"internal/errinbox.") {
			continue
		}
		return strings.TrimPrefix(fn, modulePath)
	}
	return ""
}

// capString cuts s to at most n bytes on a rune boundary, marking the cut.
func capString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !runeStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func runeStart(b byte) bool { return b&0xC0 != 0x80 }
