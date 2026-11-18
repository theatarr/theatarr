package catalog

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// TagLabelWords is the refusal of a label outside the rule, in the arrs' words (TagController's validator).
const TagLabelWords = "Allowed characters a-z, 0-9 and -"

// ErrTagLabel is FindOrCreateTag's refusal of a label outside the rule.
var ErrTagLabel = errors.New("catalog: tag label outside the rule")

var tagLabel = regexp.MustCompile(`(?i)^[a-z0-9-]+$`)

// ValidTagLabel says whether a label keeps the rule: not empty, letters, digits and hyphens.
func ValidTagLabel(label string) bool { return label != "" && tagLabel.MatchString(label) }

// FindOrCreateTag is the one label rule (docs/design/arrview.md "Tags per version"), for the compatible tag create and
// Theatarr's own tag editor: a label outside the rule is refused (ErrTagLabel); an existing label in any case is
// reused as stored; a new one is stored lower-case.
func FindOrCreateTag(ctx context.Context, tx *sql.Tx, label string, now time.Time) (int64, string, error) {
	if !ValidTagLabel(label) {
		return 0, "", ErrTagLabel
	}
	var id int64
	var stored string
	err := tx.QueryRowContext(ctx, `SELECT id, label FROM arrview_tag WHERE label = ?`, label).Scan(&id, &stored)
	if err != sql.ErrNoRows {
		return id, stored, err
	}
	stored = strings.ToLower(label)
	res, err := tx.ExecContext(ctx, `INSERT INTO arrview_tag (label, created_at) VALUES (?, ?)`, stored, store.Millis(now))
	if err != nil {
		return 0, "", err
	}
	id, err = res.LastInsertId()
	return id, stored, err
}
