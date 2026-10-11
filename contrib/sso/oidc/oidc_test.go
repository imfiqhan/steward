package oidc_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
	"github.com/imfiqhan/steward/contrib/sso/google"
	"github.com/imfiqhan/steward/contrib/sso/internal/ssotest"
	"github.com/imfiqhan/steward/contrib/sso/oidc"
)

// begin runs AuthURL and returns the request and the parsed redirect.
func begin(t *testing.T, p steward.AuthProvider) (*steward.SSORequest, url.Values) {
	t.Helper()
	req := &steward.SSORequest{State: "st", CallbackURL: "https://panel.test/admin/auth/sso/x/callback", Values: map[string]string{}}
	raw, err := p.AuthURL(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return req, u.Query()
}

func callback(code string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/cb?code="+url.QueryEscape(code), nil)
	return r
}

func TestAuthorizationCodeFlow(t *testing.T) {
	is := ssotest.NewIssuer(t)
	is.CheckClient = func(id, secret string) bool { return id == "client" && secret == "s3cret" }
	p, err := oidc.New(context.Background(), oidc.Config{ID: "x", Issuer: is.URL, ClientID: "client", ClientSecret: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}

	req, q := begin(t, p)
	if q.Get("state") != "st" || q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" {
		t.Fatalf("authorization URL: %v", q)
	}
	is.Grant("c1", q.Get("code_challenge"), map[string]any{
		"sub": "u-1", "aud": "client", "nonce": q.Get("nonce"),
		"email": "u@example.com", "email_verified": true, "name": "U One",
	})
	id, err := p.Exchange(context.Background(), req, callback("c1"))
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "u-1" || id.Email != "u@example.com" || !id.EmailVerified || id.Name != "U One" {
		t.Fatalf("identity %+v", id)
	}
}

func TestExchangeRejectsTamperedFlows(t *testing.T) {
	is := ssotest.NewIssuer(t)
	p, err := oidc.New(context.Background(), oidc.Config{ID: "x", Issuer: is.URL, ClientID: "client", ClientSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}

	req, q := begin(t, p)
	is.Grant("wrong-nonce", q.Get("code_challenge"), map[string]any{"sub": "u", "aud": "client", "nonce": "other"})
	if _, err := p.Exchange(context.Background(), req, callback("wrong-nonce")); err == nil {
		t.Fatal("accepted an id_token with another nonce")
	}

	req, q = begin(t, p)
	is.Grant("wrong-aud", q.Get("code_challenge"), map[string]any{"sub": "u", "aud": "someone-else", "nonce": q.Get("nonce")})
	if _, err := p.Exchange(context.Background(), req, callback("wrong-aud")); err == nil {
		t.Fatal("accepted an id_token for another client")
	}

	req, q = begin(t, p)
	req.Values["pkce"] = "not-the-verifier"
	is.Grant("wrong-pkce", q.Get("code_challenge"), map[string]any{"sub": "u", "aud": "client", "nonce": q.Get("nonce")})
	if _, err := p.Exchange(context.Background(), req, callback("wrong-pkce")); err == nil {
		t.Fatal("token exchange succeeded with the wrong PKCE verifier")
	}
}

func TestVerifyIDTokenAudiences(t *testing.T) {
	is := ssotest.NewIssuer(t)
	p, err := google.NewVerifier(context.Background(), google.Config{ClientID: "web", Issuer: is.URL})
	if err != nil {
		t.Fatal(err)
	}
	opts := oidc.VerifyOptions{Audiences: []string{"ios", "android"}}

	id, err := p.VerifyIDToken(context.Background(), is.Sign(map[string]any{"sub": "g-1", "aud": "ios", "email": "a@b.c", "email_verified": true}), opts)
	if err != nil || id.Subject != "g-1" || !id.EmailVerified {
		t.Fatalf("iOS token: %+v %v", id, err)
	}
	if _, err := p.VerifyIDToken(context.Background(), is.Sign(map[string]any{"sub": "g-1", "aud": "web"}), opts); err == nil {
		t.Fatal("accepted a token for an audience outside the list")
	}
	if _, err := p.VerifyIDToken(context.Background(), is.Sign(map[string]any{"sub": "g-1", "aud": "ios", "nonce": "a"}),
		oidc.VerifyOptions{Audiences: opts.Audiences, Nonce: "b"}); err == nil {
		t.Fatal("accepted a token with the wrong nonce")
	}
}

func TestGoogleHostedDomain(t *testing.T) {
	is := ssotest.NewIssuer(t)
	p, err := google.New(context.Background(), google.Config{ClientID: "client", ClientSecret: "s", HostedDomain: "corp.example", Issuer: is.URL})
	if err != nil {
		t.Fatal(err)
	}
	req, q := begin(t, p)
	if q.Get("hd") != "corp.example" {
		t.Fatalf("hd hint missing: %v", q)
	}
	is.Grant("outside", q.Get("code_challenge"), map[string]any{"sub": "u", "aud": "client", "nonce": q.Get("nonce"), "hd": "gmail.com"})
	if _, err := p.Exchange(context.Background(), req, callback("outside")); err == nil || !strings.Contains(err.Error(), "Workspace domain") {
		t.Fatalf("account outside the hosted domain: %v", err)
	}
}
