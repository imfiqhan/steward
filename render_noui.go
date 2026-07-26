//go:build no_ui

package steward

import "net/http"

// With the no_ui build tag the embedded templates and assets are compiled
// out entirely (smaller binaries for headless/API-only deployments). Every
// HTML route answers 503; the JSON endpoints keep working.
type renderer struct {
	assetVersion string
}

func newRenderer(a *Admin) (*renderer, error) {
	return &renderer{assetVersion: "noui"}, nil
}

func (a *Admin) render(c *Context, name, title string, data any) error {
	return a.noUI(c)
}

func (a *Admin) renderStandalone(c *Context, name string, data any) error {
	return a.noUI(c)
}

func (a *Admin) noUI(c *Context) error {
	http.Error(c.W, "steward: built with the no_ui tag — HTML UI unavailable; use the JSON API", http.StatusServiceUnavailable)
	return nil
}

func (a *Admin) serveAsset(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

// BuiltinTemplates is empty under the no_ui tag.
func BuiltinTemplates() fs.FS { return emptyFS{} }

// BuiltinAssets is empty under the no_ui tag.
func BuiltinAssets() fs.FS { return emptyFS{} }
