//go:build !no_ui

package steward

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/imfiqhan/steward/internal/session"
)

//go:embed all:templates
var templatesFS embed.FS

// renderer executes page templates through the overlay filesystem: the user's
// Config.TemplatesFS/AssetsFS layers win over the embedded defaults, and the
// same resolution path runs in dev and production — overriding a template is
// always "drop a file at the documented relative path".
type renderer struct {
	a            *Admin
	tmplLayers   []fs.FS
	assetLayers  []fs.FS
	assetVersion string

	mu   sync.RWMutex
	tmpl *template.Template

	iconMu sync.RWMutex
	icons  map[string]template.HTML
}

func newRenderer(a *Admin) (*renderer, error) {
	embTmpl, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		return nil, err
	}
	embAssets, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return nil, err
	}
	r := &renderer{a: a, icons: map[string]template.HTML{}}
	if a.cfg.TemplatesFS != nil {
		r.tmplLayers = append(r.tmplLayers, a.cfg.TemplatesFS)
	}
	r.tmplLayers = append(r.tmplLayers, embTmpl)
	if a.cfg.AssetsFS != nil {
		r.assetLayers = append(r.assetLayers, a.cfg.AssetsFS)
	}
	r.assetLayers = append(r.assetLayers, embAssets)

	if a.cfg.Dev {
		r.assetVersion = "dev"
	} else {
		r.assetVersion = hashLayers(r.assetLayers)
	}
	if err := r.parse(); err != nil {
		return nil, err
	}
	return r, nil
}

// open resolves a relative path through the layers, first hit wins.
func openLayered(layers []fs.FS, name string) (fs.File, error) {
	for _, l := range layers {
		if f, err := l.Open(name); err == nil {
			return f, nil
		}
	}
	return nil, fs.ErrNotExist
}

func readLayered(layers []fs.FS, name string) ([]byte, error) {
	f, err := openLayered(layers, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// names returns the union of *.html paths across all layers.
func templateNames(layers []fs.FS) []string {
	seen := map[string]bool{}
	for _, l := range layers {
		_ = fs.WalkDir(l, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
				return nil
			}
			seen[p] = true
			return nil
		})
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// parse builds one template set where every template is named by its full
// relative path ("layout/sidebar.html"), sidestepping ParseFS's basename
// collisions and making override paths self-documenting.
func (r *renderer) parse() error {
	root := template.New("").Funcs(r.funcs())
	for _, name := range templateNames(r.tmplLayers) {
		content, err := readLayered(r.tmplLayers, name)
		if err != nil {
			return fmt.Errorf("reading template %s: %w", name, err)
		}
		if _, err := root.New(name).Parse(string(content)); err != nil {
			return fmt.Errorf("parsing template %s: %w", name, err)
		}
	}
	r.mu.Lock()
	r.tmpl = root
	r.mu.Unlock()
	return nil
}

func (r *renderer) funcs() template.FuncMap {
	return template.FuncMap{
		"asset": func(p string) string {
			return r.a.url("_assets", r.assetVersion, p)
		},
		"url": func(parts ...string) string {
			return r.a.url(parts...)
		},
		"icon": r.icon,
		"safe": func(s string) template.HTML { return template.HTML(s) },
		// dict builds a map inline for widget partials:
		// {{template "widgets/metric.html" (dict "Label" "Users" "Value" 42)}}
		"dict": func(pairs ...any) (map[string]any, error) {
			if len(pairs)%2 != 0 {
				return nil, fmt.Errorf("dict: odd number of arguments")
			}
			m := make(map[string]any, len(pairs)/2)
			for i := 0; i < len(pairs); i += 2 {
				k, ok := pairs[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key %v is not a string", pairs[i])
				}
				m[k] = pairs[i+1]
			}
			return m, nil
		},
	}
}

// icon inlines an embedded Tabler SVG with extra classes appended. Unknown
// names render an empty span so a typo can't break a page.
func (r *renderer) icon(name string, classes ...string) template.HTML {
	key := name + "|" + strings.Join(classes, " ")
	if !r.a.cfg.Dev {
		r.iconMu.RLock()
		if svg, ok := r.icons[key]; ok {
			r.iconMu.RUnlock()
			return svg
		}
		r.iconMu.RUnlock()
	}
	raw, err := readLayered(r.assetLayers, "icons/"+name+".svg")
	if err != nil {
		return template.HTML(`<span class="icon"></span>`)
	}
	svg := string(raw)
	if extra := strings.Join(classes, " "); extra != "" {
		svg = strings.Replace(svg, `class="`, `class="`+extra+` `, 1)
	}
	out := template.HTML(svg)
	if !r.a.cfg.Dev {
		r.iconMu.Lock()
		r.icons[key] = out
		r.iconMu.Unlock()
	}
	return out
}

// hashLayers fingerprints asset content for cache-busting URLs.
func hashLayers(layers []fs.FS) string {
	h := sha256.New()
	for _, name := range templateNamesAll(layers) {
		b, err := readLayered(layers, name)
		if err != nil {
			continue
		}
		h.Write([]byte(name))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// templateNamesAll lists every file (not just .html) across layers.
func templateNamesAll(layers []fs.FS) []string {
	seen := map[string]bool{}
	for _, l := range layers {
		_ = fs.WalkDir(l, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			seen[p] = true
			return nil
		})
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// pageMeta is the layout-level data every template can reach via .Page.
type pageMeta struct {
	Brand   string
	Title   string
	Prefix  string
	CSRF    string
	Theme   string
	Dev     bool
	User    *AdminUser
	Menu    []MenuNode
	Flashes []session.Flash
	Path    string
}

// execute runs one named template with {Page, Data}.
func (r *renderer) execute(w io.Writer, name string, page pageMeta, data any) error {
	if r.a.cfg.Dev {
		if err := r.parse(); err != nil {
			return err
		}
	}
	r.mu.RLock()
	t := r.tmpl.Lookup(name)
	r.mu.RUnlock()
	if t == nil {
		return fmt.Errorf("steward: template %q not found", name)
	}
	return t.Execute(w, map[string]any{"Page": page, "Data": data})
}

// themeCookie stores the viewer's light/dark preference.
const themeCookie = "steward_theme"

func themeFrom(req *http.Request) string {
	if ck, err := req.Cookie(themeCookie); err == nil && ck.Value == "dark" {
		return "dark"
	}
	return "light"
}

func (a *Admin) pageMetaFor(c *Context, title string) pageMeta {
	return pageMeta{
		Brand:   a.cfg.Brand,
		Title:   title,
		Prefix:  a.cfg.Prefix,
		CSRF:    c.CSRF(),
		Theme:   themeFrom(c.R),
		Dev:     a.cfg.Dev,
		User:    c.User,
		Menu:    a.buildMenu(c),
		Flashes: c.takeFlashes(),
		Path:    c.R.URL.Path,
	}
}

// render writes a page: content-only for HTMX fragment navigation (with an
// inline <title> htmx picks up), the full layout otherwise.
func (a *Admin) render(c *Context, name, title string, data any) error {
	page := a.pageMetaFor(c, title)

	var content bytes.Buffer
	if err := a.renderer.execute(&content, name, page, data); err != nil {
		return err
	}

	c.W.Header().Set("Vary", "HX-Request, Accept")
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	if c.WantsFragment() {
		_, _ = fmt.Fprintf(c.W, "<title>%s — %s</title>\n", template.HTMLEscapeString(title), template.HTMLEscapeString(page.Brand))
		var flash bytes.Buffer
		if err := a.renderer.execute(&flash, "layout/flash.html", page, nil); err != nil {
			return err
		}
		_, _ = c.W.Write(flash.Bytes())
		_, err := c.W.Write(content.Bytes())
		return err
	}
	page.Title = title
	return a.renderer.execute(c.W, "layout/base.html", page, map[string]any{
		"Content": template.HTML(content.String()),
	})
}

// renderStandalone executes a self-contained full-page template (login).
func (a *Admin) renderStandalone(c *Context, name string, data any) error {
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	return a.renderer.execute(c.W, name, a.pageMetaFor(c, a.cfg.Brand), data)
}

// overlayFS exposes the layer chain as one fs.FS (first hit wins).
type overlayFS []fs.FS

// Open implements fs.FS.
func (o overlayFS) Open(name string) (fs.File, error) { return openLayered(o, name) }

// serveAsset streams an embedded (or overlaid) static file. URLs carry a
// content-hash version segment ({prefix}/_assets/{version}/css/tabler.min.css)
// so cache headers can be immutable outside dev.
func (a *Admin) serveAsset(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, a.cfg.Prefix+"/_assets/")
	i := strings.IndexByte(rel, '/')
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	rel = rel[i+1:]
	if rel == "" || strings.Contains(rel, "..") {
		http.NotFound(w, r)
		return
	}
	if a.cfg.Dev {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeFileFS(w, r, overlayFS(a.renderer.assetLayers), rel)
}
