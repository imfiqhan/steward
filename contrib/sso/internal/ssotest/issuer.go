// Package ssotest runs a fake OpenID Connect issuer for tests.
package ssotest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Issuer is a fake OIDC provider. Authorize is not served: tests read the
// authorization URL, then call Grant to decide what the code yields.
type Issuer struct {
	URL string

	t      *testing.T
	key    *rsa.PrivateKey
	signer jose.Signer

	mu     sync.Mutex
	grants map[string]grant

	// CheckClient, when set, validates the client credentials posted to the
	// token endpoint.
	CheckClient func(clientID, clientSecret string) bool
}

type grant struct {
	claims    map[string]any
	challenge string
}

// NewIssuer starts the fake issuer; it stops when the test ends.
func NewIssuer(t *testing.T) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"))
	if err != nil {
		t.Fatal(err)
	}
	is := &Issuer{t: t, key: key, signer: signer, grants: map[string]grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", is.discovery)
	mux.HandleFunc("GET /keys", is.keys)
	mux.HandleFunc("POST /token", is.token)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	is.URL = srv.URL
	return is
}

func (is *Issuer) discovery(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                is.URL,
		"authorization_endpoint":                is.URL + "/authorize",
		"token_endpoint":                        is.URL + "/token",
		"jwks_uri":                              is.URL + "/keys",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (is *Issuer) keys(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: is.key.Public(), KeyID: "test", Algorithm: "RS256", Use: "sig",
	}}})
}

// Grant registers what code yields: an ID token with these claims (iss, iat
// and exp are filled in). challenge is the PKCE S256 challenge the token
// request's verifier must match; empty skips the check.
func (is *Issuer) Grant(code, challenge string, claims map[string]any) {
	is.mu.Lock()
	defer is.mu.Unlock()
	is.grants[code] = grant{claims: claims, challenge: challenge}
}

func (is *Issuer) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	if is.CheckClient != nil && !is.CheckClient(id, secret) {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	is.mu.Lock()
	g, ok := is.grants[r.PostFormValue("code")]
	delete(is.grants, r.PostFormValue("code"))
	is.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	if g.challenge != "" {
		sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			http.Error(w, `{"error":"invalid_grant","error_description":"pkce"}`, http.StatusBadRequest)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "at", "token_type": "Bearer", "expires_in": 3600,
		"id_token": is.Sign(g.claims),
	})
}

// Sign returns an ID token with these claims, signed by the issuer.
func (is *Issuer) Sign(claims map[string]any) string {
	is.t.Helper()
	full := map[string]any{"iss": is.URL, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	for k, v := range claims {
		full[k] = v
	}
	raw, err := jwt.Signed(is.signer).Claims(full).Serialize()
	if err != nil {
		is.t.Fatal(err)
	}
	return raw
}

// Public returns the issuer's public key.
func (is *Issuer) Public() crypto.PublicKey { return is.key.Public() }
