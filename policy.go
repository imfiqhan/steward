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

// permissionExcept lists prefix-relative paths that skip the permission
// middleware (auth itself, the dashboard, assets).
func (a *Admin) permissionSkip(rel string) bool {
	switch rel {
	case "/", "/auth/login", "/auth/logout", "/auth/profile":
		return true
	}
	return strings.HasPrefix(rel, "/_assets/") || strings.HasPrefix(rel, "/_uploads/")
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
