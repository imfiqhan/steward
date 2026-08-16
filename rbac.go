package steward

import (
	"errors"
	"html/template"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm/clause"
)

// registerBuiltins dogfoods the framework's own admin pages — users, roles,
// permissions, menu, operation log, settings — through the same Register API
// applications use. Called at the start of Build.
func (a *Admin) registerBuiltins() {
	a.registerUsersResource()
	a.registerRolesResource()
	a.registerPermissionsResource()
	a.registerMenuResource()
	a.registerLogResource()
	a.registerSettingsResource()
}

// syncPivot replaces the pivot rows owned by ownerID with the given ids.
func (a *Admin) syncPivot(c *Context, table, ownerCol string, ownerID uint, relCol string, ids []string) error {
	if err := a.db.WithContext(c.Ctx()).Exec(
		"DELETE FROM "+table+" WHERE "+ownerCol+" = ?", ownerID).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	rows := make([]map[string]any, 0, len(ids))
	for _, raw := range ids {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			continue
		}
		rows = append(rows, map[string]any{
			ownerCol: ownerID, relCol: id, "created_at": now, "updated_at": now,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return a.db.WithContext(c.Ctx()).Table(table).Create(rows).Error
}

func (a *Admin) rolesOptions() func(c *Context) Options {
	return func(c *Context) Options {
		var roles []Role
		if err := c.Admin.db.WithContext(c.Ctx()).Order("id").Find(&roles).Error; err != nil {
			return Options{}
		}
		o := Options{}
		for _, r := range roles {
			o[strconv.FormatUint(uint64(r.ID), 10)] = r.Name
		}
		return o
	}
}

func (a *Admin) registerUsersResource() {
	repo, err := NewGormRepository[AdminUser](a.db)
	if err != nil {
		a.verifyErrs = append(a.verifyErrs, err)
		return
	}
	repo.With("Roles")

	res := Register[AdminUser](a).Slug("auth/users").Title("Administrators").
		Icon("users").Group("Admin").Repository(repo).
		// Reachable from the palette by whoever may already list them: the
		// search runs behind the same ViewAny gate the grid does.
		Command("Name", "Username", "Email")

	res.Grid(func(g *Grid[AdminUser]) {
		g.Column("ID").Sortable().Width(60)
		g.Column("Username").Sortable()
		g.Column("Name")
		g.ColumnFunc("Roles", "Roles", func(u *AdminUser) template.HTML {
			var b strings.Builder
			for _, r := range u.Roles {
				b.WriteString(`<span class="badge bg-blue-lt me-1">`)
				b.WriteString(template.HTMLEscapeString(r.Name))
				b.WriteString(`</span>`)
			}
			return template.HTML(b.String())
		})
		g.Column("CreatedAt", "Created").Sortable()
		g.QuickSearch("Username", "Name")
	})

	res.Form(func(f *Form[AdminUser]) {
		f.Text("Username").
			Rules("required|alpha_dash|unique:" + prefixed("users") + ",username,{id}")
		f.Text("Name").Rules("required")
		f.Email("Email").Help("Optional; enables password reset.").
			SavingValue(func(_ *Context, raw string) (any, error) {
				if strings.TrimSpace(raw) == "" {
					return nil, nil // store NULL, not "" (unique index)
				}
				return raw, nil
			})
		f.Password("Password").CreationRules("required").Rules("min:5|max:72").
			Help("Leave blank to keep the current password.").
			SavingValue(func(_ *Context, raw string) (any, error) {
				hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
				if err != nil {
					return nil, err
				}
				return string(hash), nil
			})
		f.MultiSelect("Roles").OptionsFunc(a.rolesOptions()).
			ValuesFunc(func(_ *Context, m any) []string {
				u, ok := m.(*AdminUser)
				if !ok {
					return nil
				}
				ids := make([]string, 0, len(u.Roles))
				for _, r := range u.Roles {
					ids = append(ids, strconv.FormatUint(uint64(r.ID), 10))
				}
				return ids
			})
		f.Saved(func(c *Context, u *AdminUser, _ bool) error {
			return a.syncPivot(c, prefixed("role_users"), "user_id", u.ID, "role_id", c.R.Form["Roles"])
		})
		f.Deleting(func(c *Context, ids []string) error {
			me := strconv.FormatUint(uint64(c.User.ID), 10)
			if slices.Contains(ids, me) {
				return errors.New("you cannot delete your own account")
			}
			return nil
		})
	})

	res.Detail(func(d *Detail[AdminUser]) {
		d.Field("ID")
		d.Field("Username")
		d.Field("Name")
		d.Field("Email")
		d.Field("CreatedAt", "Created")
	})
}

func (a *Admin) registerRolesResource() {
	res := Register[Role](a).Slug("auth/roles").Title("Roles").
		Icon("shield").Group("Admin")

	res.Grid(func(g *Grid[Role]) {
		g.Column("ID").Sortable().Width(60)
		g.Column("Name")
		g.Column("Slug").Badge(map[any]BadgeColor{RoleAdministrator: BadgePurple})
		g.Column("CreatedAt", "Created")
		g.QuickSearch("Name", "Slug")
	})

	permOptions := func(c *Context) Options {
		var perms []Permission
		if err := c.Admin.db.WithContext(c.Ctx()).
			Order(clause.OrderByColumn{Column: clause.Column{Name: "order"}}).
			Order("id").Find(&perms).Error; err != nil {
			return Options{}
		}
		o := Options{}
		for _, p := range perms {
			o[strconv.FormatUint(uint64(p.ID), 10)] = p.Name
		}
		return o
	}

	res.Form(func(f *Form[Role]) {
		f.Text("Name").Rules("required|max:50")
		f.Text("Slug").Rules("required|alpha_dash|max:50|unique:" + prefixed("roles") + ",slug,{id}")
		f.MultiSelect("Permissions").OptionsFunc(permOptions).
			ValuesFunc(func(c *Context, m any) []string {
				r, ok := m.(*Role)
				if !ok {
					return nil
				}
				var ids []string
				var rows []RolePermission
				if err := c.Admin.db.WithContext(c.Ctx()).Where("role_id = ?", r.ID).Find(&rows).Error; err != nil {
					return nil
				}
				for _, row := range rows {
					ids = append(ids, strconv.FormatUint(uint64(row.PermissionID), 10))
				}
				return ids
			})
		f.Saved(func(c *Context, r *Role, _ bool) error {
			return a.syncPivot(c, prefixed("role_permissions"), "role_id", r.ID, "permission_id", c.R.Form["Permissions"])
		})
		f.Deleting(func(c *Context, ids []string) error {
			for _, id := range ids {
				var role Role
				if err := c.Admin.db.WithContext(c.Ctx()).First(&role, id).Error; err == nil &&
					role.Slug == RoleAdministrator {
					return errors.New("the administrator role cannot be deleted")
				}
			}
			return nil
		})
	})
}

func (a *Admin) registerPermissionsResource() {
	res := Register[Permission](a).Slug("auth/permissions").Title("Permissions").
		Icon("key").Group("Admin")

	res.Grid(func(g *Grid[Permission]) {
		g.Column("ID").Sortable().Width(60)
		g.Column("Name")
		g.Column("Slug")
		g.Column("HTTPMethod", "Methods").Using(map[any]string{"": "ANY"})
		g.Column("HTTPPath", "Paths").Limit(60)
		g.QuickSearch("Name", "Slug", "HTTPPath")
	})

	res.Form(func(f *Form[Permission]) {
		f.Text("Name").Rules("required|max:50")
		f.Text("Slug").Rules("required|alpha_dash|max:50|unique:" + prefixed("permissions") + ",slug,{id}")
		f.Text("HTTPMethod", "HTTP methods").
			Placeholder("GET,POST — empty allows every method")
		f.Textarea("HTTPPath", "HTTP paths").Rules("required").
			Placeholder("/posts*\nGET:/authors").
			Help("One pattern per line, * wildcards; prefix a line with METHOD[,METHOD]: to restrict it. Paths are relative to the admin prefix.")
	})
}

func (a *Admin) registerLogResource() {
	res := Register[OperationLog](a).Slug("auth/logs").Title("Operation Log").
		Icon("history").Group("Admin")

	res.Grid(func(g *Grid[OperationLog]) {
		g.Column("ID").Sortable().Width(60)
		g.Column("UserID", "User")
		g.Column("Method").Badge(map[any]BadgeColor{
			"POST": BadgeGreen, "PUT": BadgeAzure, "PATCH": BadgeAzure, "DELETE": BadgeRed,
		})
		g.Column("Path").Limit(60)
		g.Column("IP")
		g.Column("CreatedAt", "When").Sortable()
		g.DefaultSort("ID", true)
		g.QuickSearch("Path", "IP")
		g.DisableCreate()
		g.DisableEdit()
	})

	res.Detail(func(d *Detail[OperationLog]) {
		d.Field("ID")
		d.Field("UserID", "User")
		d.Field("Method")
		d.Field("Path")
		d.Field("IP")
		d.Field("Input").JSON()
		d.Field("CreatedAt", "When")
	})
}

func (a *Admin) registerSettingsResource() {
	res := Register[Setting](a).Slug("auth/settings").Title("Settings").
		Icon("settings-2").Group("Admin")

	res.Grid(func(g *Grid[Setting]) {
		g.Column("Slug")
		g.Column("Value").Limit(80)
		g.Column("UpdatedAt", "Updated")
		g.QuickSearch("Slug")
	})

	res.Form(func(f *Form[Setting]) {
		f.Text("Slug").Rules("required|alpha_dash|max:100").OnlyOnCreate()
		f.Display("Slug").OnlyOnUpdate()
		f.Textarea("Value")
		f.Saved(func(c *Context, s *Setting, _ bool) error {
			return c.Admin.cfg.Cache.Delete(c.Ctx(), settingsCacheKey+s.Slug)
		})
	})
}

// registerMenuResource is defined in menu_sync.go alongside the sync logic.
