package settings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// localTrashCommit reads both regular checkouts and worktrees, including packed refs, without invoking Git.
func localTrashCommit(root string) (string, bool, error) {
	dir := filepath.Join(root, ".git")
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		b, err := os.ReadFile(dir)
		if err != nil {
			return "", true, err
		}
		if !strings.HasPrefix(string(b), "gitdir: ") {
			return "", true, errors.New("settings: invalid TRaSH git directory")
		}
		dir = strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir: "))
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "HEAD"))
	if err != nil {
		return "", true, err
	}
	head := strings.TrimSpace(string(b))
	if commitRE.MatchString(head) {
		return head, true, nil
	}
	ref := strings.TrimPrefix(head, "ref: ")
	if ref == head || !strings.HasPrefix(ref, "refs/") || strings.Contains(ref, "..") || strings.ContainsAny(ref, "\\\r\n") {
		return "", true, errors.New("settings: invalid TRaSH git HEAD")
	}
	common := dir
	if b, err := os.ReadFile(filepath.Join(dir, "commondir")); err == nil {
		common = strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(dir, common)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", true, err
	}
	if b, err := os.ReadFile(filepath.Join(common, filepath.FromSlash(ref))); err == nil && commitRE.MatchString(strings.TrimSpace(string(b))) {
		return strings.TrimSpace(string(b)), true, nil
	}
	if b, err := os.ReadFile(filepath.Join(common, "packed-refs")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[1] == ref && commitRE.MatchString(fields[0]) {
				return fields[0], true, nil
			}
		}
	}
	return "", true, errors.New("settings: cannot resolve TRaSH git commit")
}
