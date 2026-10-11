package steward

import (
	"net/http"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

type workspaceAdminVM struct {
	ID    uint
	Label string
}

// canManageWorkspace reports whether the user may rename the current tenant
// and hand it over: its owner, or a platform administrator.
func (c *Context) canManageWorkspace() bool {
	return c.Panel.ownershipEnabled() && c.Tenant() != nil && (c.IsTenantOwner() || c.IsPlatformAdmin())
}

// workspacePage handles GET {Prefix}/auth/workspace.
func (a *Panel) workspacePage(c *Context) error {
	if !c.canManageWorkspace() {
		a.renderError(c, http.StatusForbidden, "Only the workspace owner can change these settings", nil)
		return nil
	}
	return a.renderWorkspace(c, "")
}

func (a *Panel) renderWorkspace(c *Context, errMsg string) error {
	t := c.Tenant()
	db := a.db.WithContext(WithoutTenant(c.Ctx()))
	var admins []User
	adminID := a.administratorRoleID(c.Ctx())
	db.Where("id IN (?)", db.Model(&TenantUserRole{}).Select("user_id").
		Where("tenant_id = ? AND role_id = ?", t.ID, adminID)).Order("name").Find(&admins)
	candidates := make([]workspaceAdminVM, 0, len(admins))
	owner := ""
	for i := range admins {
		u := &admins[i]
		label := displayName(u) + " (" + u.Username + ")"
		if t.OwnedBy(u.ID) {
			owner = label
			continue
		}
		candidates = append(candidates, workspaceAdminVM{ID: u.ID, Label: label})
	}
	if owner == "" && t.OwnerUserID != nil {
		var u User
		if db.First(&u, *t.OwnerUserID).Error == nil {
			owner = displayName(&u) + " (" + u.Username + ")"
		}
	}
	return a.render(c, "auth/workspace.html", "Workspace settings", map[string]any{
		"Workspace":  t,
		"Owner":      owner,
		"Candidates": candidates,
		"Error":      errMsg,
	})
}

// workspaceSubmit handles POST {Prefix}/auth/workspace: action "rename" or
// "transfer".
func (a *Panel) workspaceSubmit(c *Context) error {
	if !c.canManageWorkspace() {
		a.renderError(c, http.StatusForbidden, "Only the workspace owner can change these settings", nil)
		return nil
	}
	t := c.Tenant()
	db := a.db.WithContext(WithoutTenant(c.Ctx()))
	switch c.R.PostFormValue("action") {
	case "rename":
		name := strings.TrimSpace(c.R.PostFormValue("name"))
		if name == "" || len(name) > 100 {
			c.W.WriteHeader(http.StatusUnprocessableEntity)
			return a.renderWorkspace(c, "Give the workspace a name of up to 100 characters.")
		}
		if err := db.Model(&Tenant{}).Where("id = ?", t.ID).Update("name", name).Error; err != nil {
			return err
		}
		c.Flash("success", "The workspace is now called "+name+".")
	case "transfer":
		to, err := strconv.ParseUint(c.R.PostFormValue("owner"), 10, 64)
		if err != nil || !a.holdsTenantRole(db, t.ID, uint(to), a.administratorRoleID(c.Ctx())) {
			c.W.WriteHeader(http.StatusUnprocessableEntity)
			return a.renderWorkspace(c, "Ownership can only go to another administrator of this workspace.")
		}
		if err := db.Model(&Tenant{}).Where("id = ?", t.ID).Update("owner_user_id", uint(to)).Error; err != nil {
			return err
		}
		c.Flash("success", "Ownership was transferred.")
	default:
		return c.Redirect(a.url("auth/workspace"))
	}
	return c.Redirect(a.url("auth/workspace"))
}

func (a *Panel) holdsTenantRole(db *gorm.DB, tenantID, userID, roleID uint) bool {
	var n int64
	db.Model(&TenantUserRole{}).Where("tenant_id = ? AND user_id = ? AND role_id = ?", tenantID, userID, roleID).Count(&n)
	return n > 0
}
