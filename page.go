package steward

import (
	"html/template"
	"io"
	"io/fs"
	"net/http"

	"github.com/imfiqhan/steward/internal/session"
)

// What the panel needs whether or not it was built with a UI. The renderer and
// the templates live behind the no_ui build tag; a page's view model, the
// theme cookie and the layered-asset readers do not — a headless build still
// answers JSON from the same Context and still reads its own assets when a
// caller hands it any.

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

// pageMeta is the layout-level data every template can reach via .Page.
type pageMeta struct {
	Brand     string
	BrandIcon string
	Title     string
	Prefix    string

	// ThemeCSS is Config.ThemeCSS, typed so the head emits it as CSS rather
	// than escaping it as text.
	ThemeCSS template.CSS

	// Notifications reports whether the bell is mounted, so the header can
	// leave the control out entirely when the feature is off.
	Notifications bool
	CSRF          string
	Theme         string
	Dev           bool
	User          *AdminUser
	// Menu is the raw tree. MenuSections is the same entries batched for
	// rendering; the shipped sidebar uses the latter, and Menu stays so an
	// overridden sidebar template keeps working.
	Menu         []MenuNode
	MenuSections []MenuSection
	Flashes      []session.Flash
	Path         string
}

// themeCookie stores the viewer's light/dark preference.
const themeCookie = "steward_theme"

func themeFrom(req *http.Request) string {
	if ck, err := req.Cookie(themeCookie); err == nil && ck.Value == "dark" {
		return "dark"
	}
	return "light"
}

func (a *Panel) pageMetaFor(c *Context, title string) pageMeta {
	// Built once and shared: the sections are a view of the same tree.
	menu := a.buildMenu(c)
	return pageMeta{
		Brand:         a.cfg.Brand,
		BrandIcon:     a.cfg.BrandIcon,
		Title:         title,
		Prefix:        a.cfg.Prefix,
		ThemeCSS:      template.CSS(a.cfg.ThemeCSS),
		Notifications: a.notificationsEnabled(),
		CSRF:          c.CSRF(),
		Theme:         themeFrom(c.R),
		Dev:           a.cfg.Dev,
		User:          c.User,
		Menu:          menu,
		MenuSections:  menuSections(menu),
		Flashes:       c.takeFlashes(),
		Path:          c.R.URL.Path,
	}
}
