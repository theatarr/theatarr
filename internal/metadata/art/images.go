package art

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// FileRE is the only file name the proxy fetches: a TMDb image path without its slash. Nothing else is fetchable,
// so /img is not an open proxy.
var FileRE = regexp.MustCompile(`^[A-Za-z0-9]{10,40}\.(jpg|png)$`)

// Doer sends a request; in the service, the gateway with the tmdb-images target.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// ErrBadImage is returned for a size or file name the proxy refuses.
var ErrBadImage = errors.New("art: not a TMDb image path or size")

// MaxImageBytes caps one fetched image.
const MaxImageBytes = 20 << 20

// ImagesConfig is what NewImages needs.
type ImagesConfig struct {
	Settings policy.ImageSettings
	// BaseURL is metadata.sources.tmdb-images' URL, e.g. https://image.tmdb.org/t/p.
	BaseURL string
	Doer    Doer
	Logger  *slog.Logger
	// Timeout bounds one fetch. Default 60s.
	Timeout time.Duration
}

// Images is the disk cache of TMDb images: {dir}/{size}/{first two chars}/{file}. TMDb never changes the content
// behind a path, so a cached file stays valid; files are evicted least recently used (by modification time, which a
// hit refreshes at most hourly) once the directory passes max_bytes. Fetches are single-flight per file.
type Images struct {
	set     policy.ImageSettings
	base    string
	doer    Doer
	log     *slog.Logger
	timeout time.Duration

	mu     sync.Mutex
	flight map[string]*call
	total  int64 // bytes in the cache; -1 until the first scan
	evict  sync.Mutex
}

type call struct {
	done chan struct{}
	err  error
}

// NewImages returns the cache; the directory is created on first use.
func NewImages(cfg ImagesConfig) (*Images, error) {
	if cfg.Doer == nil {
		return nil, errors.New("art: images: no Doer (fetches go through the gateway)")
	}
	if cfg.BaseURL == "" || !strings.HasPrefix(cfg.BaseURL, "https://") && !strings.HasPrefix(cfg.BaseURL, "http://") {
		return nil, fmt.Errorf("art: images: bad base URL %q", cfg.BaseURL)
	}
	if cfg.Settings.Dir == "" {
		return nil, errors.New("art: images: no cache directory")
	}
	if cfg.Settings.MaxBytes <= 0 {
		cfg.Settings.MaxBytes = policy.DefaultImageMaxBytes
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &Images{set: cfg.Settings, base: strings.TrimRight(cfg.BaseURL, "/"), doer: cfg.Doer,
		log: cfg.Logger.With("component", "images"), timeout: cfg.Timeout, flight: map[string]*call{}, total: -1}, nil
}

// Settings returns the cache settings.
func (im *Images) Settings() policy.ImageSettings { return im.set }

// Valid reports whether size and file are an image the proxy serves.
func (im *Images) Valid(size, file string) bool {
	return im.set.ValidImageSize(size) && FileRE.MatchString(file)
}

func (im *Images) local(size, file string) string {
	return filepath.Join(im.set.Dir, size, file[:2], file)
}

// Get returns the cached file's path, fetching it first if needed.
func (im *Images) Get(ctx context.Context, size, file string) (string, error) {
	if !im.Valid(size, file) {
		return "", ErrBadImage
	}
	p := im.local(size, file)
	if st, err := os.Stat(p); err == nil {
		if time.Since(st.ModTime()) > time.Hour {
			now := time.Now()
			_ = os.Chtimes(p, now, now) // recently used; best effort
		}
		return p, nil
	}
	return p, im.fetchOnce(ctx, size, file, p)
}

// Prewarm fetches path (a TMDb path such as '/abc.jpg') at size unless it is cached, and reports whether it fetched.
func (im *Images) Prewarm(ctx context.Context, size, path string) (bool, error) {
	file := strings.TrimPrefix(path, "/")
	if !im.Valid(size, file) {
		return false, nil
	}
	p := im.local(size, file)
	if _, err := os.Stat(p); err == nil {
		return false, nil
	}
	if err := im.fetchOnce(ctx, size, file, p); err != nil {
		return false, err
	}
	return true, nil
}

func (im *Images) fetchOnce(ctx context.Context, size, file, dest string) error {
	key := size + "/" + file
	im.mu.Lock()
	if c, ok := im.flight[key]; ok {
		im.mu.Unlock()
		select {
		case <-c.done:
			return c.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c := &call{done: make(chan struct{})}
	im.flight[key] = c
	im.mu.Unlock()

	// The fetch outlives the request that started it, so the others waiting on it still get the file.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), im.timeout)
	c.err = im.fetch(fctx, size, file, dest)
	cancel()
	im.mu.Lock()
	delete(im.flight, key)
	im.mu.Unlock()
	close(c.done)
	return c.err
}

func (im *Images) fetch(ctx context.Context, size, file, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, im.base+"/"+size+"/"+file, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "image/jpeg, image/png")
	resp, err := im.doer.Do(req)
	if err != nil {
		return fmt.Errorf("art: image %s/%s: %w", size, file, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("art: image %s/%s: HTTP %d", size, file, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "image/jpeg") &&
		!strings.HasPrefix(ct, "image/png") {
		return fmt.Errorf("art: image %s/%s: content type %q", size, file, ct)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".fetch-*")
	if err != nil {
		return err
	}
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, MaxImageBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > MaxImageBytes {
		err = fmt.Errorf("art: image %s/%s: over %d bytes", size, file, MaxImageBytes)
	}
	if err == nil && n == 0 {
		err = fmt.Errorf("art: image %s/%s: empty", size, file)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dest)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	im.added(n)
	return nil
}

// added counts new bytes and evicts when the cache passes max_bytes.
func (im *Images) added(n int64) {
	im.mu.Lock()
	if im.total < 0 {
		im.mu.Unlock()
		total, _ := im.scan()
		im.mu.Lock()
		im.total = total // includes the new file
	} else {
		im.total += n
	}
	over := im.total > im.set.MaxBytes
	im.mu.Unlock()
	if over {
		im.Evict()
	}
}

type cached struct {
	path string
	size int64
	mod  time.Time
}

func (im *Images) scan() (int64, []cached) {
	var total int64
	var files []cached
	filepath.WalkDir(im.set.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".fetch-") && time.Since(info.ModTime()) > time.Hour {
			os.Remove(p) // left by a crash mid-fetch
			return nil
		}
		total += info.Size()
		files = append(files, cached{path: p, size: info.Size(), mod: info.ModTime()})
		return nil
	})
	return total, files
}

// Evict removes the least recently used files until the cache is at 90% of max_bytes, and returns how many bytes
// it freed.
func (im *Images) Evict() int64 {
	im.evict.Lock()
	defer im.evict.Unlock()
	total, files := im.scan()
	target := im.set.MaxBytes / 10 * 9
	var freed int64
	if total > im.set.MaxBytes {
		sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
		for _, f := range files {
			if total-freed <= target {
				break
			}
			if os.Remove(f.path) == nil {
				freed += f.size
			}
		}
		im.log.Info("image cache evicted", "freed_bytes", freed, "bytes", total-freed, "max_bytes", im.set.MaxBytes)
	}
	im.mu.Lock()
	im.total = total - freed
	im.mu.Unlock()
	return freed
}

// Size returns the bytes in the cache (scanning it on first use).
func (im *Images) Size() int64 {
	im.mu.Lock()
	t := im.total
	im.mu.Unlock()
	if t >= 0 {
		return t
	}
	t, _ = im.scan()
	im.mu.Lock()
	im.total = t
	im.mu.Unlock()
	return t
}

func contentType(file string) string {
	if strings.HasSuffix(file, ".png") {
		return "image/png"
	}
	return "image/jpeg"
}
