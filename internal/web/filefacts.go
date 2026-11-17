package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
)

// Edit file facts (docs/design/library.md "Correcting an installed file"): a manager corrects the quality,
// revision, languages and release group one of the title's files was imported with, from the file rows of a movie's
// library row and of the episode window. library.EditFileFacts stores them and judges each library's cutoff again;
// nothing is renamed. The compatible API's file reads show the corrected values (they read the same link).

type factsOps interface {
	EditFileFacts(ctx context.Context, linkID int64, e library.FileFactsEdit) (*library.FileFactsEdited, error)
}

// FileFacts is the answer of GET links/{id}/facts: the file's stored facts, and the choices the form offers (the
// title kind's qualities and languages, as manual import offers them).
type FileFacts struct {
	LinkID       int64    `json:"link_id"`
	Path         string   `json:"path"`
	Quality      string   `json:"quality"`
	Revision     int      `json:"revision"`
	IsRepack     bool     `json:"is_repack,omitempty"`
	Languages    []string `json:"languages"`
	ReleaseGroup string   `json:"release_group"`
	// QualityChoices and LanguageChoices are the names the form offers.
	QualityChoices  []string `json:"quality_choices"`
	LanguageChoices []string `json:"language_choices"`
}

// FileFactsEdit is the body of PATCH links/{id}/facts.
type FileFactsEdit struct {
	Quality      string   `json:"quality"`
	Revision     int      `json:"revision"`
	Languages    []string `json:"languages"`
	ReleaseGroup string   `json:"release_group"`
}

// FileFactsResult is the answer of PATCH links/{id}/facts: each library's own score of the file now.
type FileFactsResult struct {
	Message string           `json:"message"`
	Scores  []FileFactsScore `json:"scores"`
}

// FileFactsScore is one library's format score of a file.
type FileFactsScore struct {
	Library string `json:"library"`
	Score   int    `json:"score"`
}

func (w *Web) mountFileFactsAPI(mux *http.ServeMux) {
	w.handle(mux, "GET "+APIBase+"/links/{id}/facts", SignedIn, auth.PermManage, w.apiFileFacts)
	w.handle(mux, "PATCH "+APIBase+"/links/{id}/facts", SignedIn, auth.PermManage, w.apiEditFileFacts)
}

// flavourOfKind is the arr whose quality and language lists a title kind's files use.
func flavourOfKind(kind string) format.Flavour {
	if policy.Kind(kind) == policy.KindSeries {
		return format.Sonarr
	}
	return format.Radarr
}

// languageChoices are the languages manual import offers for the flavour, by name.
func languageChoices(fl format.Flavour) []string {
	var out []string
	for id := 1; id < 60; id++ {
		if fl == format.Sonarr {
			if l, ok := sonarr.FindLanguageByID(id); ok {
				out = append(out, l.Name)
			}
		} else if l, ok := radarr.LanguageByID(id); ok {
			out = append(out, l.Name)
		}
	}
	return out
}

// languageOptions are the languages the form offers for a file: the languages it is stored with that manual import
// does not offer (Unknown, Original), so they are kept as they are, then the flavour's (languageChoices).
func languageOptions(fl format.Flavour, stored []string) []string {
	all := languageChoices(fl)
	var out []string
	for _, l := range stored {
		if !slices.ContainsFunc(all, func(c string) bool { return strings.EqualFold(c, l) }) && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return append(out, all...)
}

// GET /links/{id}/facts: one active link's stored facts, with the form's choices.
func (w *Web) apiFileFacts(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var raw, kind, release, quality string
	out := FileFacts{LinkID: id}
	err := w.st.Reader().QueryRowContext(ctx, `SELECT l.path, COALESCE(l.file_metadata, '{}'), t.kind,
		COALESCE(l.release_title, ''), COALESCE(l.quality, '') FROM link l JOIN title t ON t.id = l.title_id
		WHERE l.id = ? AND l.state = 'active'`, id).Scan(&out.Path, &raw, &kind, &release, &quality)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	f, err := decide.LinkFileInputs([]byte(raw), kind, release, quality)
	if err != nil {
		w.internal(rw, err)
		return
	}
	out.Quality, out.Revision, out.IsRepack = f.Quality, max(f.Revision, 1), f.IsRepack
	out.Languages, out.ReleaseGroup = f.Languages, f.ReleaseGroup
	if out.Languages == nil {
		out.Languages = []string{}
	}
	fl := flavourOfKind(kind)
	for _, q := range profile.Qualities(fl) {
		out.QualityChoices = append(out.QualityChoices, q.Name)
	}
	out.LanguageChoices = languageOptions(fl, out.Languages)
	writeJSON(rw, http.StatusOK, out)
}

// PATCH /links/{id}/facts: store a file's corrected facts (library.EditFileFacts) and record it in the journal. 400
// for a quality or language the title kind does not have; 409 when the file is no longer in a library.
func (w *Web) apiEditFileFacts(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var body FileFactsEdit
	if err := decodeJSON(r, &body); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	body.ReleaseGroup = strings.TrimSpace(body.ReleaseGroup)
	if utf8.RuneCountInString(body.ReleaseGroup) > 100 {
		badRequest(rw, "release_group: at most 100 characters")
		return
	}
	if body.Revision < 1 || body.Revision > 9 {
		badRequest(rw, "revision: 1 to 9")
		return
	}
	var raw, kind, release, quality string
	err := w.st.Reader().QueryRowContext(ctx, `SELECT COALESCE(l.file_metadata, '{}'), t.kind,
		COALESCE(l.release_title, ''), COALESCE(l.quality, '') FROM link l JOIN title t ON t.id = l.title_id
		WHERE l.id = ?`, id).Scan(&raw, &kind, &release, &quality)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	fl := flavourOfKind(kind)
	q, ok := profile.QualityByName(fl, body.Quality)
	if !ok {
		badRequest(rw, "quality: not a quality of this kind of title")
		return
	}
	body.Quality = q.Name
	stored, err := decide.LinkFileInputs([]byte(raw), kind, release, quality)
	if err != nil {
		w.internal(rw, err)
		return
	}
	choices := languageOptions(fl, stored.Languages)
	langs := []string{}
	for _, l := range body.Languages {
		i := slices.IndexFunc(choices, func(c string) bool { return strings.EqualFold(c, l) })
		if i < 0 {
			badRequest(rw, "languages: "+strconv.Quote(l)+" is not a language")
			return
		}
		if !slices.Contains(langs, choices[i]) {
			langs = append(langs, choices[i])
		}
	}
	if w.noOps(rw) {
		return
	}
	ops, ok := w.ops.(factsOps)
	if !ok {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "editing file facts is unavailable"})
		return
	}
	res, err := ops.EditFileFacts(ctx, id, library.FileFactsEdit{Quality: body.Quality, Revision: body.Revision,
		Languages: langs, ReleaseGroup: body.ReleaseGroup})
	var ref *library.Refused
	switch {
	case errors.As(err, &ref):
		writeJSON(rw, http.StatusConflict, apiError{Error: ref.Detail})
		return
	case errors.Is(err, sql.ErrNoRows):
		notFound(rw)
		return
	case err != nil:
		w.opsError(rw, err)
		return
	}
	out := FileFactsResult{Scores: []FileFactsScore{}}
	labels := w.keyLabels(res.Versions)
	var scores []string
	for i, k := range res.Versions {
		if i < len(res.Scores) {
			out.Scores = append(out.Scores, FileFactsScore{Library: labels[k], Score: res.Scores[i]})
			scores = append(scores, fmt.Sprintf("%s %d", labels[k], res.Scores[i]))
		}
	}
	change := factsChange(res.Before, res.After)
	out.Message = "Saved: " + change + "."
	if len(scores) > 0 {
		out.Message += " Score: " + strings.Join(scores, ", ") + "."
	}
	by := actor(r)
	detail := change
	if len(scores) > 0 {
		detail += "; score " + strings.Join(scores, ", ")
	}
	w.record(ctx, journal.Action{Action: journal.ActEditFile, TitleID: res.TitleID, Version: strings.Join(res.Versions, ","),
		Target: res.Path, Detail: detail, By: by})
	w.log.Info("file facts corrected", "link", id, "by", orSomeone(by), "change", change)
	writeJSON(rw, http.StatusOK, out)
}

// qualityWords is a file's quality as the journal names it: "Bluray-1080p", "WEBDL-1080p Proper", "Repack v3".
func qualityWords(f decide.ExistingFile) string {
	s := f.Quality
	switch {
	case f.Revision > 1 && f.IsRepack:
		s += " Repack"
	case f.Revision > 1:
		s += " Proper"
	}
	if f.Revision > 2 {
		s += " v" + strconv.Itoa(f.Revision)
	}
	return s
}

// factsChange is what a correction changed, in words ("WEBDL-1080p → Bluray-1080p; languages English → French"); the
// quality is always named.
func factsChange(before, after decide.ExistingFile) string {
	q := qualityWords(after)
	if b := qualityWords(before); b != q {
		q = b + " → " + q
	}
	parts := []string{q}
	if !slices.Equal(before.Languages, after.Languages) {
		parts = append(parts, "languages "+orDash(strings.Join(before.Languages, ", "))+" → "+
			orDash(strings.Join(after.Languages, ", ")))
	}
	if before.ReleaseGroup != after.ReleaseGroup {
		parts = append(parts, "release group "+orDash(before.ReleaseGroup)+" → "+orDash(after.ReleaseGroup))
	}
	return strings.Join(parts, "; ")
}
