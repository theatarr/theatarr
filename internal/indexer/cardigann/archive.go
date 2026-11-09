// Port of Mediarium internal/indexers/definitions.go (extractDefinitions and its limits) @ f7d1e26
// (github.com/rdborg/Mediarium, AGPL-3.0, Copyright (C) Ryan Borg and the Mediarium contributors). Changes are
// listed in the package doc.

package cardigann

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// SupportedSchema is the newest definition schema (definitions/vN) this engine runs.
const SupportedSchema = 11

const (
	// maxArchiveBytes caps one download of the definitions repository.
	maxArchiveBytes = 64 << 20
	// maxDefinitionBytes caps one definition file.
	maxDefinitionBytes = 2 << 20
	// maxDefinitionsTotal caps all the definition files taken from one download. The real list is a few MB.
	maxDefinitionsTotal = 48 << 20
	// maxArchiveExpanded caps how much of a .tar.gz is unpacked while looking for definitions (the real archive is a
	// few tens of MB unpacked).
	maxArchiveExpanded = 512 << 20
)

// MaxArchiveBytes is the largest definitions archive a fetch may return.
const MaxArchiveBytes = maxArchiveBytes

var (
	definitionFileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	versionDirRe     = regexp.MustCompile(`(?:^|/)definitions/v(\d+)/([^/]+)\.ya?ml$`)
)

// ExtractDefinitions reads definitions/vN/*.yml out of a .tar.gz or .zip archive of the definitions repository and
// keeps the newest schema version not above maxSchema. It returns the files by base name and the version taken. An
// archive with no version at or below maxSchema is refused: newer schemas may use features this engine does not
// know.
func ExtractDefinitions(archive []byte, maxSchema int) (map[string][]byte, int, error) {
	byVersion := map[int]map[string][]byte{}
	var kept int64 // bytes of definitions held so far: a hostile archive cannot fill memory with matching files
	add := func(name string, r io.Reader, size int64) error {
		m := versionDirRe.FindStringSubmatch(path.Clean(strings.ReplaceAll(name, "\\", "/")))
		if m == nil || size > maxDefinitionBytes || !definitionFileRe.MatchString(m[2]) {
			return nil
		}
		v, _ := strconv.Atoi(m[1])
		data, err := io.ReadAll(io.LimitReader(r, maxDefinitionBytes+1))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if len(data) > maxDefinitionBytes {
			return nil // larger than it said it was: a cut-off copy would be read wrongly, so leave it out
		}
		if kept += int64(len(data)); kept > maxDefinitionsTotal {
			return fmt.Errorf("the definitions are larger than %d MB in total", maxDefinitionsTotal>>20)
		}
		if byVersion[v] == nil {
			byVersion[v] = map[string][]byte{}
		}
		byVersion[v][m[2]] = data
		return nil
	}

	switch {
	case len(archive) > 2 && archive[0] == 0x1f && archive[1] == 0x8b:
		gz, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return nil, 0, fmt.Errorf("open definitions archive: %w", err)
		}
		// a small download can unpack to enormous size (a gzip bomb): stop reading at a generous multiple of the
		// real list
		tr := tar.NewReader(io.LimitReader(gz, maxArchiveExpanded))
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, 0, fmt.Errorf("read definitions archive: %w", err)
			}
			if hdr.Typeflag != tar.TypeReg {
				continue
			}
			if err := add(hdr.Name, tr, hdr.Size); err != nil {
				return nil, 0, err
			}
		}
	case len(archive) > 2 && archive[0] == 'P' && archive[1] == 'K':
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, 0, fmt.Errorf("open definitions archive: %w", err)
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, 0, fmt.Errorf("read %s: %w", f.Name, err)
			}
			err = add(f.Name, rc, int64(f.UncompressedSize64))
			rc.Close()
			if err != nil {
				return nil, 0, err
			}
		}
	default:
		return nil, 0, fmt.Errorf("the download is not a .tar.gz or .zip file")
	}

	best := -1
	for v := range byVersion {
		if v <= maxSchema && v > best {
			best = v
		}
	}
	if best < 0 {
		return nil, 0, fmt.Errorf("the download has no definitions of schema v%d or older", maxSchema)
	}
	return byVersion[best], best, nil
}
