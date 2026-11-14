package steward

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// permissionDef is one code-owned permission row, before it is written.
type permissionDef struct {
	key    string // CodeKey: "<resource slug>" or "<resource slug>#<operation>"
	parent string // the parent's key, "" for a resource's root row
	slug   string
	name   string
	method string
	path   string
}

// pageLister exposes a resource's custom pages to the permission sync, which
// sees resources only through the type-erased registry.
type pageLister interface {
	customPages() []customPage
}

func (t *typedResource[T]) customPages() []customPage { return t.res.pages }

// permissionSlug turns a resource slug into a permission slug that passes the
// form's alpha_dash rule: "auth/users" becomes "auth-users".
func permissionSlug(resourceSlug string) string {
	return strings.ReplaceAll(resourceSlug, "/", "-")
}

// permissionDefs lists the canonical permissions for every registered
// resource, parents before their children.
//
// Paths are relative to the panel prefix. The root row grants the whole
// resource; the four operations beneath it each grant one kind of request,
// and the endpoints a form or grid calls on its own (_upload, _action, …) are
// already checked as the operation they serve.
func (a *Panel) permissionDefs() []permissionDef {
	var defs []permissionDef
	for _, entry := range a.registry {
		m := entry.meta()
		base := "/" + m.slug
		slug := permissionSlug(m.slug)

		update := []string{"GET:" + base + "/*/edit", "PUT,PATCH:" + base + "/*"}
		if pl, ok := entry.(pageLister); ok {
			// A custom page that writes is update-level: Reorderable's _order
			// is the typical one. GET pages already fall under view.
			for _, p := range pl.customPages() {
				if p.method != "GET" && p.method != "HEAD" {
					update = append(update, p.method+":"+base+"/"+p.rel)
				}
			}
		}

		defs = append(defs,
			permissionDef{key: m.slug, slug: slug, name: m.title,
				path: base + "\n" + base + "/*"},
			permissionDef{key: m.slug + "#view", parent: m.slug, slug: slug + "-view",
				name: m.title + ": view", method: "GET", path: base + "\n" + base + "/*"},
			permissionDef{key: m.slug + "#create", parent: m.slug, slug: slug + "-create",
				name: m.title + ": create", path: "GET:" + base + "/create\nPOST:" + base},
			permissionDef{key: m.slug + "#update", parent: m.slug, slug: slug + "-update",
				name: m.title + ": update", path: strings.Join(update, "\n")},
			permissionDef{key: m.slug + "#delete", parent: m.slug, slug: slug + "-delete",
				name: m.title + ": delete", path: "DELETE:" + base + "/*"},
		)
	}
	return defs
}

// PermissionSyncResult reports what one SyncPermissions run changed.
type PermissionSyncResult struct {
	Created int
	Updated int
	// Skipped are definitions not written, each with the reason: a hand-made
	// row already holds the slug, or a name or slug exceeds its column.
	Skipped []string
	// Orphaned are code keys whose resource is no longer registered. Their
	// rows stay, since roles may still be granted them.
	Orphaned []string
}

// SyncPermissions reconciles the permission table with the registered
// resources, the way the menu is reconciled at Build:
//
//   - a missing definition is inserted with source "code"
//   - an existing code row is rewritten unless it was edited in the UI
//     (overridden)
//   - a code row whose resource is gone is reported, never deleted: roles may
//     still hold it
//   - hand-made rows (source "db") are never touched
//
// Role grants are not written. Which role holds what stays a decision made in
// the database.
func (a *Panel) SyncPermissions(ctx context.Context) (*PermissionSyncResult, error) {
	if err := a.Build(); err != nil {
		return nil, err
	}
	return a.syncPermissions(ctx)
}

// syncPermissions is SyncPermissions for a panel that is already being built.
func (a *Panel) syncPermissions(ctx context.Context) (*PermissionSyncResult, error) {
	ctx = WithoutTenant(ctx)
	db := a.db.WithContext(ctx)
	if err := checkPermissionTable(db); err != nil {
		return nil, err
	}
	res := &PermissionSyncResult{}

	var existing []Permission
	if err := db.Find(&existing).Error; err != nil {
		return nil, err
	}
	byKey := map[string]*Permission{}
	bySlug := map[string]*Permission{}
	for i := range existing {
		p := &existing[i]
		bySlug[p.Slug] = p
		if p.CodeKey != nil {
			byKey[*p.CodeKey] = p
		}
	}

	seen := map[string]bool{}
	ids := map[string]uint{}
	for order, d := range a.permissionDefs() {
		seen[d.key] = true
		if len([]rune(d.name)) > 50 || len(d.slug) > 50 {
			res.Skipped = append(res.Skipped, d.key+": name or slug longer than 50 characters")
			continue
		}
		parentID := uint(0)
		if d.parent != "" {
			id, ok := ids[d.parent]
			if !ok {
				res.Skipped = append(res.Skipped, d.key+": its parent was not written")
				continue
			}
			parentID = id
		}

		if row, ok := byKey[d.key]; ok {
			ids[d.key] = row.ID
			if row.Overridden {
				continue
			}
			want := map[string]any{
				"name": d.name, "slug": d.slug, "http_method": d.method,
				"http_path": d.path, "parent_id": parentID, "order": (order + 1) * 10,
			}
			if row.Name == d.name && row.Slug == d.slug && row.HTTPMethod == d.method &&
				row.HTTPPath == d.path && row.ParentID == parentID && row.Order == (order+1)*10 {
				continue
			}
			if other, taken := bySlug[d.slug]; taken && other.ID != row.ID {
				res.Skipped = append(res.Skipped, d.key+": slug "+d.slug+" is held by another permission")
				continue
			}
			if err := db.Model(row).Updates(want).Error; err != nil {
				return res, err
			}
			res.Updated++
			continue
		}

		if _, taken := bySlug[d.slug]; taken {
			res.Skipped = append(res.Skipped, d.key+": slug "+d.slug+" is held by another permission")
			continue
		}
		key := d.key
		row := Permission{
			Name: d.name, Slug: d.slug, HTTPMethod: d.method, HTTPPath: d.path,
			ParentID: parentID, Order: (order + 1) * 10,
			Source: MenuSourceCode, CodeKey: &key,
		}
		if err := db.Create(&row).Error; err != nil {
			return res, err
		}
		ids[d.key] = row.ID
		bySlug[row.Slug] = &row
		res.Created++
	}

	for key, row := range byKey {
		if !seen[key] && row.Source == MenuSourceCode {
			res.Orphaned = append(res.Orphaned, key)
		}
	}
	for _, s := range res.Skipped {
		a.log.Warn("steward: permission not synced", "reason", s)
	}
	if len(res.Orphaned) > 0 {
		a.log.Warn("steward: synced permissions have no matching resource", "code_keys", res.Orphaned)
	}
	return res, nil
}

// errPermissionSyncNeedsDB is returned when the sync runs before the
// permission table exists.
var errPermissionSyncNeedsDB = errors.New("steward: the permission table is missing; run migrations first")

// checkPermissionTable reports a missing table before the sync's first query
// turns it into a less helpful driver error.
func checkPermissionTable(db *gorm.DB) error {
	if !db.Migrator().HasTable(&Permission{}) {
		return errPermissionSyncNeedsDB
	}
	if !db.Migrator().HasColumn(&Permission{}, "CodeKey") {
		return fmt.Errorf("%w (0009_add_permission_sync_columns)", errPermissionSyncNeedsDB)
	}
	return nil
}
