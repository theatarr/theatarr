package settings

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Git archives contain one top-level directory. Only regular files under it are extracted, with bounded size;
// links, traversal and oversized archives are refused before their contents can become settings.
func unpackQualityArchive(data []byte) (root string, resultErr error) {
	if len(data) > 64<<20 {
		return "", errors.New("settings: archive exceeds 64 MiB")
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("settings: expected tar.gz git archive")
	}
	defer gz.Close()
	dir, err := os.MkdirTemp("", "theatarr-quality-archive-")
	if err != nil {
		return "", err
	}
	defer func() {
		if resultErr != nil {
			os.RemoveAll(dir)
		}
	}()
	tr := tar.NewReader(io.LimitReader(gz, 128<<20))
	prefix := ""
	count := 0
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		// git archive (and GitHub's codeload tarballs) start with a pax global header carrying the commit id.
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		count++
		if count > 10000 {
			return "", errors.New("settings: too many archive entries")
		}
		total += h.Size + 512
		if h.Size < 0 || total > 120<<20 {
			return "", errors.New("settings: expanded archive exceeds limit")
		}
		name := strings.TrimSuffix(h.Name, "/")
		clean := path.Clean(name)
		parts := strings.Split(clean, "/")
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(name, "\\") || clean != name {
			return "", errors.New("settings: unsafe archive path")
		}
		if prefix == "" {
			prefix = parts[0]
		}
		if prefix != parts[0] {
			return "", errors.New("settings: archive must have one root directory")
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg || len(parts) < 2 || h.Size > 8<<20 {
			return "", errors.New("settings: unsafe archive entry")
		}
		relative := strings.Join(parts[1:], "/")
		if strings.Contains(relative, "/.git/") || strings.HasPrefix(relative, ".git/") {
			continue
		}
		// Only the JSON tree is needed by the converter.
		if !strings.HasSuffix(relative, ".json") {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return "", err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		_, copyErr := io.CopyN(f, tr, h.Size)
		closeErr := f.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return "", err
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "metadata.json")); err != nil {
		return "", errors.New("settings: archive has no TRaSH metadata.json")
	}
	return dir, nil
}
