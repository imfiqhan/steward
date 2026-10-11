// Package saml is a Steward AuthProviderSource for SAML 2.0 identity
// providers, one connection per organization. Each connection becomes a
// provider with the ID "saml-<slug>"; users reach it from the login page by
// typing their work email or the organization slug.
package saml

import (
	"bytes"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	crewjam "github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
	"github.com/imfiqhan/steward/migrate"
)

// Connection is one organization's identity provider.
type Connection struct {
	ID uint `gorm:"primaryKey"`
	// TenantID is the tenant signed-in users are moved into (0: none).
	TenantID uint   `gorm:"index;default:0"`
	Slug     string `gorm:"size:64;uniqueIndex;not null"`
	Name     string `gorm:"size:255"`
	// Domains is a comma-separated list of email domains that discover
	// this connection, e.g. "example.com,example.org".
	Domains string `gorm:"size:1024"`
	// Exactly one of IDPMetadataURL and IDPMetadataXML is used; the URL
	// wins when both are set.
	IDPMetadataURL string `gorm:"size:1024"`
	IDPMetadataXML string `gorm:"type:text"`
	Enabled        bool   `gorm:"default:true"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TableName is fixed; Steward's table prefix does not apply to contrib models.
func (Connection) TableName() string { return "sso_connections" }

// Migration creates the connections table. Register it with the
// application's migrations.
func Migration() migrate.Migration {
	return migrate.Migration{
		Name: "sso_0001_create_sso_connections",
		Up:   func(tx *gorm.DB) error { return tx.Migrator().AutoMigrate(&Connection{}) },
		Down: func(tx *gorm.DB) error { return tx.Migrator().DropTable(&Connection{}) },
	}
}

// Config configures the source.
type Config struct {
	DB *gorm.DB
	// Key and Certificate are the service provider's signing pair, published
	// in the metadata every identity provider imports.
	Key         crypto.Signer
	Certificate *x509.Certificate
	// HTTPClient fetches IdP metadata; nil uses http.DefaultClient.
	HTTPClient *http.Client
	// MetadataTTL bounds how long fetched IdP metadata is reused (default 1h).
	MetadataTTL time.Duration
}

// Source implements steward.AuthProviderSource.
type Source struct {
	cfg Config

	mu    sync.Mutex
	cache map[string]cachedMetadata
	seen  *replayCache
}

type cachedMetadata struct {
	md      *crewjam.EntityDescriptor
	fetched time.Time
}

// ProviderPrefix begins every SAML provider ID.
const ProviderPrefix = "saml-"

// NewSource returns the source.
func NewSource(cfg Config) (*Source, error) {
	if cfg.DB == nil || cfg.Key == nil || cfg.Certificate == nil {
		return nil, errors.New("saml: DB, Key and Certificate are required")
	}
	if cfg.MetadataTTL == 0 {
		cfg.MetadataTTL = time.Hour
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Source{cfg: cfg, cache: map[string]cachedMetadata{}, seen: newReplayCache()}, nil
}

func (s *Source) db(ctx context.Context) *gorm.DB {
	return s.cfg.DB.WithContext(steward.WithoutTenant(ctx))
}

// Provider implements steward.AuthProviderSource.
func (s *Source) Provider(ctx context.Context, id string) (steward.AuthProvider, error) {
	slug, ok := strings.CutPrefix(id, ProviderPrefix)
	if !ok || slug == "" {
		return nil, nil
	}
	var c Connection
	err := s.db(ctx).Where("slug = ? AND enabled = ?", slug, true).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &provider{src: s, conn: c}, nil
}

// Discover implements steward.AuthProviderSource. An email address matches
// a connection by domain; anything else matches by slug.
func (s *Source) Discover(ctx context.Context, hint string) (steward.AuthProvider, error) {
	hint = strings.ToLower(strings.TrimSpace(hint))
	if _, domain, ok := strings.Cut(hint, "@"); ok {
		var conns []Connection
		if err := s.db(ctx).Where("enabled = ? AND domains <> ''", true).Find(&conns).Error; err != nil {
			return nil, err
		}
		for _, c := range conns {
			for _, d := range strings.Split(c.Domains, ",") {
				if strings.EqualFold(strings.TrimSpace(d), domain) {
					return &provider{src: s, conn: c}, nil
				}
			}
		}
		return nil, nil
	}
	return s.Provider(ctx, ProviderPrefix+hint)
}

func (s *Source) metadata(ctx context.Context, c Connection) (*crewjam.EntityDescriptor, error) {
	key := fmt.Sprintf("%d:%s", c.ID, c.UpdatedAt.Format(time.RFC3339Nano))
	s.mu.Lock()
	if m, ok := s.cache[key]; ok && time.Since(m.fetched) < s.cfg.MetadataTTL {
		s.mu.Unlock()
		return m.md, nil
	}
	s.mu.Unlock()

	var md *crewjam.EntityDescriptor
	var err error
	switch {
	case c.IDPMetadataURL != "":
		u, perr := url.Parse(c.IDPMetadataURL)
		if perr != nil {
			return nil, perr
		}
		md, err = samlsp.FetchMetadata(ctx, s.cfg.HTTPClient, *u)
	case c.IDPMetadataXML != "":
		md, err = samlsp.ParseMetadata([]byte(c.IDPMetadataXML))
	default:
		err = errors.New("no IdP metadata configured")
	}
	if err != nil {
		return nil, fmt.Errorf("saml %s: metadata: %w", c.Slug, err)
	}
	s.mu.Lock()
	s.cache[key] = cachedMetadata{md: md, fetched: time.Now()}
	s.mu.Unlock()
	return md, nil
}

// provider is one connection as a steward.AuthProvider.
type provider struct {
	src  *Source
	conn Connection
}

func (p *provider) ID() string    { return ProviderPrefix + p.conn.Slug }
func (p *provider) Label() string { return "Continue with " + p.conn.Name }
func (p *provider) Icon() string  { return "building-2" }

// sp builds the service provider for a callback URL. The metadata URL (and
// entity ID) sits beside the callback at …/metadata.
func (p *provider) sp(ctx context.Context, callback string) (*crewjam.ServiceProvider, error) {
	md, err := p.src.metadata(ctx, p.conn)
	if err != nil {
		return nil, err
	}
	acs, err := url.Parse(callback)
	if err != nil {
		return nil, err
	}
	meta := *acs
	meta.Path = strings.TrimSuffix(meta.Path, "/callback") + "/metadata"
	return &crewjam.ServiceProvider{
		EntityID:    meta.String(),
		Key:         p.src.cfg.Key,
		Certificate: p.src.cfg.Certificate,
		HTTPClient:  p.src.cfg.HTTPClient,
		MetadataURL: meta,
		AcsURL:      *acs,
		IDPMetadata: md,
		// Assertions must answer a request this panel made.
		AllowIDPInitiated: false,
	}, nil
}

// AuthURL implements steward.AuthProvider.
func (p *provider) AuthURL(ctx context.Context, req *steward.SSORequest) (string, error) {
	sp, err := p.sp(ctx, req.CallbackURL)
	if err != nil {
		return "", err
	}
	loc := sp.GetSSOBindingLocation(crewjam.HTTPRedirectBinding)
	if loc == "" {
		return "", errors.New("saml: identity provider has no HTTP-Redirect SSO endpoint")
	}
	ar, err := sp.MakeAuthenticationRequest(loc, crewjam.HTTPRedirectBinding, crewjam.HTTPPostBinding)
	if err != nil {
		return "", err
	}
	req.Values["rid"] = ar.ID
	u, err := ar.Redirect(req.State, sp)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// Exchange implements steward.AuthProvider.
func (p *provider) Exchange(ctx context.Context, req *steward.SSORequest, r *http.Request) (*steward.ExternalIdentity, error) {
	sp, err := p.sp(ctx, req.CallbackURL)
	if err != nil {
		return nil, err
	}
	a, err := sp.ParseResponse(r, []string{req.Values["rid"]})
	if err != nil {
		var ire *crewjam.InvalidResponseError
		if errors.As(err, &ire) {
			return nil, fmt.Errorf("saml: %w: %v", err, ire.PrivateErr)
		}
		return nil, err
	}
	if a.ID == "" || !p.src.seen.add(a.ID, assertionExpiry(a)) {
		return nil, errors.New("saml: assertion was already used")
	}
	if a.Subject == nil || a.Subject.NameID == nil || a.Subject.NameID.Value == "" {
		return nil, errors.New("saml: assertion without NameID")
	}
	id := &steward.ExternalIdentity{
		Provider: p.ID(),
		Subject:  a.Subject.NameID.Value,
		TenantID: p.conn.TenantID,
		Raw:      map[string]any{},
	}
	for _, st := range a.AttributeStatements {
		for _, at := range st.Attributes {
			if len(at.Values) == 0 {
				continue
			}
			v := at.Values[0].Value
			id.Raw[at.Name] = v
			switch attrKind(at) {
			case "email":
				id.Email = v
			case "name":
				id.Name = v
			}
		}
	}
	if id.Email == "" && a.Subject.NameID.Format == string(crewjam.EmailAddressNameIDFormat) {
		id.Email = a.Subject.NameID.Value
	}
	// The organization's IdP is authoritative for its own domains.
	id.EmailVerified = id.Email != "" && p.ownsDomain(id.Email)
	return id, nil
}

// Metadata implements steward.MetadataProvider.
func (p *provider) Metadata(ctx context.Context, callback string) ([]byte, string, error) {
	sp, err := p.sp(ctx, callback)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	if err := xml.NewEncoder(&buf).Encode(sp.Metadata()); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "application/samlmetadata+xml", nil
}

func (p *provider) ownsDomain(email string) bool {
	_, domain, ok := strings.Cut(strings.ToLower(email), "@")
	if !ok {
		return false
	}
	for _, d := range strings.Split(p.conn.Domains, ",") {
		if strings.EqualFold(strings.TrimSpace(d), domain) {
			return true
		}
	}
	return false
}

func attrKind(at crewjam.Attribute) string {
	n := strings.ToLower(at.Name)
	f := strings.ToLower(at.FriendlyName)
	switch {
	case n == "email", f == "email", f == "mail", n == "mail",
		n == "urn:oid:0.9.2342.19200300.100.1.3",
		strings.HasSuffix(n, "/claims/emailaddress"):
		return "email"
	case n == "name", f == "displayname", n == "displayname", f == "cn",
		n == "urn:oid:2.16.840.1.113730.3.1.241", n == "urn:oid:2.5.4.3",
		strings.HasSuffix(n, "/claims/name"):
		return "name"
	}
	return ""
}

func assertionExpiry(a *crewjam.Assertion) time.Time {
	if a.Conditions != nil && !a.Conditions.NotOnOrAfter.IsZero() {
		return a.Conditions.NotOnOrAfter
	}
	return time.Now().Add(time.Hour)
}

// replayCache remembers assertion IDs until they expire. It is per process;
// several replicas each keep their own.
type replayCache struct {
	mu  sync.Mutex
	ids map[string]time.Time
}

func newReplayCache() *replayCache { return &replayCache{ids: map[string]time.Time{}} }

// add records id and reports whether it was new.
func (c *replayCache) add(id string, until time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, exp := range c.ids {
		if now.After(exp) {
			delete(c.ids, k)
		}
	}
	if _, dup := c.ids[id]; dup {
		return false
	}
	c.ids[id] = until
	return true
}
