package preflight

import (
	"fmt"
	"io/fs"

	"github.com/theatarr/theatarr/internal/policy"
)

// FS is what the checks read from the filesystem. OSFS is the real one.
type FS interface {
	Stat(path string) (fs.FileInfo, error)
	// Writable is nil when this process may create and remove entries in the directory (access(2) W_OK|X_OK, which
	// also says EROFS for a read-only bind mount).
	Writable(path string) error
	// Readable is nil when this process may list and traverse the directory.
	Readable(path string) error
}

// DirectoryFact is a filesystem observation shared by install preflight and Setup.
// Each caller decides whether this path must be writable and what a missing path means for its report.
type DirectoryFact struct {
	Checked bool
	Problem error
}

// InspectDirectory observes the path without creating or changing anything.
func InspectDirectory(fsys FS, path string, write bool) DirectoryFact {
	if fsys == nil {
		return DirectoryFact{}
	}
	f := DirectoryFact{Checked: true}
	info, err := fsys.Stat(path)
	switch {
	case err != nil:
		f.Problem = fmt.Errorf("%s: %w", path, err)
	case !info.IsDir():
		f.Problem = fmt.Errorf("%s is not a directory", path)
	case !write:
		if err := fsys.Readable(path); err != nil {
			f.Problem = fmt.Errorf("%s is not readable by Theatarr: %w", path, err)
		}
	case write:
		if err := fsys.Writable(path); err != nil {
			f.Problem = fmt.Errorf("%s is not writable by Theatarr: %w", path, err)
		}
	}
	return f
}

// ProviderFact describes presence and effective credentials, without deciding readiness or contacting a provider.
type ProviderFact struct {
	Key        string
	Kind       string
	Order      int
	Credential bool
}

// InspectProviders preserves the requested order.
func InspectProviders(p *policy.Policy, keys []string, credential func(entity, key, env string) bool) []ProviderFact {
	out := make([]ProviderFact, 0, len(keys))
	for _, key := range keys {
		f := ProviderFact{Key: key, Order: 1 << 30}
		if pv := p.Providers[key]; pv != nil {
			f.Kind, f.Order = pv.Kind, pv.Order
			f.Credential = credential != nil && credential("providers", key, pv.APIKeyEnv)
		}
		out = append(out, f)
	}
	return out
}
