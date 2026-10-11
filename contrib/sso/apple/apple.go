// Package apple is a Steward AuthProvider for Sign in with Apple, plus
// identity-token verification for the native iOS and Android flows.
package apple

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"

	steward "github.com/imfiqhan/steward"
	"github.com/imfiqhan/steward/contrib/sso/oidc"
)

// Issuer is Apple's OpenID Connect issuer.
const Issuer = "https://appleid.apple.com"

// Config configures Sign in with Apple. In the Apple Developer portal create
// a Services ID for the web flow, add the return URL
// {PublicURL}{Prefix}/auth/sso/apple/callback, and create a key with Sign in
// with Apple enabled.
type Config struct {
	// ServicesID is the web client ID (a Services ID, e.g. com.example.web).
	ServicesID string
	// TeamID is the 10-character Apple Developer team ID.
	TeamID string
	// KeyID identifies the Sign in with Apple key.
	KeyID string
	// PrivateKey is the key's .p8 file contents (PEM, PKCS#8 EC key).
	PrivateKey []byte
	// Label overrides "Continue with Apple".
	Label string
	// Issuer overrides the issuer; for tests.
	Issuer string
	// HTTPClient is used for discovery, keys and token exchange.
	HTTPClient *http.Client
}

// Provider is the Apple provider (ID "apple").
type Provider struct {
	*oidc.Provider
}

// New returns the Apple provider. Apple returns to the callback with a
// cross-site form POST, so the panel must be served over HTTPS.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.ServicesID == "" || cfg.TeamID == "" || cfg.KeyID == "" || len(cfg.PrivateKey) == 0 {
		return nil, errors.New("apple: ServicesID, TeamID, KeyID and PrivateKey are required")
	}
	secret, err := newSecretSource(cfg)
	if err != nil {
		return nil, err
	}
	p, err := oidc.New(ctx, oidcConfig(cfg, secret.get))
	if err != nil {
		return nil, err
	}
	return &Provider{Provider: p}, nil
}

// NewVerifier returns a provider for verifying identity tokens from native
// apps only; it needs no key. Pass the bundle ID (iOS) or Services ID
// (Android web flow) as audiences.
func NewVerifier(ctx context.Context, cfg Config) (*oidc.Provider, error) {
	c := oidcConfig(cfg, nil)
	c.ClientSecret = "unused"
	if c.ClientID == "" {
		c.ClientID = "unused"
	}
	return oidc.New(ctx, c)
}

func oidcConfig(cfg Config, secret func() (string, error)) oidc.Config {
	issuer := cfg.Issuer
	if issuer == "" {
		issuer = Issuer
	}
	label := cfg.Label
	if label == "" {
		label = "Continue with Apple"
	}
	return oidc.Config{
		ID: "apple", Label: label, Icon: "apple", Issuer: issuer,
		ClientID: cfg.ServicesID, ClientSecretFunc: secret,
		Scopes: []string{"openid", "email", "name"},
		// Apple requires form_post whenever name or email is requested.
		AuthParams: map[string]string{"response_mode": "form_post"},
		AuthStyle:  oauth2.AuthStyleInParams,
		HTTPClient: cfg.HTTPClient,
	}
}

// Exchange verifies the callback and adds the name Apple posts only on the
// first authorization.
func (p *Provider) Exchange(ctx context.Context, req *steward.SSORequest, r *http.Request) (*steward.ExternalIdentity, error) {
	id, err := p.Provider.Exchange(ctx, req, r)
	if err != nil {
		return nil, err
	}
	if id.Name == "" {
		id.Name = NameFromUserField(r.FormValue("user"))
	}
	return id, nil
}

// NameFromUserField reads the "user" JSON Apple sends with the first
// authorization: {"name":{"firstName":"…","lastName":"…"},"email":"…"}.
func NameFromUserField(raw string) string {
	if raw == "" {
		return ""
	}
	var u struct {
		Name struct {
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
		} `json:"name"`
	}
	if json.Unmarshal([]byte(raw), &u) != nil {
		return ""
	}
	return strings.TrimSpace(u.Name.FirstName + " " + u.Name.LastName)
}

// secretSource signs the ES256 client secret Apple requires and reuses it
// until shortly before it expires.
type secretSource struct {
	cfg    Config
	signer jose.Signer

	mu      sync.Mutex
	value   string
	expires time.Time
}

// secretTTL is the client secret lifetime; Apple allows up to six months.
const secretTTL = 24 * time.Hour

func newSecretSource(cfg Config) (*secretSource, error) {
	block, _ := pem.Decode(cfg.PrivateKey)
	if block == nil {
		return nil, errors.New("apple: PrivateKey is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apple: PrivateKey: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apple: PrivateKey is not an EC key")
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", cfg.KeyID))
	if err != nil {
		return nil, err
	}
	return &secretSource{cfg: cfg, signer: signer}, nil
}

func (s *secretSource) get() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value != "" && time.Until(s.expires) > time.Hour {
		return s.value, nil
	}
	now := time.Now()
	exp := now.Add(secretTTL)
	aud := s.cfg.Issuer
	if aud == "" {
		aud = Issuer
	}
	v, err := jwt.Signed(s.signer).Claims(jwt.Claims{
		Issuer: s.cfg.TeamID, Subject: s.cfg.ServicesID, Audience: jwt.Audience{aud},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(exp),
	}).Serialize()
	if err != nil {
		return "", err
	}
	s.value, s.expires = v, exp
	return v, nil
}
