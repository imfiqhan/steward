package apple_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	steward "github.com/imfiqhan/steward"
	"github.com/imfiqhan/steward/contrib/sso/apple"
	"github.com/imfiqhan/steward/contrib/sso/internal/ssotest"
)

func TestSignInWithApple(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	p8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	is := ssotest.NewIssuer(t)
	is.CheckClient = func(id, secret string) bool {
		if id != "com.example.web" {
			return false
		}
		tok, err := jwt.ParseSigned(secret, []jose.SignatureAlgorithm{jose.ES256})
		if err != nil || tok.Headers[0].KeyID != "KEY123" {
			return false
		}
		var c jwt.Claims
		if err := tok.Claims(&key.PublicKey, &c); err != nil {
			return false
		}
		return c.Issuer == "TEAM123456" && c.Subject == "com.example.web" &&
			c.Audience.Contains(is.URL) && c.ValidateWithLeeway(jwt.Expected{Time: time.Now()}, 0) == nil
	}

	p, err := apple.New(context.Background(), apple.Config{
		ServicesID: "com.example.web", TeamID: "TEAM123456", KeyID: "KEY123", PrivateKey: p8, Issuer: is.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := &steward.SSORequest{State: "st", CallbackURL: "https://panel.test/admin/auth/sso/apple/callback", Values: map[string]string{}}
	raw, err := p.AuthURL(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("response_mode") != "form_post" || !strings.Contains(q.Get("scope"), "name") {
		t.Fatalf("authorization URL: %v", q)
	}

	is.Grant("code", q.Get("code_challenge"), map[string]any{
		"sub": "001234.abc", "aud": "com.example.web", "nonce": q.Get("nonce"),
		"email": "x@privaterelay.appleid.com", "email_verified": "true",
	})
	form := url.Values{"code": {"code"}, "state": {"st"},
		"user": {`{"name":{"firstName":"Ada","lastName":"Lovelace"},"email":"x@privaterelay.appleid.com"}`}}
	r, _ := http.NewRequest(http.MethodPost, "/cb", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	id, err := p.Exchange(context.Background(), req, r)
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "001234.abc" || !id.EmailVerified || id.Name != "Ada Lovelace" || id.Provider != "apple" {
		t.Fatalf("identity %+v", id)
	}
}

func TestNewRejectsBadKey(t *testing.T) {
	_, err := apple.New(context.Background(), apple.Config{
		ServicesID: "s", TeamID: "t", KeyID: "k", PrivateKey: []byte("not pem"),
	})
	if err == nil {
		t.Fatal("accepted a private key that is not PEM")
	}
}
