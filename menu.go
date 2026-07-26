package steward

import "strings"

// MenuNode is one rendered sidebar entry. Until milestone M5 wires the
// admin_menu table and policies in, the sidebar derives directly from the
// registered resources: groups become section headers, resources become
// links, Dashboard is always first.
type MenuNode struct {
	Title    string
	Icon     string
	URI      string // absolute path
	Active   bool
	Children []MenuNode
}

func (a *Admin) buildMenu(c *Context) []MenuNode {
	current := c.R.URL.Path
	nodes := []MenuNode{{
		Title:  "Dashboard",
		Icon:   "home",
		URI:    a.cfg.Prefix + "/",
		Active: current == a.cfg.Prefix+"/" || current == a.cfg.Prefix,
	}}

	grouped := map[string][]MenuNode{}
	var groupOrder []string
	for _, entry := range a.registry {
		m := entry.meta()
		node := MenuNode{
			Title:  m.title,
			Icon:   m.icon,
			URI:    a.url(m.slug),
			Active: current == a.url(m.slug) || strings.HasPrefix(current, a.url(m.slug)+"/"),
		}
		if m.group == "" {
			nodes = append(nodes, node)
			continue
		}
		if _, seen := grouped[m.group]; !seen {
			groupOrder = append(groupOrder, m.group)
		}
		grouped[m.group] = append(grouped[m.group], node)
	}
	for _, g := range groupOrder {
		children := grouped[g]
		active := false
		for _, ch := range children {
			if ch.Active {
				active = true
				break
			}
		}
		nodes = append(nodes, MenuNode{Title: g, Icon: "folder", Active: active, Children: children})
	}
	return nodes
}
