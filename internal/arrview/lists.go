package arrview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/store"
)

type collectionIn struct {
	ID                  int64    `json:"id"`
	TmdbID              int64    `json:"tmdbId"`
	CollectionIDs       []int64  `json:"collectionIds"`
	Title               string   `json:"title"`
	Name                string   `json:"name"`
	Monitored           *bool    `json:"monitored"`
	QualityProfileID    int64    `json:"qualityProfileId"`
	RootFolderPath      string   `json:"rootFolderPath"`
	SearchOnAdd         *bool    `json:"searchOnAdd"`
	MinimumAvailability enumText `json:"minimumAvailability"`
}

type collectionResource struct {
	ID                  int64             `json:"id"`
	TmdbID              int64             `json:"tmdbId"`
	Title               string            `json:"title"`
	Name                string            `json:"name"`
	Monitored           bool              `json:"monitored"`
	QualityProfileID    int64             `json:"qualityProfileId"`
	RootFolderPath      string            `json:"rootFolderPath"`
	SearchOnAdd         bool              `json:"searchOnAdd"`
	MinimumAvailability string            `json:"minimumAvailability"`
	Movies              []collectionMovie `json:"movies"`
}

type collectionMovie struct {
	TmdbID    int    `json:"tmdbId"`
	Title     string `json:"title"`
	Overview  string `json:"overview"`
	Year      int    `json:"year"`
	MovieID   int64  `json:"movieId"`
	Monitored bool   `json:"monitored"`
	HasFile   bool   `json:"hasFile"`
}

type importListIn struct {
	ID                  int64       `json:"id"`
	Name                string      `json:"name"`
	Implementation      string      `json:"implementation"`
	ConfigContract      string      `json:"configContract"`
	Enabled             *bool       `json:"enabled"`
	Enable              *bool       `json:"enable"`
	EnableAuto          *bool       `json:"enableAuto"`
	ShouldMonitor       enumText    `json:"shouldMonitor"`
	Monitor             enumText    `json:"monitor"`
	SearchOnAdd         bool        `json:"searchOnAdd"`
	QualityProfileID    int64       `json:"qualityProfileId"`
	RootFolderPath      string      `json:"rootFolderPath"`
	MinimumAvailability enumText    `json:"minimumAvailability"`
	Fields              []listField `json:"fields"`
}

type listField struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

type importListResource struct {
	ID                  int64       `json:"id"`
	Name                string      `json:"name"`
	Implementation      string      `json:"implementation"`
	ImplementationName  string      `json:"implementationName"`
	ConfigContract      string      `json:"configContract"`
	ListType            string      `json:"listType"`
	Enabled             bool        `json:"enabled"`
	EnableAuto          bool        `json:"enableAuto"`
	ShouldMonitor       string      `json:"shouldMonitor"`
	Monitor             string      `json:"monitor"`
	SearchOnAdd         bool        `json:"searchOnAdd"`
	QualityProfileID    int64       `json:"qualityProfileId"`
	RootFolderPath      string      `json:"rootFolderPath"`
	MinimumAvailability string      `json:"minimumAvailability"`
	Fields              []listField `json:"fields"`
}

func (s *Service) collection(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		id, ok := r.pathID(1)
		if !ok {
			notFound(w, "collection not found")
			return
		}
		c, err := s.collectionResource(ctx, r.v, id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if c == nil {
			notFound(w, "collection not found")
			return
		}
		writeJSON(w, http.StatusOK, c)
		return
	}
	var in collectionIn
	if err := decodeBody(r, &in); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	ids := in.CollectionIDs
	if len(ids) == 0 {
		id := in.TmdbID
		if id <= 0 {
			id = in.ID
		}
		if p, ok := r.pathID(1); ok {
			id = p
		}
		ids = []int64{id}
	}
	for _, id := range ids {
		if id <= 0 {
			badRequest(w, "tmdbId", "A collection id is required", id)
			return
		}
	}
	minimum := in.MinimumAvailability.minAvail()
	if in.MinimumAvailability != "" && minimum == "" {
		badRequest(w, "minimumAvailability", "Unknown availability", nil)
		return
	}
	if minimum == "" {
		minimum = "released"
	}
	versions, err := s.listVersions(r.v, in.QualityProfileID, in.RootFolderPath)
	if err != nil {
		badRequest(w, "qualityProfileId", err.Error(), nil)
		return
	}
	explicit := in.QualityProfileID > 0 || in.RootFolderPath != ""
	name := in.Title
	if name == "" {
		name = in.Name
	}
	now := store.Millis(s.now())
	var noTarget int64
	err = s.st.WriteTx(ctx, func(tx *sqlTx) error {
		for _, id := range ids {
			keys, setVersions, setView, err := s.collectionTargets(ctx, tx, r.v, id, in.Monitored, versions, explicit)
			if err != nil {
				return err
			}
			if keys == nil {
				noTarget = id
				return errNoCollectionTarget
			}
			vb, _ := json.Marshal(keys)
			_, err = tx.ExecContext(ctx, `INSERT INTO movie_collection
    (tmdb_id,name,monitored,versions,view,quality_profile_id,root_folder_path,minimum_availability,search_on_add,updated_at)
    VALUES(?,?,COALESCE(?,0),?,?,?,?,?,COALESCE(?,0),?) ON CONFLICT(tmdb_id) DO UPDATE SET
    name=CASE WHEN excluded.name<>'' THEN excluded.name ELSE name END,
    monitored=COALESCE(?,monitored), search_on_add=COALESCE(?,search_on_add),
    versions=CASE WHEN ? THEN excluded.versions ELSE versions END,
    view=CASE WHEN ? THEN excluded.view ELSE view END,
    quality_profile_id=CASE WHEN ?>0 THEN excluded.quality_profile_id ELSE quality_profile_id END,
    root_folder_path=CASE WHEN excluded.root_folder_path<>'' THEN excluded.root_folder_path ELSE root_folder_path END,
    minimum_availability=CASE WHEN ?<>'' THEN excluded.minimum_availability ELSE minimum_availability END,
    updated_at=excluded.updated_at`, id, name, in.Monitored, string(vb), r.v.Key, in.QualityProfileID, strings.TrimSuffix(in.RootFolderPath, "/"), minimum, in.SearchOnAdd, now,
				in.Monitored, in.SearchOnAdd, setVersions, setView, in.QualityProfileID, in.MinimumAvailability)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errNoCollectionTarget) {
		badRequest(w, "qualityProfileId", "A monitored collection needs a target library that is an active movie version no other version follows", noTarget)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(in.CollectionIDs) > 0 {
		s.collections(w, r)
		return
	}
	c, err := s.collectionResource(ctx, r.v, ids[0])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, c)
}

var errNoCollectionTarget = errors.New("arrview: no collection target")

// collectionTargets are the versions a PUT collection stores for collection id (docs/design/arrview.md, Collections):
// the request's profile/root versions when it names them (explicit), else the stored ones. A collection the request
// leaves monitored keeps only collection targets (policy CollectionTarget), written explicitly; with none, a
// per-version view's own version when it is one, and otherwise nil: the request is refused. setView is whether the
// row takes this view's settings.
func (s *Service) collectionTargets(ctx context.Context, tx *sqlTx, v *View, id int64, monitored *bool,
	requested []string, explicit bool) (keys []string, setVersions, setView bool, err error) {
	on, stored := false, "[]"
	err = tx.QueryRowContext(ctx, `SELECT monitored, versions FROM movie_collection WHERE tmdb_id=?`, id).Scan(&on, &stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, false, err
	}
	if monitored != nil {
		on = *monitored
	}
	keys = requested
	if !explicit {
		keys = nil
		if err := json.Unmarshal([]byte(stored), &keys); err != nil {
			return nil, false, false, err
		}
	}
	if keys == nil {
		keys = []string{}
	}
	if !on {
		return keys, explicit, explicit, nil
	}
	eligible := []string{}
	for _, k := range keys {
		if s.pol.CollectionTarget(k) {
			eligible = append(eligible, k)
		}
	}
	setView = explicit
	if k, ok := strings.CutPrefix(v.Key, "v/"); ok && len(eligible) == 0 && s.pol.CollectionTarget(k) {
		eligible, setView = []string{k}, true
	}
	if len(eligible) == 0 {
		return nil, false, false, nil
	}
	return eligible, true, setView, nil
}

func (s *Service) collections(w http.ResponseWriter, r *request) {
	if err := catalog.DiscoverCollections(r.Context(), s.st, s.now()); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.st.Reader().QueryContext(r.Context(), `SELECT tmdb_id FROM movie_collection ORDER BY tmdb_id`)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		ids = append(ids, id)
	}
	if err := closeRows(rows); err != nil {
		s.internal(w, r, err)
		return
	}
	out := []collectionResource{}
	for _, id := range ids {
		c, err := s.collectionResource(r.Context(), r.v, id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if c != nil {
			out = append(out, *c)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) collectionResource(ctx context.Context, v *View, id int64) (*collectionResource, error) {
	var c collectionResource
	var movies, versions string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT tmdb_id,name,monitored,quality_profile_id,root_folder_path,search_on_add,minimum_availability,movies,versions
	 FROM movie_collection WHERE tmdb_id=?`, id).Scan(&c.TmdbID, &c.Name, &c.Monitored, &c.QualityProfileID, &c.RootFolderPath, &c.SearchOnAdd, &c.MinimumAvailability, &movies, &versions)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.QualityProfileID, c.RootFolderPath, err = s.intakeTargets(v, versions, c.QualityProfileID, c.RootFolderPath)
	if err != nil {
		return nil, err
	}
	c.ID = id
	c.Title = c.Name
	c.Movies = []collectionMovie{}
	var parts []tmdb.MovieSummary
	if err := json.Unmarshal([]byte(movies), &parts); err != nil {
		return nil, err
	}
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT DISTINCT t.id FROM title t JOIN title_external_id x ON x.title_id=t.id
  WHERE t.kind='movie' AND x.source='tmdb_collection' AND x.external_id=? ORDER BY t.id`, strconv.FormatInt(id, 10))
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var tid int64
		if err := rows.Scan(&tid); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, tid)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	known := map[int]*entry{}
	if len(ids) > 0 {
		entries, err := s.load(ctx, v, ids)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			m := s.movieResource(v, e)
			known[m.TmdbID] = e
			if !slices.ContainsFunc(parts, func(p tmdb.MovieSummary) bool { return p.ID == m.TmdbID }) {
				parts = append(parts, tmdb.MovieSummary{ID: m.TmdbID, Title: m.Title})
			}
		}
	}
	for _, part := range parts {
		m := collectionMovie{TmdbID: part.ID, Title: part.Title, Overview: part.Overview}
		if date, err := time.Parse(time.DateOnly, part.ReleaseDate); err == nil {
			m.Year = date.Year()
		}
		if e := known[part.ID]; e != nil {
			movie := s.movieResource(v, e)
			m.MovieID = movie.ID
			m.Monitored = movie.Monitored
			m.Year = movie.Year
			m.HasFile = movie.HasFile != nil && *movie.HasFile
		}
		c.Movies = append(c.Movies, m)
	}
	return &c, nil
}

func listTemplate() importListResource {
	return importListResource{Implementation: "TmdbList", ImplementationName: "TMDb List", ConfigContract: "TmdbListSettings", ListType: "tmdb",
		Enabled: true, EnableAuto: true, ShouldMonitor: "movieOnly", Monitor: "movieOnly", MinimumAvailability: "released", Fields: []listField{{Name: "listId", Value: ""}}}
}

func (s *Service) readImportLists(ctx context.Context, v *View) ([]importListResource, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id,name,enabled,enable_auto,monitor,search_on_add,quality_profile_id,root_folder_path,minimum_availability,tmdb_id,versions FROM import_list WHERE view=? AND list_type='tmdb' ORDER BY id`, v.Key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []importListResource{}
	for rows.Next() {
		x := listTemplate()
		var tmdbID int64
		var versions string
		if err := rows.Scan(&x.ID, &x.Name, &x.Enabled, &x.EnableAuto, &x.ShouldMonitor, &x.SearchOnAdd, &x.QualityProfileID, &x.RootFolderPath, &x.MinimumAvailability, &tmdbID, &versions); err != nil {
			return nil, err
		}
		x.QualityProfileID, x.RootFolderPath, err = s.intakeTargets(v, versions, x.QualityProfileID, x.RootFolderPath)
		if err != nil {
			return nil, err
		}
		x.Monitor = x.ShouldMonitor
		x.Fields = []listField{{Name: "listId", Value: strconv.FormatInt(tmdbID, 10)}}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Service) importLists(w http.ResponseWriter, r *request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if len(r.seg) == 2 && r.seg[1] == "schema" {
			x := listTemplate()
			writeJSON(w, http.StatusOK, []any{map[string]any{"implementation": x.Implementation, "implementationName": x.ImplementationName, "configContract": x.ConfigContract,
				"listType": x.ListType, "enabled": true, "enableAuto": true, "shouldMonitor": x.ShouldMonitor, "searchOnAdd": false, "minimumAvailability": x.MinimumAvailability,
				"fields": []any{map[string]any{"name": "listId", "label": "List ID", "type": "textbox", "value": "", "order": 0, "privacy": "normal", "isAdvanced": false}}}})
			return
		}
		lists, err := s.readImportLists(r.Context(), r.v)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if len(r.seg) == 2 {
			id, ok := r.pathID(1)
			for _, x := range lists {
				if ok && x.ID == id {
					writeJSON(w, http.StatusOK, x)
					return
				}
			}
			notFound(w, "import list not found")
			return
		}
		writeJSON(w, http.StatusOK, lists)
		return
	}
	if r.Method == http.MethodDelete {
		id, ok := r.pathID(1)
		if !ok {
			notFound(w, "import list not found")
			return
		}
		res, err := s.st.Writer().ExecContext(r.Context(), `DELETE FROM import_list WHERE id=? AND view=?`, id, r.v.Key)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		n, err := res.RowsAffected()
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if n == 0 {
			notFound(w, "import list not found")
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	var in importListIn
	if err := decodeBody(r, &in); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		badRequest(w, "name", "Name is required", nil)
		return
	}
	if in.Implementation != "" && !strings.EqualFold(in.Implementation, "TmdbList") || in.ConfigContract != "" && in.ConfigContract != "TmdbListSettings" {
		badRequest(w, "implementation", "Only TMDb List is supported", nil)
		return
	}
	id := int64(0)
	if r.Method == http.MethodPut {
		id = in.ID
		if p, ok := r.pathID(1); ok {
			id = p
		}
		if id <= 0 {
			notFound(w, "import list not found")
			return
		}
	}
	tmdbID := int64(0)
	for _, f := range in.Fields {
		if strings.EqualFold(f.Name, "listId") {
			switch v := f.Value.(type) {
			case string:
				tmdbID, _ = strconv.ParseInt(v, 10, 64)
			case float64:
				if v > 0 && v < 1<<53 && v == float64(int64(v)) {
					tmdbID = int64(v)
				}
			}
		}
	}
	if tmdbID <= 0 {
		badRequest(w, "fields", "A positive listId is required", nil)
		return
	}
	monitor := in.Monitor
	if monitor == "" {
		monitor = in.ShouldMonitor
	}
	mode := monitor.value("movieOnly", "movieAndCollection", "none")
	if monitor != "" && mode == "" {
		badRequest(w, "monitor", "Unknown monitoring option", nil)
		return
	}
	if mode == "" {
		mode = "movieOnly"
	}
	minimum := in.MinimumAvailability.minAvail()
	if in.MinimumAvailability != "" && minimum == "" {
		badRequest(w, "minimumAvailability", "Unknown availability", nil)
		return
	}
	if minimum == "" {
		minimum = "released"
	}
	versions, err := s.listVersions(r.v, in.QualityProfileID, in.RootFolderPath)
	if err != nil {
		badRequest(w, "qualityProfileId", err.Error(), nil)
		return
	}
	vb, _ := json.Marshal(versions)
	enabled, auto := true, true
	if in.Enable != nil {
		enabled = *in.Enable
	}
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if in.EnableAuto != nil {
		auto = *in.EnableAuto
	}
	now := store.Millis(s.now())
	duplicate := errors.New("duplicate list")
	err = s.st.WriteTx(r.Context(), func(tx *sqlTx) error {
		var exists bool
		if id > 0 {
			if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM import_list WHERE id=? AND view=?)`, id, r.v.Key).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return sql.ErrNoRows
			}
		}
		if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM import_list WHERE list_type='tmdb' AND tmdb_id=? AND id<>?)`, tmdbID, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return duplicate
		}
		args := []any{in.Name, tmdbID, enabled, auto, mode, in.SearchOnAdd, string(vb), r.v.Key, in.QualityProfileID, strings.TrimSuffix(in.RootFolderPath, "/"), minimum, now}
		if id > 0 {
			res, err := tx.ExecContext(r.Context(), `UPDATE import_list SET name=?,tmdb_id=?,enabled=?,enable_auto=?,monitor=?,search_on_add=?,versions=?,view=?,quality_profile_id=?,root_folder_path=?,minimum_availability=?,updated_at=? WHERE id=? AND view=?`, append(args, id, r.v.Key)...)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err == nil && n == 0 {
				return sql.ErrNoRows
			}
			return err
		}
		res, err := tx.ExecContext(r.Context(), `INSERT INTO import_list(name,tmdb_id,enabled,enable_auto,monitor,search_on_add,versions,view,quality_profile_id,root_folder_path,minimum_availability,updated_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, append(args, now)...)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if errors.Is(err, duplicate) {
		badRequest(w, "fields", "TMDb list already exists", nil)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, "import list not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	lists, err := s.readImportLists(r.Context(), r.v)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, x := range lists {
		if x.ID == id {
			status := http.StatusCreated
			if r.Method == http.MethodPut {
				status = http.StatusAccepted
			}
			writeJSON(w, status, x)
			return
		}
	}
	notFound(w, "import list not found")
}

// Snapshot both anime choices through the same profile/root mapping as movie adds.
func (s *Service) listVersions(v *View, profile int64, root string) ([]string, error) {
	in := titleIn{QualityProfileID: profile, RootFolderPath: root}
	a, errA := s.chooseVersions(v, &in, false)
	b, errB := s.chooseVersions(v, &in, true)
	if errA != nil && errB != nil {
		return nil, errA
	}
	for _, k := range b {
		if !slices.Contains(a, k) {
			a = append(a, k)
		}
	}
	slices.Sort(a)
	return a, nil
}

func (s *Service) intakeTargets(v *View, versions string, profile int64, root string) (int64, string, error) {
	var keys []string
	if err := json.Unmarshal([]byte(versions), &keys); err != nil {
		return 0, "", err
	}
	e := &entry{rec: &viewRec{qpid: profile}}
	for _, key := range v.Versions {
		if slices.Contains(keys, key) {
			e.versions = append(e.versions, &ver{key: key})
			if root == "" {
				if vp := s.pol.Versions[key]; vp != nil {
					root = vp.Library
				}
			}
		}
	}
	return qualityProfileID(v, e), root, nil
}

func (s *Service) exclusions(w http.ResponseWriter, r *request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		rows, err := s.st.Reader().QueryContext(r.Context(), `SELECT tmdb_id FROM movie_exclusion ORDER BY tmdb_id`)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				s.internal(w, r, err)
				return
			}
			out = append(out, map[string]any{"id": id, "tmdbId": id})
		}
		if err := rows.Err(); err != nil {
			s.internal(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if r.Method == http.MethodDelete {
		id, ok := r.pathID(1)
		if !ok {
			notFound(w, "exclusion not found")
			return
		}
		if _, err := s.st.Writer().ExecContext(r.Context(), `DELETE FROM movie_exclusion WHERE tmdb_id=?`, id); err != nil {
			s.internal(w, r, err)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	var in struct {
		TmdbID int64 `json:"tmdbId"`
	}
	if err := decodeBody(r, &in); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	if in.TmdbID <= 0 {
		badRequest(w, "tmdbId", "A positive TMDb id is required", nil)
		return
	}
	if _, err := s.st.Writer().ExecContext(r.Context(), `INSERT INTO movie_exclusion(tmdb_id,created_at) VALUES(?,?) ON CONFLICT DO NOTHING`, in.TmdbID, store.Millis(s.now())); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": in.TmdbID, "tmdbId": in.TmdbID})
}
