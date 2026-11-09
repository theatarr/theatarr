package main

import (
	"encoding/json"
	"errors"
	"flag"
	"os"

	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

func init() {
	register(command{
		name:    "sonarr-parse",
		summary: "parse release titles with the Sonarr parser port; prints parsedEpisodeInfo JSON",
		run:     runSonarrParse,
	})
}

// runSonarrParse prints one JSON line per title: the parsedEpisodeInfo Sonarr's GET /api/v3/parse would return, or
// null when the title does not parse. With -path the argument is a file path (Parser.ParsePath). -sonarr v5 parses
// with Sonarr v5's parser and, as v5's parse endpoint does, falls back to ParseSeasonTitle.
func runSonarrParse(args []string) error {
	fs := flag.NewFlagSet("sonarr-parse", flag.ContinueOnError)
	path := fs.Bool("path", false, "treat the arguments as file paths (ParsePath)")
	ver := fs.String("sonarr", "v4", "the Sonarr parser: v4, v4plus or v5")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: theatarr sonarr-parse [-path] [-sonarr v4|v4plus|v5] '<title>' [more titles]")
	}
	v, err := sonarr.ParseVersion(*ver)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	for _, t := range fs.Args() {
		var r *sonarr.ParsedEpisodeInfo
		if *path {
			r = v.ParsePath(t)
		} else {
			r = v.ParseTitle(t)
			if r == nil {
				r = v.ParseSeasonTitle(t)
			}
		}
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}
