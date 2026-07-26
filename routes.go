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

	mux.HandleFunc("GET "+p+"/auth/login", a.h(a.loginPage))
	mux.HandleFunc("POST "+p+"/auth/login", a.h(a.loginSubmit))
	mux.HandleFunc("POST "+p+"/auth/logout", a.h(a.logoutHandler))
	mux.HandleFunc("GET "+p+"/auth/profile", a.h(a.profilePage))
	mux.HandleFunc("POST "+p+"/auth/profile", a.h(a.profileSubmit))
	mux.HandleFunc("PUT "+p+"/auth/profile", a.h(a.profileSubmit))

	mux.HandleFunc("GET "+p+"/_assets/", a.serveAsset)

	// Local uploads are served straight from disk; other Storage backends
	// give absolute URLs and never hit this route.
	if ls, ok := a.cfg.Storage.(*LocalStorage); ok {
		fileServer := http.StripPrefix(p+"/_uploads/", http.FileServer(http.Dir(ls.Dir)))
		mux.Handle("GET "+p+"/_uploads/", fileServer)
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

func (a *Admin) dashboard(c *Context) error {
	return a.render(c, "pages/dashboard.html", "Dashboard", map[string]any{
		"ResourceCount": len(a.registry),
	})
}
