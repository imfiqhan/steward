package steward

import (
	"context"
	"errors"
	"html/template"
	"slices"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// platformOnly lets platform administrators do everything and everyone else
// nothing. It guards the built-in pages that configure the whole panel
// (permissions, menu, settings) once tenancy is on.
type platformOnly[T any] struct{}

func (platformOnly[T]) ViewAny(c *Context) bool    { return c.IsPlatformAdmin() }
func (platformOnly[T]) View(c *Context, _ *T) bool { return c.IsPlatformAdmin() }
func (platformOnly[T]) Create(c *Context) bool     { return c.IsPlatformAdmin() }
func (platformOnly[T]) Update(c *Context, _ *T) bool {
	return c.IsPlatformAdmin()
}
func (platformOnly[T]) Delete(c *Context, _ *T) bool { return c.IsPlatformAdmin() }

// platformWrites lets anyone read and only platform administrators write.
// Tenant administrators need to see the roles they assign.
type platformWrites[T any] struct{ AllowAll[T] }

func (platformWrites[T]) Create(c *Context) bool       { return c.IsPlatformAdmin() }
func (platformWrites[T]) Update(c *Context, _ *T) bool { return c.IsPlatformAdmin() }
func (platformWrites[T]) Delete(c *Context, _ *T) bool { return c.IsPlatformAdmin() }

// tenantUsersPolicy stops a tenant administrator from changing an account
// that exists outside their tenant: its password and username are shared by
// every tenant it belongs to.
type tenantUsersPolicy struct{ AllowAll[User] }

func (tenantUsersPolicy) Update(c *Context, u *User) bool {
	if c.IsPlatformAdmin() || c.Tenant() == nil {
		return true
	}
	if c.Panel.owns(c.Tenant(), u.ID) && u.ID != c.User.ID {
		return false
	}
	return c.Panel.ownedByTenant(c.Ctx(), c.TenantID(), u.ID)
}

func (tenantUsersPolicy) Delete(c *Context, u *User) bool {
	if c.IsPlatformAdmin() || c.Tenant() == nil {
		return true
	}
	return !c.Panel.isPlatformAdmin(c.Ctx(), u.ID)
}

// ownedByTenant reports whether the account belongs to this tenant only and
// holds no platform-wide administrator role.
func (a *Panel) ownedByTenant(ctx context.Context, tenantID, userID uint) bool {
	db := a.db.WithContext(WithoutTenant(ctx))
	var other int64
	db.Model(&TenantUser{}).Where("user_id = ? AND tenant_id <> ?", userID, tenantID).Count(&other)
	return other == 0 && !a.isPlatformAdmin(ctx, userID)
}

func (a *Panel) isPlatformAdmin(ctx context.Context, userID uint) bool {
	var u User
	if err := a.db.WithContext(WithoutTenant(ctx)).Preload("Roles").First(&u, userID).Error; err != nil {
		return false
	}
	return u.IsAdministrator()
}

// tenantUserRepo is the Users page repository under tenancy. Inside a tenant
// it lists only members, shows and edits the roles held in that tenant, adds
// new accounts as members, and deletes a membership rather than the account.
type tenantUserRepo struct {
	*GormRepository[User]
	panel *Panel
}

func newTenantUserRepo(a *Panel, base *GormRepository[User]) tenantUserRepo {
	base.Query(func(db *gorm.DB) *gorm.DB {
		id, ok := TenantFromContext(db.Statement.Context)
		if !ok {
			return db
		}
		return db.Where("id IN (?)", db.Session(&gorm.Session{NewDB: true}).
			Model(&TenantUser{}).Select("user_id").Where("tenant_id = ?", id))
	})
	return tenantUserRepo{GormRepository: base, panel: a}
}

func (r tenantUserRepo) tenantRolesInto(ctx context.Context, users []User) error {
	id, ok := TenantFromContext(ctx)
	if !ok || len(users) == 0 {
		return nil
	}
	db := r.panel.db.WithContext(WithoutTenant(ctx))
	for i := range users {
		roles, err := r.panel.tenantRoles(db, id, users[i].ID)
		if err != nil {
			return err
		}
		users[i].Roles = roles
	}
	return nil
}

func (r tenantUserRepo) List(ctx context.Context, q *ListQuery) ([]User, int64, error) {
	rows, n, err := r.GormRepository.List(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	return rows, n, r.tenantRolesInto(ctx, rows)
}

func (r tenantUserRepo) Find(ctx context.Context, id string) (*User, error) {
	u, err := r.GormRepository.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	one := []User{*u}
	if err := r.tenantRolesInto(ctx, one); err != nil {
		return nil, err
	}
	return &one[0], nil
}

func (r tenantUserRepo) Create(ctx context.Context, u *User) error {
	return r.panel.db.WithContext(WithoutTenant(ctx)).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(u).Error; err != nil {
			return err
		}
		if id, ok := TenantFromContext(ctx); ok {
			return tx.Create(&TenantUser{TenantID: id, UserID: u.ID}).Error
		}
		return nil
	})
}

func (r tenantUserRepo) Delete(ctx context.Context, ids []string) error {
	id, ok := TenantFromContext(ctx)
	if !ok {
		return r.GormRepository.Delete(ctx, ids)
	}
	return r.panel.db.WithContext(WithoutTenant(ctx)).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ? AND user_id IN ?", id, ids).Delete(&TenantUserRole{}).Error; err != nil {
			return err
		}
		return tx.Where("tenant_id = ? AND user_id IN ?", id, ids).Delete(&TenantUser{}).Error
	})
}

// syncTenantRoles replaces the user's roles inside one tenant.
func (a *Panel) syncTenantRoles(ctx context.Context, tenantID, userID uint, ids []uint) error {
	return a.db.WithContext(WithoutTenant(ctx)).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ? AND user_id = ?", tenantID, userID).
			Delete(&TenantUserRole{}).Error; err != nil {
			return err
		}
		now := time.Now()
		for _, roleID := range ids {
			row := TenantUserRole{TenantID: tenantID, UserID: userID, RoleID: roleID, CreatedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// registerTenantsResource is the platform administrator's list of tenants and
// their members.
func (a *Panel) registerTenantsResource() {
	res := Register[Tenant](a).Slug("auth/tenants").Title("Tenants").
		Icon("building-2").Group("Admin").Policy(platformOnly[Tenant]{})

	res.Grid(func(g *Grid[Tenant]) {
		g.Column("ID").Sortable().Width(60)
		g.Column("Name").Sortable()
		g.Column("Slug")
		g.Column("Active").Bool()
		g.Column("CreatedAt", "Created").Sortable()
		g.QuickSearch("Name", "Slug")
	})

	res.Detail(func(d *Detail[Tenant]) {
		d.Field("ID")
		d.Field("Name")
		d.Field("Slug")
		d.Field("Active")
		if a.ownershipEnabled() {
			d.FieldFunc("Owner", "Owner", func(t *Tenant) template.HTML {
				if t.OwnerUserID == nil {
					return template.HTML(`<span class="text-muted-foreground">None</span>`)
				}
				var u User
				if a.db.WithContext(WithoutTenant(context.Background())).First(&u, *t.OwnerUserID).Error != nil {
					return ""
				}
				return template.HTML(template.HTMLEscapeString(displayName(&u) + " (" + u.Username + ")"))
			})
		}
		d.Field("CreatedAt", "Created")
	})

	userOptions := func(c *Context) Options {
		var users []User
		if err := c.Panel.db.WithContext(WithoutTenant(c.Ctx())).Order("username").Find(&users).Error; err != nil {
			return Options{}
		}
		o := Options{}
		for _, u := range users {
			o[strconv.FormatUint(uint64(u.ID), 10)] = displayName(&u) + " (" + u.Username + ")"
		}
		return o
	}

	res.Form(func(f *Form[Tenant]) {
		f.Text("Name").Rules("required|max:255")
		f.Text("Slug").Rules("required|alpha_dash|max:64|unique:" + prefixed("tenants") + ",slug,{id}")
		f.Switch("Active").Default(true)
		f.MultiSelect("Members").OptionsFunc(userOptions).
			ValuesFunc(func(c *Context, m any) []string {
				t, ok := m.(*Tenant)
				if !ok || t.ID == 0 {
					return nil
				}
				var ids []uint
				c.Panel.db.WithContext(WithoutTenant(c.Ctx())).Model(&TenantUser{}).
					Where("tenant_id = ?", t.ID).Pluck("user_id", &ids)
				out := make([]string, 0, len(ids))
				for _, id := range ids {
					out = append(out, strconv.FormatUint(uint64(id), 10))
				}
				return out
			})
		if a.ownershipEnabled() {
			f.Select("OwnerUserID", "Owner").OptionsFunc(userOptions).
				Help("The owner is always an administrator and cannot be removed.").
				SavingValue(func(_ *Context, raw string) (any, error) {
					if strings.TrimSpace(raw) == "" {
						return nil, nil
					}
					id, err := strconv.ParseUint(raw, 10, 64)
					if err != nil {
						return nil, errors.New("choose an owner from the list")
					}
					return uint(id), nil
				})
			f.Saving(func(c *Context, t *Tenant) error {
				if t.OwnerUserID != nil && !slices.Contains(c.R.Form["Members"], strconv.FormatUint(uint64(*t.OwnerUserID), 10)) {
					return errors.New("the owner must be one of the members")
				}
				return nil
			})
		}
		f.Saved(func(c *Context, t *Tenant, _ bool) error {
			if err := a.syncMembers(c.Ctx(), t.ID, c.R.Form["Members"]); err != nil {
				return err
			}
			if !a.ownershipEnabled() || t.OwnerUserID == nil {
				return nil
			}
			return a.db.WithContext(WithoutTenant(c.Ctx())).Clauses(clause.OnConflict{DoNothing: true}).
				Create(&TenantUserRole{TenantID: t.ID, UserID: *t.OwnerUserID,
					RoleID: a.administratorRoleID(c.Ctx()), CreatedAt: time.Now()}).Error
		})
		f.Deleting(func(c *Context, ids []string) error {
			db := c.Panel.db.WithContext(WithoutTenant(c.Ctx()))
			if err := db.Where("tenant_id IN ?", ids).Delete(&TenantUserRole{}).Error; err != nil {
				return err
			}
			return db.Where("tenant_id IN ?", ids).Delete(&TenantUser{}).Error
		})
	})
}

// syncMembers replaces a tenant's members, dropping the tenant roles of
// anyone removed.
func (a *Panel) syncMembers(ctx context.Context, tenantID uint, raw []string) error {
	want := map[uint]bool{}
	for _, s := range raw {
		if id, err := strconv.ParseUint(s, 10, 64); err == nil {
			want[uint(id)] = true
		}
	}
	return a.db.WithContext(WithoutTenant(ctx)).Transaction(func(tx *gorm.DB) error {
		var have []uint
		if err := tx.Model(&TenantUser{}).Where("tenant_id = ?", tenantID).Pluck("user_id", &have).Error; err != nil {
			return err
		}
		var drop []uint
		for _, id := range have {
			if !want[id] {
				drop = append(drop, id)
			}
			delete(want, id)
		}
		if len(drop) > 0 {
			if err := tx.Where("tenant_id = ? AND user_id IN ?", tenantID, drop).Delete(&TenantUserRole{}).Error; err != nil {
				return err
			}
			if err := tx.Where("tenant_id = ? AND user_id IN ?", tenantID, drop).Delete(&TenantUser{}).Error; err != nil {
				return err
			}
		}
		for id := range want {
			if err := tx.Create(&TenantUser{TenantID: tenantID, UserID: id}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
