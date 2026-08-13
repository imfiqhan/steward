package steward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Cache is the small byte cache backing menu/settings lookups. The default is
// the in-process MemoryCache; supply a Redis-backed implementation for
// multi-instance deployments.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
}

// Storage persists uploaded files for File/Image form fields. The default
// LocalStorage writes below Config.UploadDir; an S3-compatible backend is one
// small implementation of this interface away.
type Storage interface {
	Put(ctx context.Context, name string, r io.Reader, size int64, contentType string) (url string, err error)
	Delete(ctx context.Context, name string) error
	URL(name string) string
}

// Mail is one outbound message.
type Mail struct {
	To      []string
	Subject string
	HTML    string
	Text    string
}

// Mailer sends mail; configuring one enables the password-reset flow.
type Mailer interface {
	Send(ctx context.Context, m Mail) error
}

// JobInfo describes a scheduled job for the admin page.
type JobInfo struct {
	Name    string
	Spec    string
	LastRun time.Time
	LastErr string
}

// Scheduler runs recurring jobs. It is deliberately not wired into the
// Admin: run it in a worker process (see steward.CLI's worker command) so
// the panel and background jobs deploy and scale independently.
type Scheduler interface {
	Add(cronSpec, name string, fn func(context.Context) error) error
	Jobs() []JobInfo
}

// SearchDoc and SearchHit reserve the full-text search seam; grid quick
// search uses SQL LIKE until a Searcher ships.
type SearchDoc struct {
	ID     string
	Type   string
	Fields map[string]string
}

type SearchHit struct {
	ID    string
	Type  string
	Score float64
}

// Searcher indexes and queries documents.
type Searcher interface {
	Index(ctx context.Context, doc SearchDoc) error
	Query(ctx context.Context, q string, limit int) ([]SearchHit, error)
}

// MemoryCache is a TTL map cache; the zero value is not usable, call
// NewMemoryCache.
type MemoryCache struct {
	mu    sync.RWMutex
	items map[string]memoryItem
}

type memoryItem struct {
	val []byte
	exp time.Time
}

// NewMemoryCache returns an empty in-process cache.
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{items: map[string]memoryItem{}}
}

// Get implements Cache.
func (c *MemoryCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.RLock()
	it, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || (!it.exp.IsZero() && time.Now().After(it.exp)) {
		return nil, false, nil
	}
	return it.val, true, nil
}

// Set implements Cache; ttl <= 0 stores without expiry.
func (c *MemoryCache) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	c.mu.Lock()
	c.items[key] = memoryItem{val: append([]byte(nil), val...), exp: exp}
	c.mu.Unlock()
	return nil
}

// Delete implements Cache.
func (c *MemoryCache) Delete(_ context.Context, keys ...string) error {
	c.mu.Lock()
	for _, k := range keys {
		delete(c.items, k)
	}
	c.mu.Unlock()
	return nil
}

// StorageURL turns a stored value into something a browser can fetch.
//
// File and Image form fields store a storage-relative path, not a URL, so a
// column or field that puts one straight into an href or a src produces a
// reference the browser resolves against the current page. Anything already
// absolute is returned untouched, since an app may store full URLs instead.
//
// Use this inside a Display or Link function, where the URL is yours to compute:
//
//	g.Column("File").Link(func(m *Magazine) string { return app.StorageURL(m.File) })
func (a *Admin) StorageURL(name string) string {
	if name == "" || absoluteRef(name) {
		return name
	}
	return a.cfg.Storage.URL(name)
}

// resolvedRef carries a stored value alongside its fetchable URL, so a presenter
// can put one in an href and still show the other. The render substitutes it for
// the raw value on a column or field marked storageRef.
type resolvedRef struct{ raw, url string }

// refParts splits a presenter's value into what to show and what to fetch. They
// differ only for a storageRef helper; everything else gets the same string
// twice, so a presenter can use this unconditionally.
func refParts(v any) (raw, href string) {
	if r, ok := v.(resolvedRef); ok {
		return r.raw, r.url
	}
	if v == nil {
		return "", ""
	}
	s := fmt.Sprint(v)
	return s, s
}

// absoluteRef reports whether a stored value is already fetchable as it stands.
// Deliberately a prefix test rather than a URL parse: a filename containing a
// colon parses as having a scheme, and treating that as absolute would leave it
// unresolved.
func absoluteRef(s string) bool {
	return strings.HasPrefix(s, "/") || // rooted at the host, and protocol-relative
		strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "data:")
}

// LocalStorage stores uploads on the local filesystem below Dir and serves
// them under BaseURL (the admin wires BaseURL to {prefix}/_uploads).
type LocalStorage struct {
	Dir     string
	BaseURL string
}

// ErrUnsafePath rejects names that escape the storage root.
var ErrUnsafePath = errors.New("storage: unsafe path")

func (s *LocalStorage) clean(name string) (string, error) {
	name = path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))[1:]
	if name == "" || name == "." || strings.HasPrefix(name, "..") {
		return "", ErrUnsafePath
	}
	return name, nil
}

// Put implements Storage.
func (s *LocalStorage) Put(_ context.Context, name string, r io.Reader, _ int64, _ string) (string, error) {
	name, err := s.clean(name)
	if err != nil {
		return "", err
	}
	full := filepath.Join(s.Dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return s.URL(name), nil
}

// Delete implements Storage; deleting a missing file is not an error.
func (s *LocalStorage) Delete(_ context.Context, name string) error {
	name, err := s.clean(name)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(s.Dir, filepath.FromSlash(name)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// URL implements Storage.
func (s *LocalStorage) URL(name string) string {
	cleaned, err := s.clean(name)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s/%s", strings.TrimRight(s.BaseURL, "/"), urlEscapePath(cleaned))
}

func urlEscapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
