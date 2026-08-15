package steward

import (
	"net/http"
)

// handlerFunc is the signature every Steward page/action handler uses.
type handlerFunc func(c *Context) error

// h adapts a handlerFunc onto net/http, building the Context from what the
// middleware chain resolved and rendering returned errors.
func (a *Admin) h(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := &Context{W: w, R: r, Admin: a, User: userOf(r), sess: sessionOf(r)}
		if err := fn(c); err != nil {
			a.log.Error("steward: handler", "path", r.URL.Path, "err", err)
			a.renderError(c, http.StatusInternalServerError, "Something went wrong", err)
		}
	}
}

// renderError writes the error page (or envelope) without failing further.
func (a *Admin) renderError(c *Context, code int, title string, err error) {
	if c.WantsJSON() {
		_ = c.JSON(code, Error(title))
		return
	}
	detail := ""
	if err != nil && a.cfg.Dev {
		detail = err.Error()
	}
	c.W.WriteHeader(code)
	rerr := a.render(c, "pages/error.html", title, map[string]any{
		"Code":   code,
		"Title":  title,
		"Detail": detail,
	})
	if rerr != nil {
		a.log.Error("steward: error page render", "err", rerr)
	}
}

// buildRoutes constructs the panel's route table on a net/http ServeMux.
// Every pattern is registered with the full prefix so the Admin can be
// mounted on any router without path rewriting.
func (a *Admin) buildRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	p := a.cfg.Prefix

	// Bare prefix → canonical trailing-slash home.
	mux.HandleFunc("GET "+p+"/{$}", a.h(a.dashboard))
	mux.Handle("GET "+p, http.RedirectHandler(p+"/", http.StatusMovedPermanently))

	// Lazy dashboard widgets fetch their own tile.
	mux.HandleFunc("GET "+p+"/_widget/{index}", a.h(a.widgetFragment))

	mux.HandleFunc("GET "+p+"/auth/login", a.h(a.loginPage))
	mux.HandleFunc("POST "+p+"/auth/login", a.h(a.loginSubmit))
	if a.cfg.Mailer != nil {
		mux.HandleFunc("GET "+p+"/auth/forgot", a.h(a.forgotPage))
		mux.HandleFunc("POST "+p+"/auth/forgot", a.h(a.forgotSubmit))
		mux.HandleFunc("GET "+p+"/auth/reset", a.h(a.resetPage))
		mux.HandleFunc("POST "+p+"/auth/reset", a.h(a.resetSubmit))
	}
	if a.cfg.EnableTokenAuth {
		mux.HandleFunc("POST "+p+"/auth/token", a.h(a.issueToken))
		mux.HandleFunc("DELETE "+p+"/auth/token", a.h(a.revokeToken))
	}
	// The two-factor challenge is reachable without a session — the caller has
	// passed the password but is not authenticated yet.
	mux.HandleFunc("GET "+p+"/auth/2fa", a.h(a.twoFactorChallengePage))
	mux.HandleFunc("POST "+p+"/auth/2fa", a.h(a.twoFactorChallengeSubmit))

	mux.HandleFunc("POST "+p+"/auth/logout", a.h(a.logoutHandler))
	mux.HandleFunc("GET "+p+"/auth/profile", a.h(a.profilePage))
	mux.HandleFunc("POST "+p+"/auth/profile", a.h(a.profileSubmit))
	mux.HandleFunc("PUT "+p+"/auth/profile", a.h(a.profileSubmit))

	// Enrolment writes a secret, so every step is a POST rather than a GET.
	mux.HandleFunc("POST "+p+"/auth/profile/2fa/enable", a.h(a.twoFactorEnableStart))
	mux.HandleFunc("POST "+p+"/auth/profile/2fa/confirm", a.h(a.twoFactorConfirm))
	mux.HandleFunc("POST "+p+"/auth/profile/2fa/disable", a.h(a.twoFactorDisable))
	mux.HandleFunc("POST "+p+"/auth/profile/2fa/codes", a.h(a.twoFactorRegenerateCodes))

	mux.HandleFunc("GET "+p+"/_assets/", a.serveAsset)

	// Local uploads are served straight from disk; other Storage backends
	// give absolute URLs and never hit this route.
	//
	// Behind uploadGuard: this used to sit outside the panel's authentication,
	// so anyone who knew a path could read any stored file without signing in.
	for _, name := range a.DiskNames() {
		d, _ := a.Disk(name)
		ls, ok := d.Storage.(*LocalStorage)
		if !ok {
			continue // an object store hands out its own URLs
		}
		route := a.uploadRoutePrefix(name)
		fileServer := http.StripPrefix(route, http.FileServer(http.Dir(ls.Dir)))
		mux.Handle("GET "+route, a.uploadGuard(name, uploadHeaders(fileServer)))
	}

	for _, res := range a.registry {
		res.registerRoutes(a, mux)
	}

	// Everything else under the prefix is a 404 page.
	mux.HandleFunc(p+"/", a.h(func(c *Context) error {
		a.renderError(c, http.StatusNotFound, "Page not found", nil)
		return nil
	}))
	return mux
}

// uploadHeaders makes a stored file inert.
//
// Uploads are user-supplied bytes served from the panel's own origin, and the
// file server types them from their extension — an uploaded .html came back as
// text/html and ran its script as the panel. Content-Disposition turns a visit
// to one into a download instead of a page, and nosniff stops a mistyped file
// being re-guessed into something executable.
//
// Neither header affects a subresource: Content-Disposition applies to
// navigation, so <img src> and <a download> keep working exactly as before.
func uploadHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", "attachment")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
