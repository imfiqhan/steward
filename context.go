package steward

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/imfiqhan/steward/internal/session"
)

// Context wraps one admin request. Handlers receive it instead of the raw
// (w, r) pair and return an error; the router renders returned errors as the
// error page or an error envelope depending on the client.
type Context struct {
	W     http.ResponseWriter
	R     *http.Request
	Admin *Admin

	// User is the authenticated account, nil on public routes (login).
	User *AdminUser

	sess *session.Data
}

// Ctx returns the request's context.Context for repository calls.
func (c *Context) Ctx() context.Context { return c.R.Context() }

// WantsFragment reports an HTMX partial-navigation request: the response
// should be the page content only, not the full layout. Boosted requests are
// full-page swaps and still want the fragment (layout stays put).
func (c *Context) WantsFragment() bool {
	return c.R.Header.Get("HX-Request") == "true" && c.R.Header.Get("HX-History-Restore-Request") != "true"
}

// WantsJSON reports Accept-header preference for JSON (the headless API) or
// an XMLHttpRequest-style client.
func (c *Context) WantsJSON() bool {
	accept := c.R.Header.Get("Accept")
	if strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html") {
		return true
	}
	return c.R.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

// CSRF returns the session's CSRF token (issued by the session middleware).
func (c *Context) CSRF() string {
	if c.sess == nil {
		return ""
	}
	return c.sess.CSRF
}

// Flash queues a one-shot message shown on the next rendered page. It
// persists the session cookie immediately, so call it before writing a body.
func (c *Context) Flash(typ, msg string) {
	if c.sess == nil {
		return
	}
	c.sess.Flashes = append(c.sess.Flashes, session.Flash{Type: typ, Message: msg})
	c.Admin.saveSession(c)
}

// takeFlashes drains queued flashes (persisting the drain) for rendering.
func (c *Context) takeFlashes() []session.Flash {
	if c.sess == nil || len(c.sess.Flashes) == 0 {
		return nil
	}
	f := c.sess.Flashes
	c.sess.Flashes = nil
	c.Admin.saveSession(c)
	return f
}

// login binds the session to a user id and rotates the CSRF token.
func (c *Context) login(uid uint) error {
	tok, err := session.NewToken()
	if err != nil {
		return err
	}
	c.sess.UID = uid
	c.sess.CSRF = tok
	c.sess.IssuedAt = 0 // re-stamp at encode
	c.Admin.saveSession(c)
	return nil
}

// logout clears the session.
func (c *Context) logout() {
	c.sess = &session.Data{}
	c.Admin.clearSession(c)
}

// JSON writes v with the given status.
func (c *Context) JSON(status int, v any) error {
	c.W.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.W.WriteHeader(status)
	return json.NewEncoder(c.W).Encode(v)
}

// Envelope writes the unified mutation response.
func (c *Context) Envelope(e *Envelope) error {
	code := e.code
	if code == 0 {
		code = http.StatusOK
	}
	return c.JSON(code, e)
}

// Redirect sends a client-appropriate redirect: HX-Redirect header for HTMX
// requests (full navigation client-side), 302 otherwise.
func (c *Context) Redirect(url string) error {
	if c.R.Header.Get("HX-Request") == "true" {
		c.W.Header().Set("HX-Redirect", url)
		c.W.WriteHeader(http.StatusNoContent)
		return nil
	}
	http.Redirect(c.W, c.R, url, http.StatusFound)
	return nil
}

// URL joins path segments onto the admin prefix ("/admin", "auth/login" →
// "/admin/auth/login").
func (c *Context) URL(parts ...string) string {
	return c.Admin.url(parts...)
}
