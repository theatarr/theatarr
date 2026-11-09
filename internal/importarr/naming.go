package importarr

import (
	"context"
	"encoding/json"
)

func (w *wtx) naming(ctx context.Context, instance string, snapshot map[string]json.RawMessage) error {
	body := snapshot["config/naming"]
	if len(body) == 0 {
		return nil
	}
	var defs []struct {
		Title   string `json:"title"`
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	}
	if err := json.Unmarshal(snapshot["qualitydefinition"], &defs); err != nil {
		return err
	}
	titles := map[string]string{}
	for _, d := range defs {
		if d.Title != "" && d.Title != d.Quality.Name {
			titles[d.Quality.Name] = d.Title
		}
	}
	qb, err := json.Marshal(titles)
	if err != nil {
		return err
	}
	_, err = w.tx.ExecContext(ctx, `INSERT INTO library_naming (instance, naming, qualities, fetched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (instance) DO UPDATE SET naming = excluded.naming, qualities = excluded.qualities,
		fetched_at = excluded.fetched_at`, instance, string(body), string(qb), w.now)
	return err
}
