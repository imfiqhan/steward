// Package meilistore implements steward.Searcher over Meilisearch.
//
//	搜 := meilistore.New(meilistore.Config{
//	    Host:   "http://127.0.0.1:7700",
//	    APIKey: os.Getenv("MEILI_MASTER_KEY"),
//	})
//	admin, _ := steward.New(steward.Config{Searcher: 搜, ...})
//
// One Meilisearch index per resource, named "{Prefix}{slug}". A resource's slug
// can contain a slash ("auth/users"); Meilisearch index UIDs cannot, so it is
// replaced with a dash.
package meilistore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	steward "github.com/imfiqhan/steward"
)

// Config describes the Meilisearch instance.
type Config struct {
	// Host is the base URL, scheme included ("http://127.0.0.1:7700").
	Host string
	// APIKey is a key with document read and write on the indexes below. A
	// master key works; a scoped key is better.
	APIKey string
	// Prefix namespaces the indexes, so one Meilisearch can serve several
	// panels or environments ("staging-").
	Prefix string
	// HTTPClient is optional; the default carries a 10s timeout, which is long
	// for a search and short enough not to hold a page open.
	HTTPClient *http.Client
}

// Store is the steward.Searcher.
type Store struct {
	host   string
	key    string
	prefix string
	http   *http.Client
	// ensured remembers the indexes already created, so a write does not pay
	// for a settings round trip every time.
	ensured map[string]bool
}

var _ steward.Searcher = (*Store)(nil)

// New returns a Store. It does not talk to Meilisearch: an index is created the
// first time something is written to it, so a panel still boots when the engine
// is briefly down.
func New(cfg Config) (*Store, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("meilistore: Host is required")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Store{
		host:    strings.TrimRight(cfg.Host, "/"),
		key:     cfg.APIKey,
		prefix:  cfg.Prefix,
		http:    client,
		ensured: map[string]bool{},
	}, nil
}

// indexUID is the Meilisearch index for a resource slug.
func (s *Store) indexUID(typ string) string {
	return s.prefix + strings.ReplaceAll(typ, "/", "-")
}

func (s *Store) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.host+path, rdr)
	if err != nil {
		return err
	}
	if s.key != "" {
		req.Header.Set("Authorization", "Bearer "+s.key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("meilistore: %s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ensure creates the index once per process and tells it which attributes are
// searchable, in the order they were declared.
//
// The order is not decoration. Meilisearch ranks a match by which attribute it
// landed in, and its default — every attribute, in the order it first saw them
// — makes that order whatever JSON happened to serialise first, which is
// alphabetical. A match in SubTitle outranked a match in Title until this was
// set.
func (s *Store) ensure(ctx context.Context, uid string, attributes []string) error {
	if s.ensured[uid] {
		return nil
	}
	err := s.do(ctx, http.MethodPost, "/indexes",
		map[string]string{"uid": uid, "primaryKey": "id"}, nil)
	// Already there is the normal case after the first write.
	if err != nil && !strings.Contains(err.Error(), "index_already_exists") {
		return err
	}
	if len(attributes) > 0 {
		fields := make([]string, 0, len(attributes))
		for _, a := range attributes {
			fields = append(fields, attrName(a))
		}
		if err := s.do(ctx, http.MethodPut,
			"/indexes/"+uid+"/settings/searchable-attributes", fields, nil); err != nil {
			return err
		}
	}
	s.ensured[uid] = true
	return nil
}

// attrName is a steward path as a Meilisearch attribute: the alphabet there
// allows letters, digits, - and _, and a path may be "Author.Name".
func attrName(path string) string { return strings.ReplaceAll(path, ".", "_") }

// Index implements steward.Searcher.
func (s *Store) Index(ctx context.Context, docs ...steward.SearchDoc) error {
	byType := map[string][]map[string]any{}
	order := map[string][]string{}
	for _, d := range docs {
		row := map[string]any{"id": meiliID(d.ID)}
		for k, v := range d.Fields {
			row[attrName(k)] = v
		}
		byType[d.Type] = append(byType[d.Type], row)
		if len(d.Attributes) > 0 {
			order[d.Type] = d.Attributes
		}
	}
	for typ, rows := range byType {
		uid := s.indexUID(typ)
		if err := s.ensure(ctx, uid, order[typ]); err != nil {
			return err
		}
		if err := s.do(ctx, http.MethodPost, "/indexes/"+uid+"/documents", rows, nil); err != nil {
			return err
		}
	}
	return nil
}

// Delete implements steward.Searcher.
func (s *Store) Delete(ctx context.Context, typ string, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	uid := s.indexUID(typ)
	if err := s.ensure(ctx, uid, nil); err != nil {
		return err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, meiliID(id))
	}
	return s.do(ctx, http.MethodPost, "/indexes/"+uid+"/documents/delete-batch", out, nil)
}

// Query implements steward.Searcher.
func (s *Store) Query(ctx context.Context, typ, query string, limit int) ([]steward.SearchHit, error) {
	if limit <= 0 {
		limit = 20
	}
	var reply struct {
		Hits []map[string]any `json:"hits"`
	}
	err := s.do(ctx, http.MethodPost, "/indexes/"+s.indexUID(typ)+"/search",
		map[string]any{"q": query, "limit": limit}, &reply)
	if err != nil {
		// An index that was never written to is not an error worth failing a
		// search over: it means nothing has been indexed yet.
		if strings.Contains(err.Error(), "index_not_found") {
			return nil, nil
		}
		return nil, err
	}
	hits := make([]steward.SearchHit, 0, len(reply.Hits))
	for i, h := range reply.Hits {
		id, _ := h["id"].(string)
		if id == "" {
			continue
		}
		hits = append(hits, steward.SearchHit{
			ID: unmeiliID(id), Type: typ,
			// Meilisearch returns its own ranking order rather than a score, so
			// position is the only ranking signal there is.
			Score: float64(len(reply.Hits) - i),
		})
	}
	return hits, nil
}

// meiliID keeps a document ID inside Meilisearch's allowed alphabet
// (A-Z a-z 0-9 - _). Steward keys are usually numeric, but a slug or a UUID
// with other characters would otherwise be refused on write.
func meiliID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "_%02x", r)
		}
	}
	return b.String()
}

// unmeiliID reverses meiliID.
func unmeiliID(id string) string {
	var b strings.Builder
	for i := 0; i < len(id); i++ {
		if id[i] == '_' && i+2 < len(id) {
			var r rune
			if _, err := fmt.Sscanf(id[i+1:i+3], "%02x", &r); err == nil {
				b.WriteRune(r)
				i += 2
				continue
			}
		}
		b.WriteByte(id[i])
	}
	return b.String()
}
