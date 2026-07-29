package steward

// Bearer-token authentication for API and mobile clients, enabled by
// Config.EnableTokenAuth.
//
// A token is an opaque random string presented as "Authorization: Bearer
// <token>". Only its SHA-256 is stored, so a database leak does not yield
// usable credentials. Tokens belong to an AdminUser and carry exactly that
// user's roles, permissions, and policies — there is no separate authorization
// path to keep in sync with the panel's.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/imfiqhan/steward/internal/session"
)

// tokenPrefix marks Steward tokens so secret scanners and log filters can spot
// them, and so an operator can tell one from a session cookie at a glance.
const tokenPrefix = "stw_"

// defaultTokenTTL applies when Config.TokenTTL is zero.
const defaultTokenTTL = 30 * 24 * time.Hour

// touchInterval throttles last-used bookkeeping so a busy client does not turn
// every read into a write.
const touchInterval = 5 * time.Minute

// tokenHash is the stored form of a token. SHA-256 rather than bcrypt: tokens
// are 256-bit random values, so stretching buys nothing, and a plain digest
// keeps lookup a single indexed query.
func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func newTokenValue() (string, error) {
	v, err := session.NewToken()
	if err != nil {
		return "", err
	}
	return tokenPrefix + v, nil
}

// tokenTTL resolves Config.TokenTTL: zero means the default, negative means
// tokens never expire.
func (a *Admin) tokenTTL() time.Duration {
	if a.cfg.TokenTTL == 0 {
		return defaultTokenTTL
	}
	return a.cfg.TokenTTL
}

// bearerToken extracts the credential from an Authorization header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// tokenOf returns the token that authenticated the request, or nil when the
// caller arrived by session cookie.
func tokenOf(r *http.Request) *AdminToken {
	t, _ := r.Context().Value(ctxKeyToken).(*AdminToken)
	return t
}

// resolveToken looks up a presented token and its owner. A missing, expired,
// or orphaned token is reported as errBadCredentials; anything else is a real
// failure worth surfacing.
func (a *Admin) resolveToken(ctx context.Context, raw string) (*AdminToken, *AdminUser, error) {
	var tok AdminToken
	err := a.db.WithContext(ctx).Where("hash = ?", tokenHash(raw)).First(&tok).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, errBadCredentials
	}
	if err != nil {
		return nil, nil, err
	}
	if tok.ExpiresAt != nil && time.Now().After(*tok.ExpiresAt) {
		return nil, nil, errBadCredentials
	}
	var user AdminUser
	err = a.db.WithContext(ctx).Preload("Roles").First(&user, tok.UserID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// The owning account was deleted; its tokens die with it.
		return nil, nil, errBadCredentials
	}
	if err != nil {
		return nil, nil, err
	}
	return &tok, &user, nil
}

// touchToken records last use, at most once per touchInterval.
func (a *Admin) touchToken(ctx context.Context, tok *AdminToken) {
	now := time.Now()
	if tok.LastUsedAt != nil && now.Sub(*tok.LastUsedAt) < touchInterval {
		return
	}
	if err := a.db.WithContext(ctx).Model(&AdminToken{}).
		Where("id = ?", tok.ID).Update("last_used_at", now).Error; err != nil {
		a.log.Error("steward: token last-used update", "err", err)
	}
}

// withToken resolves a bearer token into the request's principal. It runs
// before withCSRF so that CSRF can be skipped for token callers, and before
// withAuth, which defers to a principal that is already resolved.
func (a *Admin) withToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := ""
		if a.cfg.EnableTokenAuth {
			raw = bearerToken(r)
		}
		if raw == "" {
			next.ServeHTTP(w, r)
			return
		}
		tok, user, err := a.resolveToken(r.Context(), raw)
		if err != nil {
			if !errors.Is(err, errBadCredentials) {
				a.log.Error("steward: token lookup", "err", err)
			}
			// A presented-but-unusable token is an explicit failure: falling
			// back to the session would mask a stale credential.
			a.deny(w, r, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		a.touchToken(r.Context(), tok)
		ctx := context.WithValue(r.Context(), ctxKeyUser, user)
		ctx = context.WithValue(ctx, ctxKeyToken, tok)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// tokenEndpoint reports the credential-issuing path, which is exempt from CSRF
// (it is authenticated by the body, not by an ambient cookie).
func (a *Admin) tokenEndpoint(p string) bool {
	rel := "/" + strings.TrimLeft(strings.TrimPrefix(p, a.cfg.Prefix), "/")
	return rel == "/auth/token"
}

type tokenIssueRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

// issueToken mints a token from a username and password: the login step for a
// mobile app or a script. Public and CSRF-exempt by necessity — the caller has
// no session yet.
//
// This endpoint accepts passwords, so it is a brute-force target. Steward does
// not rate-limit it; put a limiter in front of {Prefix}/auth/token in any
// internet-facing deployment.
func (a *Admin) issueToken(c *Context) error {
	var req tokenIssueRequest
	if strings.Contains(c.R.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(http.MaxBytesReader(c.W, c.R.Body, 4<<10)).Decode(&req); err != nil {
			return c.JSON(http.StatusBadRequest, Error("Malformed JSON body."))
		}
	} else {
		if err := c.R.ParseForm(); err != nil {
			return c.JSON(http.StatusBadRequest, Error("Malformed form body."))
		}
		req.Username = c.R.PostFormValue("username")
		req.Password = c.R.PostFormValue("password")
		req.Name = c.R.PostFormValue("name")
	}

	user, err := a.authenticate(c.Ctx(), strings.TrimSpace(req.Username), req.Password)
	if errors.Is(err, errBadCredentials) {
		return c.JSON(http.StatusUnauthorized, Error("These credentials do not match our records."))
	}
	if err != nil {
		return err
	}

	raw, err := newTokenValue()
	if err != nil {
		return err
	}
	tok := AdminToken{
		UserID: user.ID,
		Name:   strings.TrimSpace(req.Name),
		Hash:   tokenHash(raw),
	}
	if ttl := a.tokenTTL(); ttl > 0 {
		exp := time.Now().Add(ttl)
		tok.ExpiresAt = &exp
	}
	if err := a.db.WithContext(c.Ctx()).Create(&tok).Error; err != nil {
		return err
	}
	a.log.Info("steward: token issued", "user", user.Username, "name", tok.Name)

	out := map[string]any{
		// The only time the raw value is ever available; only its hash is kept.
		"token": raw,
		"user": map[string]any{
			"id": user.ID, "username": user.Username, "name": displayName(user),
		},
	}
	if tok.ExpiresAt != nil {
		out["expires_at"] = tok.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return c.JSON(http.StatusCreated, out)
}

// revokeToken deletes the token that authenticated the request: logout for a
// client that holds no cookie.
func (a *Admin) revokeToken(c *Context) error {
	tok := tokenOf(c.R)
	if tok == nil {
		return c.JSON(http.StatusUnauthorized, Error("Present the token you want to revoke as a bearer credential."))
	}
	if err := a.db.WithContext(c.Ctx()).Delete(&AdminToken{}, tok.ID).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, Info("Token revoked."))
}
