package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/migration"
	"github.com/theatarr/theatarr/internal/settings"
)

const migrateArrKey = "radarr-key-0123456789abcdef"

// fakeRadarr answers one Radarr instance from the migration fixtures (testdata/stack/radarr_uhd) and the decide
// snapshot it stands for, and fails the test on anything but a GET.
func fakeRadarr(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("radarr got %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Api-Key") != migrateArrKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ep := strings.TrimPrefix(r.URL.Path, "/api/v3/")
		file := filepath.Join("..", "migration", "testdata", "stack", "radarr_uhd", strings.ReplaceAll(ep, "/", "_")+".json")
		switch ep {
		case "qualityprofile", "customformat", "qualitydefinition":
			file = filepath.Join("..", "decide", "testdata", "arr", "radarr_4k_debrid", "snapshot", ep+".json")
		case "config/naming":
			w.Write([]byte(`{"renameMovies": true, "colonReplacementFormat": "delete", "standardMovieFormat": "{Movie CleanTitle} ({Release Year})", "movieFolderFormat": "{Movie CleanTitle} ({Release Year})"}`))
			return
		}
		b, err := os.ReadFile(file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (se *settingsEnv) migrateCall(method, path string, body any) *httptest.ResponseRecorder {
	se.t.Helper()
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			se.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, APIBase+"/migrate"+path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(se.cookie)
	r.Header.Set(auth.CSRFHeader, se.csrf)
	rec := se.do(r)
	se.bodies = append(se.bodies, rec.Body.String())
	return rec
}

func TestMigrateAPI(t *testing.T) {
	t.Parallel()
	se := newQualityEnv(t)
	rec := se.migrateCall("GET", "/plans", nil)
	se.want(rec, http.StatusServiceUnavailable, "without migration hooks")

	radarr := fakeRadarr(t)
	checkpoints := 0
	se.web.migration = &Migration{
		Checkpoint: func(ctx context.Context) (migration.Checkpoint, error) {
			checkpoints++
			snap, err := se.coord.Snapshot(ctx)
			if err != nil {
				return migration.Checkpoint{}, err
			}
			return migration.Checkpoint{At: testNow, Backup: "/backups/theatarr-test", Generation: snap.Generation}, nil
		},
		Dial: migration.NewDialer(arr.Options{Timeout: 5 * time.Second, MinInterval: time.Millisecond, Retries: 1,
			Backoff: time.Millisecond}),
	}
	sources := migration.Sources{Arr: []migration.ArrSource{{Name: "radarr_uhd", Flavour: "radarr", URL: radarr.URL,
		Key: "RADARR_UHD_KEY"}}}
	keys := map[string]string{"RADARR_UHD_KEY": migrateArrKey}

	rec = se.migrateCall("POST", "/inspect", MigrateInspectRequest{Sources: sources})
	if out := se.want(rec, http.StatusBadRequest, "inspect without the key"); !strings.Contains(out["error"].(string), "RADARR_UHD_KEY is not set") {
		t.Errorf("error %v", out)
	}
	rec = se.migrateCall("POST", "/inspect", MigrateInspectRequest{Sources: sources, Keys: keys})
	se.want(rec, http.StatusOK, "inspect")
	var inv migration.Inventory
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil || len(inv.Arr) != 1 || inv.Arr[0].Titles.Total != 3 ||
		inv.Arr[0].Raw != nil || inv.SchemaVersion != migration.SchemaVersion {
		t.Fatalf("inventory %+v %v", inv, err)
	}

	rec = se.migrateCall("POST", "/plans", MigratePlanRequest{Inventory: &inv, Keys: keys})
	se.want(rec, http.StatusBadRequest, "keys with an inventory")
	rec = se.migrateCall("POST", "/plans", MigratePlanRequest{})
	se.want(rec, http.StatusBadRequest, "neither sources nor inventory")
	rec = se.migrateCall("POST", "/plans", MigratePlanRequest{Sources: &sources, Keys: keys,
		Options: migration.PlanOptions{Name: "api"}})
	se.want(rec, http.StatusCreated, "plan")
	var view MigratePlan
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || view.Plan.ID == 0 || view.Plan.Name != "api" {
		t.Fatalf("plan %+v %v", view.Plan, err)
	}
	id := view.Plan.ID
	quality := ""
	for _, s := range view.Plan.Steps {
		if s.Kind == migration.KindQuality {
			quality = s.ID
		}
	}
	if quality == "" {
		t.Fatalf("no quality step: %+v", view.Plan.Steps)
	}

	rec = se.migrateCall("GET", "/plans", nil)
	var list []migration.Summary
	if se.want(rec, http.StatusOK, "list"); json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("list %s", rec.Body.String())
	}
	rec = se.migrateCall("GET", "/plans/"+itoa(id), nil)
	se.want(rec, http.StatusOK, "get")
	se.want(se.migrateCall("GET", "/plans/999", nil), http.StatusNotFound, "unknown plan")

	gen := se.generation()
	rec = se.migrateCall("POST", "/plans/"+itoa(id)+"/apply", MigrateApplyRequest{Steps: []string{"import/radarr_uhd"}})
	se.want(rec, http.StatusBadRequest, "apply a manual step")
	if checkpoints != 0 || se.generation() != gen {
		t.Fatal("a refused apply took a checkpoint or wrote")
	}
	rec = se.migrateCall("POST", "/plans/"+itoa(id)+"/apply", MigrateApplyRequest{Steps: []string{quality}})
	se.want(rec, http.StatusOK, "apply")
	var applied migration.Plan
	if err := json.Unmarshal(rec.Body.Bytes(), &applied); err != nil || applied.Step(quality).Status != migration.StatusDone ||
		len(applied.Checkpoints) != 1 || checkpoints != 1 || se.generation() == gen {
		t.Fatalf("applied %+v %v", applied.Step(quality), err)
	}

	rec = se.migrateCall("POST", "/plans/"+itoa(id)+"/verify", nil)
	se.want(rec, http.StatusOK, "verify")
	var rep migration.VerifyReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil || len(rep.Versions) != 1 || rep.Versions[0].Ready ||
		rep.Rollback == nil || rep.Rollback.Backup != "/backups/theatarr-test" {
		t.Fatalf("verify %s", rec.Body.String())
	}

	se.web.migration.Checkpoint = func(context.Context) (migration.Checkpoint, error) {
		return migration.Checkpoint{}, errors.New("backup dir not writable")
	}
	rec = se.migrateCall("POST", "/plans/"+itoa(id)+"/apply", MigrateApplyRequest{})
	if out := se.want(rec, http.StatusInternalServerError, "apply without a checkpoint"); !strings.Contains(out["error"].(string), "nothing was applied") {
		t.Errorf("error %v", out)
	}

	all := strings.Join(se.bodies, "\n") + se.logs.String()
	if strings.Contains(all, migrateArrKey) {
		t.Error("a response or the log holds the arr key")
	}
	if !strings.Contains(se.logs.String(), "migration plan applied") {
		t.Error("apply not logged")
	}
}

// A browser can preview gaps and save a complete plan using inspect alone: source keys never need to be retained
// for /plans, and the raw profile bodies never leave the server.
func TestMigrateInspectPlan(t *testing.T) {
	t.Parallel()
	se := newQualityEnv(t)
	radarr := fakeRadarr(t)
	se.web.migration = &Migration{
		Checkpoint: func(context.Context) (migration.Checkpoint, error) {
			t.Fatal("inspect took a checkpoint")
			return migration.Checkpoint{}, nil
		},
		Dial: migration.NewDialer(arr.Options{Timeout: 5 * time.Second, MinInterval: time.Millisecond, Retries: 1,
			Backoff: time.Millisecond}),
	}
	sources := migration.Sources{Arr: []migration.ArrSource{{Name: "radarr_uhd", Flavour: "radarr", URL: radarr.URL,
		Key: "RADARR_UHD_KEY"}}}
	keys := map[string]string{"RADARR_UHD_KEY": migrateArrKey}
	gen := se.generation()
	se.want(se.migrateCall("POST", "/inspect", MigrateInspectRequest{Sources: sources, Keys: keys, SavePlan: true}),
		http.StatusBadRequest, "save needs options")

	for _, save := range []bool{false, true} {
		rec := se.migrateCall("POST", "/inspect", MigrateInspectRequest{Sources: sources, Keys: keys,
			Options: &migration.PlanOptions{Name: "wizard"}, SavePlan: save})
		se.want(rec, http.StatusOK, "inspect with plan")
		var result MigrateInspectResult
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Plan == nil || result.Inventory == nil {
			t.Fatalf("inspect plan: %s, %v", rec.Body.String(), err)
		}
		p := result.Plan
		if p.Name != "wizard" || len(p.Gaps) == 0 || len(result.Arr) != 1 || result.Arr[0].Raw != nil {
			t.Fatalf("preview: %+v", result)
		}
		quality := false
		for _, step := range p.Steps {
			if step.Kind == migration.KindQuality && step.Payload != nil && len(step.Payload.Config) > 0 && len(step.Payload.Specifications) > 0 {
				quality = true
			}
		}
		if !quality {
			t.Fatal("preview lost the raw profile data before freezing quality")
		}
		list, err := migration.List(context.Background(), se.st.Reader())
		if err != nil {
			t.Fatal(err)
		}
		if save {
			if p.ID == 0 || len(list) != 1 || list[0].ID != p.ID {
				t.Fatalf("saved plan: %+v, list %+v", p, list)
			}
			stored, inv, err := migration.Load(context.Background(), se.st.Reader(), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(MigratePlan{Plan: stored, Inventory: inv})
			if err != nil || strings.Contains(string(b), migrateArrKey) || inv.Arr[0].Raw != nil {
				t.Fatalf("stored plan contains credentials/raw bodies, or cannot marshal: %v", err)
			}
		} else if p.ID != 0 || len(list) != 0 {
			t.Fatalf("preview was persisted: id %d, list %+v", p.ID, list)
		}
		if se.generation() != gen || len(p.Checkpoints) != 0 {
			t.Fatal("inspect changed settings or took a checkpoint")
		}
	}
	se.want(se.migrateCall("POST", "/inspect", MigrateInspectRequest{Sources: sources, Keys: keys,
		Options: &migration.PlanOptions{Providers: []migration.ProviderChoice{{Kind: "invalid"}}}}),
		http.StatusBadRequest, "invalid plan options")
	if strings.Contains(strings.Join(se.bodies, "\n")+se.logs.String(), migrateArrKey) {
		t.Fatal("inspect response or log contains a source key")
	}
}

type migrationConfirmingSettings struct {
	SettingsAdmin
	asked bool
}

func (s *migrationConfirmingSettings) SaveAndApply(ctx context.Context, req settings.SaveRequest) (*settings.Snapshot, error) {
	if !s.asked {
		s.asked = true
		return nil, &settings.ConfirmationError{Confirmation: &settings.Confirmation{Token: "migration-confirm-token",
			Effects: []settings.ConfirmationEffect{{Version: "movies", Effect: "Expose the migrated view"}}}}
	}
	if req.ConfirmationToken != "migration-confirm-token" {
		return nil, errors.New("unexpected migration confirmation token")
	}
	req.ConfirmationToken = ""
	return s.SettingsAdmin.SaveAndApply(ctx, req)
}

func TestMigrateApplyConfirmationRoundTrip(t *testing.T) {
	t.Parallel()
	se := newQualityEnv(t)
	radarr := fakeRadarr(t)
	se.web.settings = &migrationConfirmingSettings{SettingsAdmin: se.coord}
	se.web.migration = &Migration{
		Checkpoint: func(ctx context.Context) (migration.Checkpoint, error) {
			snap, err := se.coord.Snapshot(ctx)
			return migration.Checkpoint{At: testNow, Backup: "/backups/confirm", Generation: snap.Generation}, err
		},
		Dial: migration.NewDialer(arr.Options{Timeout: 5 * time.Second, MinInterval: time.Millisecond, Retries: 1, Backoff: time.Millisecond}),
	}
	sources := migration.Sources{Arr: []migration.ArrSource{{Name: "radarr_uhd", Flavour: "radarr", URL: radarr.URL, Key: "RADARR_UHD_KEY"}}}
	options := migration.PlanOptions{Name: "confirmation", Providers: []migration.ProviderChoice{{Kind: "torbox", Key: "tb-migrated", Secret: "TORBOX_KEY"}}}
	rec := se.migrateCall("POST", "/plans", MigratePlanRequest{Sources: &sources,
		Keys: map[string]string{"RADARR_UHD_KEY": migrateArrKey}, Options: options})
	se.want(rec, http.StatusCreated, "create plan")
	var view MigratePlan
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	step := ""
	for _, s := range view.Plan.Steps {
		if s.Kind == migration.KindProvider {
			step = s.ID
			break
		}
	}
	if step == "" {
		t.Fatal("provider step missing")
	}
	path := "/plans/" + itoa(view.Plan.ID) + "/apply"
	rec = se.migrateCall("POST", path, MigrateApplyRequest{Steps: []string{step}})
	se.want(rec, http.StatusOK, "apply requests confirmation")
	var prompt migration.Plan
	if err := json.Unmarshal(rec.Body.Bytes(), &prompt); err != nil || prompt.Step(step).Status != migration.StatusConfirmation ||
		prompt.Step(step).Confirmation == nil || prompt.Step(step).Confirmation.Token != "migration-confirm-token" ||
		len(prompt.Step(step).Confirmation.Effects) != 1 {
		t.Fatalf("confirmation response %+v, %v", prompt.Step(step), err)
	}
	token := prompt.Step(step).Confirmation.Token
	rec = se.migrateCall("POST", path, MigrateApplyRequest{Steps: []string{step}, Confirm: []string{step}, ConfirmationToken: "stale-token"})
	se.want(rec, http.StatusBadRequest, "stale confirmation token")
	rec = se.migrateCall("POST", path, MigrateApplyRequest{Steps: []string{step}, Confirm: []string{step},
		ConfirmationToken: token})
	se.want(rec, http.StatusOK, "confirm and apply")
	var applied migration.Plan
	if err := json.Unmarshal(rec.Body.Bytes(), &applied); err != nil || applied.Step(step).Status != migration.StatusDone {
		t.Fatalf("confirmed plan %+v: %v", applied.Step(step), err)
	}
}
