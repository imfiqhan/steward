package steward

import (
	"context"
	"net/http"
	"path"
	"runtime/debug"
	"strings"
	"time"

	"github.com/imfiqhan/steward/internal/session"
)

type ctxKey int

const (
	ctxKeySession ctxKey = iota
	ctxKeyUser
)

// wrap composes the fixed middleware chain around the route table:
// recover → access log → session → CSRF → auth → permission →
// operation log → routes.
func (a *Admin) wrap(next http.Handler) http.Handler {
	h := a.withOperationLog(next)
	h = a.withPermission(h)
	h = a.withAuth(h)
	h = a.withCSRF(h)
	h = a.withSession(h)
	h = a.withLogging(h)
	h = a.withRecover(h)
	return h
}

func (a *Admin) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				a.log.Error("steward: panic", "err", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	if sw.status == 0 {
		sw.status = code
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if sw.status == 0 {
		sw.status = http.StatusOK
	}
	return sw.ResponseWriter.Write(b)
}

func (a *Admin) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.isAssetPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(sw, r)
		a.log.Info("steward", "method", r.Method, "path", r.URL.Path, "status", sw.status, "dur", time.Since(start).Round(time.Millisecond))
	})
}

// withSession decodes the session cookie and guarantees a CSRF token exists,
// setting the cookie immediately when one is issued.
func (a *Admin) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := a.sessionFromRequest(r)
		if sess.CSRF == "" {
			if tok, err := session.NewToken(); err == nil {
				sess.CSRF = tok
				a.saveSessionW(w, r, sess)
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeySession, sess)))
	})
}

// withCSRF enforces the double-submit token on state-changing methods.
func (a *Admin) withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		sess := sessionOf(r)
		token := r.Header.Get("X-CSRF-Token")
		if token == "" {
			token = r.PostFormValue("_token")
		}
		if sess == nil || sess.CSRF == "" || token != sess.CSRF {
			a.deny(w, r, http.StatusForbidden, "invalid or missing CSRF token — reload the page and retry")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// publicPath reports paths that skip authentication: login, static assets,
// and configured AuthExcept globs (relative to the prefix).
func (a *Admin) publicPath(p string) bool {
	rel := strings.TrimPrefix(p, a.cfg.Prefix)
	rel = "/" + strings.TrimLeft(rel, "/")
	if rel == "/auth/login" || strings.HasPrefix(rel, "/_assets/") || strings.HasPrefix(rel, "/_uploads/") {
		return true
	}
	for _, pat := range a.cfg.AuthExcept {
		pat = "/" + strings.TrimLeft(pat, "/")
		if ok, err := path.Match(pat, rel); err == nil && ok {
			return true
		}
		if strings.HasSuffix(pat, "*") && strings.HasPrefix(rel, strings.TrimSuffix(pat, "*")) {
			return true
		}
	}
	return false
}

func (a *Admin) isAssetPath(p string) bool {
	rel := strings.TrimPrefix(p, a.cfg.Prefix)
	return strings.HasPrefix(rel, "/_assets/")
}

// withAuth resolves the session's user (roles preloaded) and gates
// non-public paths.
func (a *Admin) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := sessionOf(r)
		var user *AdminUser
		if sess != nil && sess.UID != 0 {
			var u AdminUser
			if err := a.db.WithContext(r.Context()).Preload("Roles").First(&u, sess.UID).Error; err == nil {
				user = &u
			}
		}
		if user == nil && !a.publicPath(r.URL.Path) {
			if wantsJSONLike(r) {
				a.deny(w, r, http.StatusUnauthorized, "authentication required")
				return
			}
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", a.url("auth/login"))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, a.url("auth/login"), http.StatusFound)
			return
		}
		if user != nil {
			r = r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user))
		}
		next.ServeHTTP(w, r)
	})
}

func sessionOf(r *http.Request) *session.Data {
	s, _ := r.Context().Value(ctxKeySession).(*session.Data)
	return s
}

func userOf(r *http.Request) *AdminUser {
	u, _ := r.Context().Value(ctxKeyUser).(*AdminUser)
	return u
}

func wantsJSONLike(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return (strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html")) ||
		r.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

// deny writes a small error response in the client's preferred shape.
func (a *Admin) deny(w http.ResponseWriter, r *http.Request, code int, msg string) {
	if wantsJSONLike(r) || r.Header.Get("HX-Request") == "true" {
		c := &Context{W: w, R: r, Admin: a}
		_ = c.JSON(code, Error(msg))
		return
	}
	http.Error(w, msg, code)
}

// saveSessionW is the writer-level session save used before a Context exists.
func (a *Admin) saveSessionW(w http.ResponseWriter, r *http.Request, sess *session.Data) {
	c := &Context{W: w, R: r, Admin: a, sess: sess}
	a.saveSession(c)
}
