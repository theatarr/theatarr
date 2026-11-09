package decide

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
)

// ArrSnapshot is what a from_arr profile reads from an arr instance: the raw GET /api/v3 bodies. Configuration
// bodies are optional; without them the arr defaults apply (propers preferAndUpgrade, no maximum size, flags
// not preferred, episode titles Always and completed download handling enabled).
type ArrSnapshot struct {
	Flavour              format.Flavour
	QualityProfile       []byte // qualityprofile
	CustomFormat         []byte // customformat
	QualityDefinition    []byte // qualitydefinition
	MediaManagement      []byte // config/mediamanagement
	IndexerConfig        []byte // config/indexer
	DownloadClientConfig []byte // config/downloadclient
}

// ArrSource gives the snapshot of an arr instance by name (an import.arr key).
type ArrSource interface {
	Snapshot(instance string) (*ArrSnapshot, error)
}

// ArrSourceFunc adapts a function to ArrSource.
type ArrSourceFunc func(instance string) (*ArrSnapshot, error)

// Snapshot implements ArrSource.
func (f ArrSourceFunc) Snapshot(instance string) (*ArrSnapshot, error) { return f(instance) }

// SnapshotDir reads snapshots in the corpus layout:
// <dir>/<instance>/snapshot/<endpoint>.json, with the flavour from <dir>/instances.json. config/mediamanagement and
// config/indexer and config/downloadclient are read from config-mediamanagement.json, config-indexer.json and
// config-downloadclient.json when present.
type SnapshotDir string

// Snapshot implements ArrSource.
func (d SnapshotDir) Snapshot(instance string) (*ArrSnapshot, error) {
	b, err := os.ReadFile(filepath.Join(string(d), "instances.json"))
	if err != nil {
		return nil, err
	}
	var insts []struct{ Name, Flavour string }
	if err := json.Unmarshal(b, &insts); err != nil {
		return nil, fmt.Errorf("instances.json: %w", err)
	}
	s := &ArrSnapshot{}
	for _, in := range insts {
		if in.Name == instance {
			s.Flavour = format.Flavour(in.Flavour)
		}
	}
	if s.Flavour == "" {
		return nil, fmt.Errorf("instance %q is not in %s", instance, filepath.Join(string(d), "instances.json"))
	}
	dir := filepath.Join(string(d), instance, "snapshot")
	read := func(name string, optional bool) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if optional && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return b, err
	}
	for _, f := range []struct {
		name     string
		dst      *[]byte
		optional bool
	}{
		{"qualityprofile", &s.QualityProfile, false},
		{"customformat", &s.CustomFormat, false},
		{"qualitydefinition", &s.QualityDefinition, false},
		{"config-mediamanagement", &s.MediaManagement, true},
		{"config-indexer", &s.IndexerConfig, true},
		{"config-downloadclient", &s.DownloadClientConfig, true},
	} {
		if *f.dst, err = read(f.name, f.optional); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// arrConfig reads the engine settings from the optional config bodies.
func arrConfig(s *ArrSnapshot) (profile.Config, error) {
	c := profile.Config{PreferredProtocol: profile.ProtocolTorrent}
	if len(s.DownloadClientConfig) > 0 {
		var dc struct {
			EnableCompletedDownloadHandling *bool `json:"enableCompletedDownloadHandling"`
		}
		if err := json.Unmarshal(s.DownloadClientConfig, &dc); err != nil {
			return c, fmt.Errorf("config/downloadclient: %w", err)
		}
		c.CompletedDownloadHandlingDisabled = dc.EnableCompletedDownloadHandling != nil && !*dc.EnableCompletedDownloadHandling
	}
	if len(s.MediaManagement) > 0 {
		var mm struct {
			DownloadPropersAndRepacks string `json:"downloadPropersAndRepacks"`
			EpisodeTitleRequired      string `json:"episodeTitleRequired"`
		}
		if err := json.Unmarshal(s.MediaManagement, &mm); err != nil {
			return c, fmt.Errorf("config/mediamanagement: %w", err)
		}
		c.EpisodeTitleRequired = mm.EpisodeTitleRequired
		if mm.DownloadPropersAndRepacks != "" {
			v, err := profile.ParseProperDownloadTypes(mm.DownloadPropersAndRepacks)
			if err != nil {
				return c, fmt.Errorf("config/mediamanagement: %w", err)
			}
			c.DownloadPropersAndRepacks = v
		}
	}
	if len(s.IndexerConfig) > 0 {
		var ic struct {
			MaximumSize        int  `json:"maximumSize"`
			PreferIndexerFlags bool `json:"preferIndexerFlags"`
		}
		if err := json.Unmarshal(s.IndexerConfig, &ic); err != nil {
			return c, fmt.Errorf("config/indexer: %w", err)
		}
		c.MaximumSize, c.PreferIndexerFlags = ic.MaximumSize, ic.PreferIndexerFlags
	}
	return c, nil
}
