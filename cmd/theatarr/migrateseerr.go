package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/migration"
)

// seerrIDs is theatarr migrate seerr-ids: Seerr is read with GET only, the views through the running service with
// the admin API key, and nothing is written anywhere (docs/design/arrview.md "Seerr's stored ids").
func (f *migrationFlags) seerrIDs(ctx context.Context, out io.Writer) error {
	if *f.seerr == "" {
		return errors.New("seerr-ids needs -seerr URL,KEYNAME")
	}
	src, err := f.sources()
	if err != nil {
		return err
	}
	key, ok := envKeys(src.Seerr.Key)
	if !ok {
		return fmt.Errorf("seerr: key %s is not set", src.Seerr.Key)
	}
	views := map[string]string{"radarr": *f.radarrView, "sonarr": *f.sonarrView}
	for fl, p := range views {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("-%s-view %q: want a path such as /%s", fl, p, fl)
		}
		views[fl] = strings.TrimRight(p, "/")
	}
	g, err := migrationDialer(migration.ServiceSeerr, src.Seerr.URL, key)
	if err != nil {
		return err
	}
	c, err := f.serviceClient(ctx)
	if err != nil {
		return err
	}
	defer c.close()
	rep, err := migration.CheckSeerrIDs(ctx, g, views, c.viewTitle)
	if err != nil {
		return err
	}
	if *f.asJSON {
		if err := writeIndented(out, rep); err != nil {
			return err
		}
	} else {
		printSeerrIDs(out, rep)
	}
	if n := rep.Summary.WrongTitle; n > 0 {
		return fmt.Errorf("%d stored Seerr ids name a different title in Theatarr's views", n)
	}
	return nil
}

// viewTitle asks a view for one movie or series id, as Seerr's availability sync does (GET movie/{id},
// series/{id}); nil when the view answers 404.
func (c *theatarrClient) viewTitle(ctx context.Context, flavour, path string, id int64) (*migration.ViewTitle, error) {
	res := "movie"
	if flavour == "sonarr" {
		res = "series"
	}
	status, b, err := c.do(ctx, http.MethodGet, path+"/api/v3/"+res+"/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusNotFound:
		return nil, nil
	case status/100 != 2:
		return nil, fmt.Errorf("theatarr: %d %s", status, http.StatusText(status))
	}
	var vt migration.ViewTitle
	if err := json.Unmarshal(b, &vt); err != nil {
		return nil, fmt.Errorf("theatarr: GET %s %s/%d: %w", path, res, id, err)
	}
	return &vt, nil
}

func printSeerrIDs(out io.Writer, rep *migration.SeerrIDReport) {
	s := rep.Summary
	fmt.Fprintf(out, "Seerr media %d, stored ids %d (radarr view %s, sonarr view %s)\n", s.Media, s.Checked,
		rep.Views["radarr"], rep.Views["sonarr"])
	fmt.Fprintf(out, "  ok %d, wrong title %d, unresolved %d, unverifiable %d", s.OK, s.WrongTitle, s.Unresolved,
		s.Unverifiable)
	if s.NoServer > 0 {
		fmt.Fprintf(out, "; %d name a server Seerr no longer has", s.NoServer)
	}
	fmt.Fprintln(out)
	for _, res := range []string{migration.SeerrIDWrongTitle, migration.SeerrIDUnresolved, migration.SeerrIDUnverifiable} {
		head := false
		for _, it := range rep.Items {
			if it.Result != res {
				continue
			}
			if !head {
				fmt.Fprintf(out, "%s:\n", strings.ReplaceAll(res, "_", " "))
				head = true
			}
			ids := fmt.Sprintf("tmdb %d", it.TmdbID)
			if it.TvdbID > 0 {
				ids += fmt.Sprintf(", tvdb %d", it.TvdbID)
			}
			server := "no server"
			if !it.NoServer {
				server = fmt.Sprintf("%q", it.Server)
			}
			fmt.Fprintf(out, "  %s %s (Seerr media %d): %s %d on %s", it.MediaType, ids, it.MediaID, it.Field, it.ID, server)
			if vt := it.ViewTitle; vt != nil {
				var vids []string
				if vt.TmdbID > 0 {
					vids = append(vids, fmt.Sprintf("tmdb %d", vt.TmdbID))
				}
				if vt.TvdbID > 0 {
					vids = append(vids, fmt.Sprintf("tvdb %d", vt.TvdbID))
				}
				fmt.Fprintf(out, ": %s answers %q (%s)", it.View, vt.Title, strings.Join(vids, ", "))
			} else {
				fmt.Fprintf(out, ": %s answers 404", it.View)
			}
			fmt.Fprintln(out)
		}
	}
}
