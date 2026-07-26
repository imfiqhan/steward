package steward

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"gorm.io/gorm/clause"
)

// groupCodeKey names the synthetic parent row of a sidebar group.
func groupCodeKey(group string) string { return "_group_" + group }

// syncMenu reconciles registered resources into admin_menu at Build:
//
//   - missing entries are inserted with source="code" and code_key=slug
//   - existing code entries update unless an admin edited them in the UI
//     (overridden=true)
//   - code entries whose resource vanished are hidden, never deleted
//   - hand-created (source="db") rows are never touched
func (a *Admin) syncMenu(ctx context.Context) error {
	var existing []MenuItem
	if err := a.db.WithContext(ctx).Find(&existing).Error; err != nil {
		return err
	}
	byKey := map[string]*MenuItem{}
	for i := range existing {
		if existing[i].CodeKey != nil {
			byKey[*existing[i].CodeKey] = &existing[i]
		}
	}

	seen := map[string]bool{"_dashboard": true}
	order := 10
	groupID := map[string]uint{}

	upsert := func(key, title, icon, uri string, parent uint, ord int) (uint, error) {
		seen[key] = true
		if row, ok := byKey[key]; ok {
			if !row.Overridden {
				updates := map[string]any{
					"title": title, "icon": icon, "uri": uri,
					"parent_id": parent, "order": ord, "show": true,
				}
				if err := a.db.WithContext(ctx).Model(row).Updates(updates).Error; err != nil {
					return 0, err
				}
			}
			return row.ID, nil
		}
		k := key
		row := MenuItem{
			ParentID: parent, Order: ord, Title: title, Icon: icon, URI: uri,
			Show: true, Source: MenuSourceCode, CodeKey: &k,
		}
		if err := a.db.WithContext(ctx).Create(&row).Error; err != nil {
			return 0, err
		}
		byKey[key] = &row
		return row.ID, nil
	}

	for _, entry := range a.registry {
		m := entry.meta()
		parent := uint(0)
		if m.group != "" {
			gid, ok := groupID[m.group]
			if !ok {
				var err error
				gid, err = upsert(groupCodeKey(m.group), m.group, "folder", "", 0, order)
				if err != nil {
					return err
				}
				groupID[m.group] = gid
				order += 10
			}
			parent = gid
		}
		if _, err := upsert(m.slug, m.title, m.icon, "/"+m.slug, parent, order); err != nil {
			return err
		}
		order += 10
	}

	// Hide code rows whose resource no longer exists.
	for key, row := range byKey {
		if seen[key] || row.Source != MenuSourceCode {
			continue
		}
		a.log.Warn("steward: menu entry has no matching resource; hiding", "code_key", key)
		if err := a.db.WithContext(ctx).Model(row).Update("show", false).Error; err != nil {
			return err
		}
	}
	return a.flushMenuCache(ctx)
}

const menuCacheKey = "steward:menu"

func (a *Admin) flushMenuCache(ctx context.Context) error {
	return a.cfg.Cache.Delete(ctx, menuCacheKey)
}

// registerMenuResource is the menu administration page.
func (a *Admin) registerMenuResource() {
	res := Register[MenuItem](a).Slug("auth/menu").Title("Menu").
		Icon("menu-2").Group("Admin")

	res.Grid(func(g *Grid[MenuItem]) {
		g.Column("ID").Sortable().Width(60)
		g.Column("Title")
		g.Column("URI")
		g.Column("Icon")
		g.Column("Order").Sortable()
		g.Column("ParentID", "Parent")
		g.Column("Show").Bool()
		g.Column("Source").Badge(map[any]string{"code": "azure", "db": "secondary"})
		g.DefaultSort("Order", false)
		g.DisableExport()
	})

	parentOptions := func(c *Context) Options {
		var items []MenuItem
		o := Options{}
		if err := c.Admin.db.WithContext(c.Ctx()).
			Where("parent_id = 0").
			Order(clause.OrderByColumn{Column: clause.Column{Name: "order"}}).
			Find(&items).Error; err != nil {
			return o
		}
		for _, it := range items {
			o[strconv.FormatUint(uint64(it.ID), 10)] = it.Title
		}
		return o
	}

	res.Form(func(f *Form[MenuItem]) {
		f.Text("Title").Rules("required|max:100")
		f.Text("Icon").Help("A Tabler icon name, e.g. \"news\" — see tabler.io/icons.")
		f.Text("URI").Placeholder("/posts").
			Help("Relative to the admin prefix; leave empty for group headers.")
		f.Select("ParentID", "Parent").OptionsFunc(parentOptions).
			Help("Leave empty for a top-level entry.")
		f.Number("Order")
		f.Switch("Show")
		f.Saved(func(c *Context, m *MenuItem, created bool) error {
			// UI edits pin code-sourced rows against future syncs.
			if !created && m.Source == MenuSourceCode && !m.Overridden {
				if err := c.Admin.db.WithContext(c.Ctx()).Model(m).Update("overridden", true).Error; err != nil {
					return err
				}
			}
			return c.Admin.flushMenuCache(c.Ctx())
		})
		f.Deleted(func(c *Context, _ []string) error {
			return c.Admin.flushMenuCache(c.Ctx())
		})
	})

	res.Detail(func(d *Detail[MenuItem]) {
		d.Field("ID")
		d.Field("Title")
		d.Field("URI")
		d.Field("Icon")
		d.Field("Order")
		d.Field("Show").Bool()
		d.Field("Source")
		d.Field("Overridden").Bool()
	})
}

// menuItems loads the full menu table, cached.
func (a *Admin) menuItems(ctx context.Context) ([]MenuItem, error) {
	if raw, ok, _ := a.cfg.Cache.Get(ctx, menuCacheKey); ok {
		var items []MenuItem
		if err := json.Unmarshal(raw, &items); err == nil {
			return items, nil
		}
	}
	var items []MenuItem
	if err := a.db.WithContext(ctx).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "order"}}).
		Order("id").Find(&items).Error; err != nil {
		return nil, fmt.Errorf("loading menu: %w", err)
	}
	if raw, err := json.Marshal(items); err == nil {
		_ = a.cfg.Cache.Set(ctx, menuCacheKey, raw, 0)
	}
	return items, nil
}
