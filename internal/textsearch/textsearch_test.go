package textsearch

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const knight = "The.Dark.Knight.2008.UHD.BluRay.2160p.DTS-HD.MA.5.1.DV.HEVC.REMUX-FraMeSToR"

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		{"knight frame", true},
		{"frame knight", true},
		{"  KNIGHT   frame  ", true},
		{"", true},
		{"knight rises", false},
		{"knightframe", false},
	} {
		if got := Match(c.text, knight); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.text, got, c.want)
		}
	}
	if !Match("knight 2008", "The Dark Knight", "2008") {
		t.Error("words may be found in different haystacks")
	}
	if Match("kn", "dark", "night") {
		t.Error("a word must not span two haystacks")
	}
}

func TestWordsCap(t *testing.T) {
	if got := Words(strings.Repeat("a ", 15)); len(got) != MaxWords {
		t.Errorf("Words kept %d words, want %d", len(got), MaxWords)
	}
}

func TestWhereSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (name TEXT, other TEXT);
		INSERT INTO t VALUES ('` + knight + `', ''), ('100% Wolf', ''), ('1000 Wolves', ''), ('a_b', ''), ('axb', ''),
		('Untitled', 'Knight.Frame.2008')`); err != nil {
		t.Fatal(err)
	}
	names := func(text string, cols ...string) string {
		where, args := Where(text, cols...)
		q := `SELECT name FROM t`
		if where != "" {
			q += ` WHERE ` + where
		}
		rows, err := db.Query(q+` ORDER BY name`, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return strings.Join(out, "|")
	}
	for _, c := range []struct{ text, want string }{
		{"knight frame", knight},
		{"FRAME   knight", knight},
		{"100%", "100% Wolf"},
		{"0%", "100% Wolf"},
		{"a_b", "a_b"},
		{"wol", "100% Wolf|1000 Wolves"},
	} {
		if got := names(c.text, "name"); got != c.want {
			t.Errorf("Where(%q) = %q, want %q", c.text, got, c.want)
		}
	}
	if got := names("", "name"); strings.Count(got, "|") != 5 {
		t.Errorf("empty text should match all, got %q", got)
	}
	if got := names("knight 2008", "name", "other"); got != knight+"|Untitled" {
		t.Errorf("two columns: got %q", got)
	}
}
