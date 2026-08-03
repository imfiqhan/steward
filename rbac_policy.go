package steward

import (
	"context"
	"net/http"
	"strings"

	"gorm.io/gorm"

	"github.com/imfiqhan/steward/internal/httpmatch"
)

// Policy gates one resource's actions for the current user. Menu visibility
// derives from ViewAny — one source of truth instead of dcat's separate
// role↔menu bookkeeping.
type Policy[T any] interface {
	ViewAny(c *Context) bool
	View(c *Context, m *T) bool
	Create(c *Context) bool
	Update(c *Context, m *T) bool
	Delete(c *Context, m *T) bool
}

// RowScoper optionally narrows every query to the rows the user may see
// (implement it on your Policy for row-level security).
type RowScoper interface {
	Scope(c *Context, db *gorm.DB) *gorm.DB
}

// AllowAll is an embeddable Policy allowing everything; override selectively.
type AllowAll[T any] struct{}

// ViewAny implements Policy.
func (AllowAll[T]) ViewAny(*Context) bool { return true }

// View implements Policy.
func (AllowAll[T]) View(*Context, *T) bool { return true }

// Create implements Policy.
func (AllowAll[T]) Create(*Context) bool { return true }

// Update implements Policy.
func (AllowAll[T]) Update(*Context, *T) bool { return true }

// Delete implements Policy.
func (AllowAll[T]) Delete(*Context, *T) bool { return true }

// ---- enforcement ---------------------------------------------------------
//
// A nil policy allows everything; the helpers below are the only places
// that read t.policy, so every handler asks the same questions the same way.

func (t *typedResource[T]) canViewAny(c *Context) bool {
	return t.policy == nil || t.policy.ViewAny(c)
}

func (t *typedResource[T]) canView(c *Context, m *T) bool {
	return t.policy == nil || t.policy.View(c, m)
}

func (t *typedResource[T]) canCreate(c *Context) bool {
	return t.policy == nil || t.policy.Create(c)
}

func (t *typedResource[T]) canUpdate(c *Context, m *T) bool {
	return t.policy == nil || t.policy.Update(c, m)
}

func (t *typedResource[T]) canDelete(c *Context, m *T) bool {
	return t.policy == nil || t.policy.Delete(c, m)
}

// menuVisible implements resourceEntry: sidebar entries follow ViewAny.
func (t *typedResource[T]) menuVisible(c *Context) bool { return t.canViewAny(c) }

// applyRowScope narrows a list query when the policy implements RowScoper.
// It scopes lists only — single-record loads are protected by the per-row
// View/Update/Delete checks, which see the loaded model.
func (t *typedResource[T]) applyRowScope(c *Context, q *ListQuery) {
	if t.policy == nil {
		return
	}
	if rs, ok := t.policy.(RowScoper); ok {
		q.Scopes = append(q.Scopes, func(db *gorm.DB) *gorm.DB { return rs.Scope(c, db) })
	}
}

// denyPolicy renders the standard 403 for whichever shape the client asked.
func (t *typedResource[T]) denyPolicy(c *Context) error {
	if c.WantsJSON() {
		return c.JSON(http.StatusForbidden, Error("You do not have permission to do this."))
	}
	c.Admin.renderError(c, http.StatusForbidden, "Permission denied", nil)
	return nil
}

// permissionRules returns the parsed rules of every permission the user
// holds through roles, memoized per request.
func (c *Context) permissionRules() []httpmatch.Rule {
	if c.permRules != nil {
		return c.permRules
	}
	c.permRules = c.Admin.loadPermissionRules(c.Ctx(), c.User)
	return c.permRules
}

func (a *Admin) loadPermissionRules(ctx context.Context, user *AdminUser) []httpmatch.Rule {
	rules := []httpmatch.Rule{}
	if user == nil {
		return rules
	}
	roleIDs := make([]uint, 0, len(user.Roles))
	for _, r := range user.Roles {
		roleIDs = append(roleIDs, r.ID)
	}
	if len(roleIDs) == 0 {
		return rules
	}
	var perms []Permission
	err := a.db.WithContext(ctx).
		Joins("JOIN "+prefixed("role_permissions")+" rp ON rp.permission_id = "+prefixed("permissions")+".id").
		Where("rp.role_id IN ?", roleIDs).
		Find(&perms).Error
	if err != nil {
		a.log.Error("steward: loading permissions", "err", err)
		return rules
	}
	for _, p := range perms {
		rules = append(rules, httpmatch.Parse(p.HTTPMethod, p.HTTPPath)...)
	}
	return rules
}

// permissionSkip lists prefix-relative paths that skip the permission
// middleware (auth itself, the dashboard, assets).
//
// The dashboard's lazy widget fragments are skipped alongside the dashboard
// page. Exempting the page but not the tiles it fetches would leave every
// non-administrator looking at a grid of permission errors, and the tiles show
// nothing the page itself does not — a widget's callback is the place to gate
// anything a given role should not see.
func (a *Admin) permissionSkip(rel string) bool {
	switch rel {
	case "/", "/auth/login", "/auth/logout", "/auth/profile":
		return true
	}
	return strings.HasPrefix(rel, "/_assets/") ||
		strings.HasPrefix(rel, "/_uploads/") ||
		strings.HasPrefix(rel, "/_widget/") ||
		strings.HasPrefix(rel, "/auth/profile/")
}

// withPermission enforces route-level permissions after authentication.
func (a *Admin) withPermission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := userOf(r)
		if user == nil { // public path; auth middleware already decided
			next.ServeHTTP(w, r)
			return
		}
		rel := "/" + strings.TrimLeft(strings.TrimPrefix(r.URL.Path, a.cfg.Prefix), "/")
		if a.permissionSkip(rel) || a.publicPath(r.URL.Path) || user.IsAdministrator() {
			next.ServeHTTP(w, r)
			return
		}
		rules := a.loadPermissionRules(r.Context(), user)
		if httpmatch.Matches(rules, r.Method, rel) {
			next.ServeHTTP(w, r)
			return
		}
		c := &Context{W: w, R: r, Admin: a, User: user, sess: sessionOf(r)}
		if c.WantsJSON() {
			_ = c.JSON(http.StatusForbidden, Error("You do not have permission to do this."))
			return
		}
		a.renderError(c, http.StatusForbidden, "Permission denied", nil)
	})
}
