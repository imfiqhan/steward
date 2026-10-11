// Package oidc is a Steward AuthProvider for any OpenID Connect provider,
// using the authorization-code flow with PKCE and a nonce. The google and
// apple packages build on it.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	steward "github.com/imfiqhan/steward"
)

// Config describes one OpenID Connect provider.
type Config struct {
	// ID is the provider's URL slug in Steward, e.g. "okta".
	ID string
	// Label is the login button text.
	Label string
	// Icon is a Lucide or overlay icon name.
	Icon string

	// Issuer is the provider's issuer URL; its discovery document is read
	// from Issuer + "/.well-known/openid-configuration" by New.
	Issuer string

	ClientID     string
	ClientSecret string
	// ClientSecretFunc, when set, is called for each token exchange instead
	// of using ClientSecret (Apple signs a short-lived JWT).
	ClientSecretFunc func() (string, error)

	// Scopes defaults to openid, email and profile.
	Scopes []string
	// AuthParams are added to the authorization URL, e.g.
	// {"response_mode": "form_post"} or {"hd": "example.com"}.
	AuthParams map[string]string
	// AuthStyle is how the client authenticates at the token endpoint;
	// zero auto-detects.
	AuthStyle oauth2.AuthStyle

	// Claims maps the verified ID token to extra identity fields. It runs
	// after the standard claims are read and may reject the token.
	Claims func(claims map[string]any, id *steward.ExternalIdentity) error

	// HTTPClient is used for discovery, keys and token exchange.
	HTTPClient *http.Client
}

// Provider implements steward.AuthProvider.
type Provider struct {
	cfg      Config
	provider *gooidc.Provider
	verifier *gooidc.IDTokenVerifier
}

// New reads the provider's discovery document and returns the provider.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.ID == "" || cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, errors.New("oidc: ID, Issuer and ClientID are required")
	}
	if cfg.ClientSecret == "" && cfg.ClientSecretFunc == nil {
		return nil, fmt.Errorf("oidc %s: a client secret is required", cfg.ID)
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{gooidc.ScopeOpenID, "email", "profile"}
	}
	if cfg.Label == "" {
		cfg.Label = "Continue with " + cfg.ID
	}
	p, err := gooidc.NewProvider(clientContext(ctx, cfg.HTTPClient), cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc %s: discovery: %w", cfg.ID, err)
	}
	return &Provider{cfg: cfg, provider: p, verifier: p.Verifier(&gooidc.Config{ClientID: cfg.ClientID})}, nil
}

func clientContext(ctx context.Context, c *http.Client) context.Context {
	if c == nil {
		return ctx
	}
	return gooidc.ClientContext(ctx, c)
}

func (p *Provider) ID() string    { return p.cfg.ID }
func (p *Provider) Label() string { return p.cfg.Label }
func (p *Provider) Icon() string  { return p.cfg.Icon }

func (p *Provider) oauth(redirect string) (*oauth2.Config, error) {
	secret := p.cfg.ClientSecret
	if p.cfg.ClientSecretFunc != nil {
		s, err := p.cfg.ClientSecretFunc()
		if err != nil {
			return nil, err
		}
		secret = s
	}
	ep := p.provider.Endpoint()
	ep.AuthStyle = p.cfg.AuthStyle
	return &oauth2.Config{
		ClientID: p.cfg.ClientID, ClientSecret: secret,
		Endpoint: ep, RedirectURL: redirect, Scopes: p.cfg.Scopes,
	}, nil
}

// AuthURL implements steward.AuthProvider.
func (p *Provider) AuthURL(_ context.Context, req *steward.SSORequest) (string, error) {
	conf := &oauth2.Config{
		ClientID: p.cfg.ClientID, Endpoint: p.provider.Endpoint(),
		RedirectURL: req.CallbackURL, Scopes: p.cfg.Scopes,
	}
	nonce, err := randomString()
	if err != nil {
		return "", err
	}
	verifier := oauth2.GenerateVerifier()
	req.Values["nonce"] = nonce
	req.Values["pkce"] = verifier
	opts := []oauth2.AuthCodeOption{gooidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	for k, v := range p.cfg.AuthParams {
		opts = append(opts, oauth2.SetAuthURLParam(k, v))
	}
	return conf.AuthCodeURL(req.State, opts...), nil
}

// Exchange implements steward.AuthProvider.
func (p *Provider) Exchange(ctx context.Context, req *steward.SSORequest, r *http.Request) (*steward.ExternalIdentity, error) {
	code := r.FormValue("code")
	if code == "" {
		return nil, errors.New("oidc: callback without a code")
	}
	conf, err := p.oauth(req.CallbackURL)
	if err != nil {
		return nil, err
	}
	ctx = clientContext(ctx, p.cfg.HTTPClient)
	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(req.Values["pkce"]))
	if err != nil {
		return nil, fmt.Errorf("oidc: token exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return nil, errors.New("oidc: token response without id_token")
	}
	idt, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("oidc: id_token: %w", err)
	}
	if idt.Nonce == "" || idt.Nonce != req.Values["nonce"] {
		return nil, errors.New("oidc: id_token nonce does not match")
	}
	return p.identity(idt)
}

// VerifyOptions tunes VerifyIDToken.
type VerifyOptions struct {
	// Audiences are the client IDs a token may be issued to, e.g. the iOS,
	// Android and web client IDs of one app. Empty means Config.ClientID.
	Audiences []string
	// Nonce, when set, must equal the token's nonce claim.
	Nonce string
}

// VerifyIDToken checks an ID token a native client obtained itself (Google
// Sign-In on Android or iOS, Sign in with Apple) and returns its identity.
func (p *Provider) VerifyIDToken(ctx context.Context, raw string, opts VerifyOptions) (*steward.ExternalIdentity, error) {
	auds := opts.Audiences
	if len(auds) == 0 {
		auds = []string{p.cfg.ClientID}
	}
	v := p.provider.Verifier(&gooidc.Config{SkipClientIDCheck: true})
	idt, err := v.Verify(clientContext(ctx, p.cfg.HTTPClient), raw)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(idt.Audience, func(a string) bool { return slices.Contains(auds, a) }) {
		return nil, errors.New("oidc: id_token audience is not accepted")
	}
	if opts.Nonce != "" && idt.Nonce != opts.Nonce {
		return nil, errors.New("oidc: id_token nonce does not match")
	}
	return p.identity(idt)
}

func (p *Provider) identity(idt *gooidc.IDToken) (*steward.ExternalIdentity, error) {
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return nil, err
	}
	id := &steward.ExternalIdentity{
		Provider:      p.cfg.ID,
		Subject:       idt.Subject,
		Email:         stringClaim(claims, "email"),
		EmailVerified: BoolClaim(claims, "email_verified"),
		Name:          stringClaim(claims, "name"),
		Raw:           claims,
	}
	if id.Subject == "" {
		return nil, errors.New("oidc: id_token without subject")
	}
	if p.cfg.Claims != nil {
		if err := p.cfg.Claims(claims, id); err != nil {
			return nil, err
		}
	}
	return id, nil
}

func stringClaim(c map[string]any, k string) string {
	s, _ := c[k].(string)
	return s
}

// BoolClaim reads a boolean claim that some providers (Apple) send as the
// string "true".
func BoolClaim(c map[string]any, k string) bool {
	switch v := c[k].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	}
	return false
}

func randomString() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashNonce returns the SHA-256 hex digest native Sign in with Apple puts in
// the nonce claim when the app passes a raw nonce.
func HashNonce(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum[:])
}
