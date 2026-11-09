// Port of Mediarium internal/indexers/newznab.go (Result, Protocol) @ f7d1e26 (github.com/rdborg/Mediarium,
// AGPL-3.0, Copyright (C) Ryan Borg and the Mediarium contributors). Only the fields the engine fills are kept;
// Link and Magnet replace the DownloadURL link reference.

package cardigann

import "time"

// Protocol says what a site hands out.
type Protocol string

const (
	ProtocolUsenet  Protocol = "usenet"
	ProtocolTorrent Protocol = "torrent"
)

// Result is one release a definition-based site returned.
type Result struct {
	Title       string
	IndexerName string
	Protocol    Protocol
	// InfoURL is the details page.
	InfoURL     string
	SizeBytes   int64
	PublishDate time.Time
	Categories  []int
	Seeders     int
	// SeedersKnown is true when the site said how many seeders there are, so a 0 in Seeders is a dead torrent
	// and not just a missing number.
	SeedersKnown bool
	Peers        int
	InfoHash     string
	// IMDb is the release's IMDb id ("tt" and digits) when the definition reads one.
	IMDb string
	// Link is the site's download link, resolved through Manager.Download (it may need the session's cookies and
	// the definition's download rules); empty when the site gave only a magnet. Magnet is a magnet link.
	Link   string
	Magnet string
}

// StatusError is an HTTP error status a site answered a search with. (Mediarium's carries the Retry-After for its
// own back-off; here the gateway backs the indexer off.)
type StatusError struct {
	Status int
	msg    string
}

func (e *StatusError) Error() string { return e.msg }
