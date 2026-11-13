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
// null when the title does not parse. With -path the argument is a file path (Parser.ParsePath). The parser is the one
// every series library uses (v4plus).
func runSonarrParse(args []string) error {
	fs := flag.NewFlagSet("sonarr-parse", flag.ContinueOnError)
	path := fs.Bool("path", false, "treat the arguments as file paths (ParsePath)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: theatarr sonarr-parse [-path] '<title>' [more titles]")
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	for _, t := range fs.Args() {
		var r *sonarr.ParsedEpisodeInfo
		if *path {
			r = sonarr.ParsePath(t)
		} else {
			r = sonarr.ParseTitle(t)
		}
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}
