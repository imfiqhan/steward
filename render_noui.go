//go:build no_ui

package steward

import (
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
)

// With the no_ui build tag the embedded templates and assets are compiled out
// entirely (smaller binaries for headless/API-only deployments). Every HTML
// route answers 503; the JSON endpoints keep working.
//
// The type carries the same fields and methods as the real renderer, because
// the rest of the package is built either way and reaches for them: the
// dashboard executes a template, a grid asks for an icon, Verify reads the
// stylesheet. Each answers the way a panel with nothing to render should.
type renderer struct {
	assetVersion string

	// Nil, and both are read rather than called: an icon set that resolves
	// nothing, and no layers to read an asset out of.
	iconSet     *iconSet
	assetLayers []fs.FS
}

func newRenderer(a *Panel) (*renderer, error) {
	// Config.AssetsFS is still honoured: a headless panel may serve files even
	// though it renders no pages.
	var layers []fs.FS
	if a.cfg.AssetsFS != nil {
		layers = append(layers, a.cfg.AssetsFS)
	}
	return &renderer{assetVersion: "noui", assetLayers: layers}, nil
}

// execute refuses rather than writing a blank page, so a caller that renders
// without going through Panel.render gets an error it can report.
func (r *renderer) execute(w io.Writer, name string, page pageMeta, data any) error {
	return fmt.Errorf("steward: built with the no_ui tag, cannot render %q", name)
}

// icon renders nothing. The sprite it would draw from is not in this binary.
func (r *renderer) icon(name string, classes ...string) template.HTML { return "" }

// hasIcon answers yes to every name.
//
// It is what Verify consults, and there is no sprite here to check against. A
// name cannot be wrong in a build where nothing draws it, and reporting every
// icon in a panel as missing would bury the findings that do matter.
func (r *renderer) hasIcon(name string) bool { return true }

func (a *Panel) render(c *Context, name, title string, data any) error {
	return a.noUI(c)
}

func (a *Panel) renderStandalone(c *Context, name string, data any) error {
	return a.noUI(c)
}

func (a *Panel) noUI(c *Context) error {
	http.Error(c.W, "steward: built with the no_ui tag — HTML UI unavailable; use the JSON API", http.StatusServiceUnavailable)
	return nil
}

func (a *Panel) serveAsset(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

// BuiltinTemplates is empty under the no_ui tag.
func BuiltinTemplates() fs.FS { return emptyFS{} }

// BuiltinAssets is empty under the no_ui tag.
func BuiltinAssets() fs.FS { return emptyFS{} }
