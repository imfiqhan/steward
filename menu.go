package steward

import (
	"strings"

	"github.com/imfiqhan/steward/internal/httpmatch"
)

// MenuNode is one rendered sidebar entry.
type MenuNode struct {
	Title    string
	Icon     string
	URI      string // absolute path ("" for group headers)
	Active   bool
	Children []MenuNode
}

// buildMenu renders the sidebar from admin_menu (kept in sync with the
// registered resources at Build, editable at auth/menu). Visibility derives
// from the same permission rules that guard the routes: an entry the user
// cannot GET is not shown — no separate role↔menu bookkeeping.
func (a *Admin) buildMenu(c *Context) []MenuNode {
	items, err := a.menuItems(c.Ctx())
	if err != nil {
		a.log.Error("steward: menu", "err", err)
		return nil
	}

	isAdmin := c.User != nil && c.User.IsAdministrator()
	allowed := func(uri string) bool {
		if uri == "" {
			return false
		}
		// A registered resource's policy overrides everything — policies
		// bind administrators too (checked before the isAdmin shortcut).
		if entry, ok := a.bySlug[strings.Trim(uri, "/")]; ok && !entry.menuVisible(c) {
			return false
		}
		if uri == "/" || isAdmin {
			return true
		}
		return httpmatch.Matches(c.permissionRules(), "GET", uri)
	}

	current := c.R.URL.Path
	toNode := func(it *MenuItem) MenuNode {
		abs := a.cfg.Prefix + "/" + strings.TrimLeft(it.URI, "/")
		if it.URI == "/" || it.URI == "" {
			abs = a.cfg.Prefix + "/"
		}
		active := it.URI != "" && (current == abs || (abs != a.cfg.Prefix+"/" && strings.HasPrefix(current, abs+"/")))
		if it.URI == "/" {
			active = current == a.cfg.Prefix+"/" || current == a.cfg.Prefix
		}
		return MenuNode{Title: it.Title, Icon: it.Icon, URI: abs, Active: active}
	}

	var roots []MenuNode
	for i := range items {
		it := &items[i]
		if it.ParentID != 0 || !it.Show {
			continue
		}
		// Collect visible children.
		var children []MenuNode
		childActive := false
		for j := range items {
			ch := &items[j]
			if ch.ParentID != it.ID || !ch.Show || !allowed(ch.URI) {
				continue
			}
			node := toNode(ch)
			childActive = childActive || node.Active
			children = append(children, node)
		}
		switch {
		case len(children) > 0:
			node := toNode(it)
			node.URI = "" // groups render as disclosure, not links
			node.Active = childActive
			node.Children = children
			roots = append(roots, node)
		case it.URI != "" && allowed(it.URI):
			roots = append(roots, toNode(it))
		}
	}
	return roots
}
