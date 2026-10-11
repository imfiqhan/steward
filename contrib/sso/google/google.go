// Package google is a Steward AuthProvider for Google sign-in (OpenID
// Connect), plus ID-token verification for native Google Sign-In.
package google

import (
	"context"
	"errors"
	"strings"

	steward "github.com/imfiqhan/steward"
	"github.com/imfiqhan/steward/contrib/sso/oidc"
)

// Issuer is Google's OpenID Connect issuer.
const Issuer = "https://accounts.google.com"

// Config configures Google sign-in. Create the OAuth client in Google Cloud
// Console as a "Web application" and register the callback URL
// {PublicURL}{Prefix}/auth/sso/google/callback.
type Config struct {
	ClientID     string
	ClientSecret string
	// HostedDomain restricts sign-in to one Google Workspace domain. It is
	// checked against the hd claim, not only passed as a hint.
	HostedDomain string
	// Label overrides "Continue with Google".
	Label string
	// Issuer overrides the issuer; for tests.
	Issuer string
}

// New returns the Google provider (ID "google").
func New(ctx context.Context, cfg Config) (*oidc.Provider, error) {
	return oidc.New(ctx, oidcConfig(cfg))
}

// NewVerifier returns a provider for verifying ID tokens from native apps
// only. It needs no client secret; audiences are the app's client IDs.
func NewVerifier(ctx context.Context, cfg Config) (*oidc.Provider, error) {
	c := oidcConfig(cfg)
	if c.ClientSecret == "" {
		c.ClientSecret = "unused"
	}
	return oidc.New(ctx, c)
}

func oidcConfig(cfg Config) oidc.Config {
	issuer := cfg.Issuer
	if issuer == "" {
		issuer = Issuer
	}
	label := cfg.Label
	if label == "" {
		label = "Continue with Google"
	}
	c := oidc.Config{
		ID: "google", Label: label, Icon: "google", Issuer: issuer,
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
		AuthParams: map[string]string{"prompt": "select_account"},
	}
	if cfg.HostedDomain != "" {
		c.AuthParams["hd"] = cfg.HostedDomain
		want := strings.ToLower(cfg.HostedDomain)
		c.Claims = func(claims map[string]any, _ *steward.ExternalIdentity) error {
			if hd, _ := claims["hd"].(string); strings.ToLower(hd) != want {
				return errors.New("google: account is outside the allowed Workspace domain")
			}
			return nil
		}
	}
	return c
}
