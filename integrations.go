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

// SearchDoc is one indexed record. Type is the resource's slug, so one engine
// can hold every resource in a panel and still answer for one of them.
type SearchDoc struct {
	ID     string
	Type   string
	Fields map[string]string
	// Attributes is Fields in the order Searchable declared them, most
	// important first. A map has no order and JSON sorts its keys, so without
	// this an engine that ranks by attribute ranks alphabetically: a match in
	// SubTitle beat a match in Title, which is not what the declaration says.
	Attributes []string
}

// SearchHit is one match, in the engine's own order.
type SearchHit struct {
	ID    string
	Type  string
	Score float64
}

// Searcher backs quick search and the command palette with a full-text engine
// instead of SQL LIKE. Configure one with Config.Searcher and declare what goes
// in it with Resource.Searchable.
//
// Index and Delete take batches because a backfill is the normal way an index
// is first filled, and one round trip per row over a table of any size is not a
// backfill anyone will finish.
//
// Query returns matching IDs rather than rows. The rows are then read through
// the repository, so filters, sorts, and the row scope a policy applies all
// still hold — an engine that knew how to return rows directly would be an
// engine that had to be taught the panel's authorization, which is not a thing
// to duplicate.
type Searcher interface {
	Index(ctx context.Context, docs ...SearchDoc) error
	Delete(ctx context.Context, typ string, ids ...string) error
	Query(ctx context.Context, typ, query string, limit int) ([]SearchHit, error)
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
	// A backend that can sign hands out a link good for a while, so the bucket
	// behind it never has to be public. Signing is a computation rather than a
	// round trip, which is why this can stay context-free.
	return a.DiskURL(a.cfg.DefaultDisk, name)
}

// StorageURLOn is StorageURL against a named disk.
func (a *Admin) StorageURLOn(disk, name string) string { return a.DiskURL(disk, name) }

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
	// name is the disk this backend serves, filled in by the Admin. It is part
	// of what a signature covers, so a link to one disk cannot be pointed at
	// another holding a file of the same name.
	name string
	// SigningKey signs time-limited URLs. The Admin sets it from Config.SecretKey
	// when it wires up the backend; left empty, SignedURL reports ErrNotSigned
	// and links fall back to the authenticated route.
	SigningKey []byte
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
