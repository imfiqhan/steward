package steward

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// Tenant is one isolated workspace. Rows of tenant-scoped tables belong to
// exactly one tenant; accounts belong to any number through TenantUser.
type Tenant struct {
	ID     uint   `gorm:"primaryKey"`
	Slug   string `gorm:"size:64;uniqueIndex"`
	Name   string `gorm:"size:255"`
	Active bool   `gorm:"default:true"`
	// OwnerUserID is the one member who cannot be removed or demoted and who
	// alone may rename the workspace or hand it over. NULL means no owner.
	OwnerUserID *uint `gorm:"index"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// OwnedBy reports whether userID owns the tenant.
func (t *Tenant) OwnedBy(userID uint) bool {
	return t != nil && t.OwnerUserID != nil && *t.OwnerUserID == userID
}

func (Tenant) TableName() string { return prefixed("tenants") }

// TenantUser records that an account is a member of a tenant.
type TenantUser struct {
	TenantID  uint `gorm:"primaryKey"`
	UserID    uint `gorm:"primaryKey;index"`
	CreatedAt time.Time
}

func (TenantUser) TableName() string { return prefixed("tenant_users") }

// TenantUserRole grants a role to an account inside one tenant only. Roles in
// role_users stay platform-wide; the two sets are merged per request.
type TenantUserRole struct {
	TenantID  uint `gorm:"primaryKey"`
	UserID    uint `gorm:"primaryKey"`
	RoleID    uint `gorm:"primaryKey"`
	CreatedAt time.Time
}

func (TenantUserRole) TableName() string { return prefixed("tenant_user_roles") }

// TenantScoped marks a model as owned by a tenant. Embed it in an application
// model and, with Config.Tenancy set, every query through a context carrying
// a tenant is filtered to that tenant and every insert is stamped with it.
//
//	type Survey struct {
//	    ID uint
//	    steward.TenantScoped
//	    Title string
//	}
type TenantScoped struct {
	TenantID uint `gorm:"index;not null"`
}

// IsTenantScoped implements TenantScopedModel.
func (TenantScoped) IsTenantScoped() {}

// TenantScopedModel is what the tenant plugin looks for. Embedding
// TenantScoped provides it. A model that needs tenant_id inside a composite
// index declares its own `TenantID uint` field with those tags and
// implements this method instead:
//
//	type Survey struct {
//	    ID       uint
//	    TenantID uint   `gorm:"not null;uniqueIndex:idx_survey_code,priority:1"`
//	    Code     string `gorm:"uniqueIndex:idx_survey_code,priority:2"`
//	}
//
//	func (Survey) IsTenantScoped() {}
type TenantScopedModel interface{ IsTenantScoped() }

var tenantScopedType = reflect.TypeOf((*TenantScopedModel)(nil)).Elem()

// TenancyConfig turns on multi-tenancy. A nil Config.Tenancy leaves the panel
// single-tenant and registers nothing.
type TenancyConfig struct {
	// Resolve picks the tenant for an authenticated request, e.g. from a
	// subdomain. Returning 0 falls back to the tenant selected in the
	// session. The result is still checked against the user's memberships.
	Resolve func(r *http.Request, u *User) (uint, error)

	// Tables names tenant-scoped tables that have no model embedding
	// TenantScoped, so queries built with db.Table(name) are scoped too.
	Tables []string

	// Roles lists the role slugs a workspace may assign to its members, on
	// the Members page and in invitations. Nil offers every role. Platform
	// mode is not restricted.
	Roles []string

	// Signup lets a person whose SSO identity matches no account create one,
	// together with a new workspace they own. Nil keeps sign-in invite-only.
	Signup *SignupConfig

	// CanAddMember is asked before an account joins a tenant: when an
	// invitation is created and when an account is created on the Members
	// page. A non-nil error refuses, and its message is shown.
	CanAddMember func(ctx context.Context, tenantID uint) error

	// DisableInvitations removes the Invitations page and the invitation
	// links: members are then added on the Members page only.
	DisableInvitations bool

	// DisableOwnership ignores tenant owners: no owner field, no protection
	// of the owner, no Workspace settings page, and signup does not record
	// one. The last-administrator protection still applies.
	DisableOwnership bool
}

// SignupConfig turns on self-service signup through an AuthProvider.
type SignupConfig struct {
	// Created runs inside the signup transaction, after the account, the
	// tenant and the owner's membership exist. An error rolls all of it back.
	Created func(ctx context.Context, tx *gorm.DB, u *User, t *Tenant) error
}

// ErrNoTenant is returned for a query on a tenant-scoped table through a
// context that carries neither a tenant nor WithoutTenant.
var ErrNoTenant = errors.New("steward: tenant-scoped query without a tenant in the context")

type tenantCtxKey struct{}
type noTenantCtxKey struct{}

// WithTenant returns a context whose queries are scoped to tenant id.
func WithTenant(ctx context.Context, id uint) context.Context {
	return context.WithValue(ctx, tenantCtxKey{}, id)
}

// WithoutTenant returns a context whose queries see every tenant. Use it for
// platform administration, migrations and background work that spans
// tenants.
func WithoutTenant(ctx context.Context) context.Context {
	return context.WithValue(ctx, noTenantCtxKey{}, true)
}

// TenantFromContext returns the tenant a context is scoped to.
func TenantFromContext(ctx context.Context) (uint, bool) {
	if ctx == nil {
		return 0, false
	}
	id, ok := ctx.Value(tenantCtxKey{}).(uint)
	return id, ok && id != 0
}

func tenantBypassed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	b, _ := ctx.Value(noTenantCtxKey{}).(bool)
	return b
}

// TenantPlugin returns the GORM plugin that enforces tenant scoping. New
// registers it when Config.Tenancy is set; a process that shares the
// database without a Panel registers it itself:
//
//	db.Use(steward.TenantPlugin("legacy_table"))
//
// Scoping covers models embedding TenantScoped and the named tables. It
// applies to Find, First, Count, Pluck, Row, Update, Delete and Create; Raw
// and Exec are not parsed and are never scoped.
func TenantPlugin(tables ...string) gorm.Plugin {
	p := &tenantPlugin{}
	for _, t := range tables {
		p.tables.Store(t, true)
	}
	return p
}

type tenantPlugin struct {
	tables sync.Map // table name → true
}

const tenantPluginName = "steward:tenant"

func (p *tenantPlugin) Name() string { return tenantPluginName }

func (p *tenantPlugin) Initialize(db *gorm.DB) error {
	cb := db.Callback()
	if err := cb.Query().Before("gorm:query").Register(tenantPluginName+":query", p.scope); err != nil {
		return err
	}
	if err := cb.Row().Before("gorm:row").Register(tenantPluginName+":row", p.scope); err != nil {
		return err
	}
	if err := cb.Update().Before("gorm:update").Register(tenantPluginName+":update", p.update); err != nil {
		return err
	}
	if err := cb.Delete().Before("gorm:delete").Register(tenantPluginName+":delete", p.scope); err != nil {
		return err
	}
	return cb.Create().Before("gorm:create").Register(tenantPluginName+":create", p.create)
}

// useTenantPlugin registers the plugin on db, or adds tables to the one
// already registered.
func useTenantPlugin(db *gorm.DB, tables []string) error {
	if existing, ok := db.Plugins[tenantPluginName].(*tenantPlugin); ok {
		existing.addTables(tables...)
		return nil
	}
	return db.Use(TenantPlugin(tables...))
}

func (t *typedResource[T]) tenantScoped() bool {
	return reflect.PointerTo(t.ft.model.ModelType).Implements(tenantScopedType)
}

// noteTenantSchema registers a tenant-scoped model's table with the plugin
// up front, so db.Table(name) queries against it are scoped before any
// model query has named it.
func (a *Panel) noteTenantSchema(s *schema.Schema) {
	if s == nil || a.cfg.Tenancy == nil || !reflect.PointerTo(s.ModelType).Implements(tenantScopedType) {
		return
	}
	if p, ok := a.db.Plugins[tenantPluginName].(*tenantPlugin); ok {
		p.addTables(s.Table)
	}
}

func (p *tenantPlugin) addTables(tables ...string) {
	for _, t := range tables {
		p.tables.Store(t, true)
	}
}

// scoped reports whether the statement targets a tenant-scoped table, and
// remembers the table name so later db.Table(name) queries are caught too.
func (p *tenantPlugin) scoped(stmt *gorm.Statement) bool {
	if stmt.Schema != nil && reflect.PointerTo(stmt.Schema.ModelType).Implements(tenantScopedType) {
		p.tables.Store(stmt.Schema.Table, true)
		return true
	}
	if stmt.Table == "" {
		return false
	}
	_, ok := p.tables.Load(stmt.Table)
	return ok
}

// tenant returns the statement's tenant, whether scoping is skipped, and
// records ErrNoTenant when neither applies.
func (p *tenantPlugin) tenant(db *gorm.DB) (id uint, skip bool) {
	ctx := db.Statement.Context
	if tenantBypassed(ctx) {
		return 0, true
	}
	id, ok := TenantFromContext(ctx)
	if !ok {
		_ = db.AddError(fmt.Errorf("%w (table %s)", ErrNoTenant, db.Statement.Table))
		return 0, true
	}
	return id, false
}

func (p *tenantPlugin) where(db *gorm.DB, id uint) {
	db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{
		clause.Eq{Column: clause.Column{Table: clause.CurrentTable, Name: "tenant_id"}, Value: id},
	}})
}

func (p *tenantPlugin) scope(db *gorm.DB) {
	if db.Error != nil || !p.scoped(db.Statement) {
		return
	}
	if id, skip := p.tenant(db); !skip {
		p.where(db, id)
	}
}

// update filters the target rows to the tenant and refuses to move a row to
// another tenant.
func (p *tenantPlugin) update(db *gorm.DB) {
	if db.Error != nil || !p.scoped(db.Statement) {
		return
	}
	id, skip := p.tenant(db)
	if skip {
		return
	}
	p.where(db, id)
	if m, ok := db.Statement.Dest.(map[string]any); ok {
		for _, k := range []string{"tenant_id", "TenantID"} {
			if v, present := m[k]; present && fmt.Sprint(v) != fmt.Sprint(id) {
				_ = db.AddError(errors.New("steward: an update cannot move a row to another tenant"))
				return
			}
		}
	}
	if db.Statement.Schema == nil {
		return
	}
	if f := db.Statement.Schema.LookUpField("TenantID"); f != nil {
		rv := reflect.Indirect(reflect.ValueOf(db.Statement.Dest))
		if rv.Kind() == reflect.Struct && rv.Type() == db.Statement.Schema.ModelType {
			if v, zero := f.ValueOf(db.Statement.Context, rv); !zero && v != any(id) {
				_ = db.AddError(errors.New("steward: an update cannot move a row to another tenant"))
			}
		}
	}
}

// create stamps new rows with the tenant. A row that already names a
// different tenant is refused.
func (p *tenantPlugin) create(db *gorm.DB) {
	if db.Error != nil || !p.scoped(db.Statement) {
		return
	}
	ctx := db.Statement.Context
	id, ok := TenantFromContext(ctx)
	if !ok && !tenantBypassed(ctx) {
		_ = db.AddError(fmt.Errorf("%w (table %s)", ErrNoTenant, db.Statement.Table))
		return
	}

	switch dest := db.Statement.Dest.(type) {
	case map[string]any:
		p.stampMap(db, dest, id)
		return
	case []map[string]any:
		for _, m := range dest {
			p.stampMap(db, m, id)
		}
		return
	case *[]map[string]any:
		for _, m := range *dest {
			p.stampMap(db, m, id)
		}
		return
	}

	if db.Statement.Schema == nil {
		return
	}
	f := db.Statement.Schema.LookUpField("TenantID")
	if f == nil {
		return
	}
	stamp := func(rv reflect.Value) {
		v, zero := f.ValueOf(ctx, rv)
		switch {
		case zero && id == 0:
			_ = db.AddError(fmt.Errorf("%w (table %s)", ErrNoTenant, db.Statement.Table))
		case zero:
			_ = f.Set(ctx, rv, id)
		case id != 0 && v != any(id):
			_ = db.AddError(errors.New("steward: cannot create a row for another tenant"))
		}
	}
	rv := db.Statement.ReflectValue
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			stamp(reflect.Indirect(rv.Index(i)))
		}
	case reflect.Struct:
		stamp(rv)
	}
}

func (p *tenantPlugin) stampMap(db *gorm.DB, m map[string]any, id uint) {
	if v, ok := m["tenant_id"]; ok && v != nil && fmt.Sprint(v) != "0" {
		if id != 0 && fmt.Sprint(v) != fmt.Sprint(id) {
			_ = db.AddError(errors.New("steward: cannot create a row for another tenant"))
		}
		return
	}
	if id == 0 {
		_ = db.AddError(fmt.Errorf("%w (table %s)", ErrNoTenant, db.Statement.Table))
		return
	}
	m["tenant_id"] = id
}

// tenantColumnModels are the framework tables that record a tenant without
// being scoped by it.
func tenantColumnModels() []any { return []any{&Token{}, &ExportJob{}, &OperationLog{}} }

func addTenantColumns(tx *gorm.DB) error {
	mig := tx.Migrator()
	for _, m := range tenantColumnModels() {
		if mig.HasColumn(m, "TenantID") {
			continue
		}
		if err := mig.AddColumn(m, "TenantID"); err != nil {
			return err
		}
		if err := mig.CreateIndex(m, "TenantID"); err != nil {
			return err
		}
	}
	return nil
}

func dropTenantColumns(tx *gorm.DB) error {
	mig := tx.Migrator()
	for _, m := range tenantColumnModels() {
		if !mig.HasColumn(m, "TenantID") {
			continue
		}
		if mig.HasIndex(m, "TenantID") {
			if err := mig.DropIndex(m, "TenantID"); err != nil {
				return err
			}
		}
		if err := mig.DropColumn(m, "TenantID"); err != nil {
			return err
		}
	}
	return nil
}

// ---- request resolution ----------------------------------------------------

type ctxTenantState struct {
	tenant   *Tenant
	platform bool // holds a platform-wide administrator role
}

type tenantStateKey struct{}

func tenantStateOf(r *http.Request) *ctxTenantState {
	s, _ := r.Context().Value(tenantStateKey{}).(*ctxTenantState)
	return s
}

// TenancyEnabled reports whether the panel runs with Config.Tenancy.
func (a *Panel) TenancyEnabled() bool { return a.cfg.Tenancy != nil }

// withTenant resolves the request's tenant after authentication. It scopes
// the request context, merges the user's roles in that tenant into
// User.Roles, and keeps the selection in the session.
func (a *Panel) withTenant(next http.Handler) http.Handler {
	if a.cfg.Tenancy == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := userOf(r)
		if user == nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		db := a.db.WithContext(WithoutTenant(ctx))
		state := &ctxTenantState{platform: user.IsAdministrator()}
		if tokenOf(r) == nil && a.invitationsEnabled() {
			a.acceptPendingInvite(w, r, user)
		}

		id, err := a.requestedTenant(r, user)
		if err != nil {
			a.deny(w, r, http.StatusBadRequest, err.Error())
			return
		}
		if id != 0 && !state.platform && !a.isMember(db, id, user.ID) {
			id = 0
		}
		if id == 0 && !state.platform {
			id = a.firstMembership(db, user.ID)
		}

		if id != 0 {
			var t Tenant
			if err := db.First(&t, id).Error; err != nil || (!t.Active && !state.platform) {
				id = 0
			} else {
				state.tenant = &t
			}
		}

		switch {
		case state.tenant != nil:
			roles, err := a.tenantRoles(db, state.tenant.ID, user.ID)
			if err != nil {
				a.deny(w, r, http.StatusInternalServerError, "loading workspace roles failed")
				return
			}
			user.Roles = mergeRoles(user.Roles, roles)
			ctx = WithTenant(ctx, state.tenant.ID)
		case state.platform:
			ctx = WithoutTenant(ctx)
		default:
			if !a.tenantlessPath(r.URL.Path) {
				if wantsJSONLike(r) || r.Header.Get("HX-Request") == "true" || tokenOf(r) != nil {
					a.deny(w, r, http.StatusForbidden, "your account is not a member of any active workspace")
					return
				}
				a.renderNoWorkspace(w, r, user)
				return
			}
		}

		if sess := sessionOf(r); sess != nil && tokenOf(r) == nil && sess.TenantID != id {
			sess.TenantID = id
			a.saveSessionW(w, r, sess)
		}
		ctx = context.WithValue(ctx, tenantStateKey{}, state)
		ctx = context.WithValue(ctx, requestBaseKey{}, a.publicBase(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestedTenant returns the tenant the request asks for: the bearer token's,
// the resolver's, or the session's selection, in that order.
func (a *Panel) requestedTenant(r *http.Request, u *User) (uint, error) {
	if tok := tokenOf(r); tok != nil && tok.TenantID != 0 {
		return tok.TenantID, nil
	}
	if a.cfg.Tenancy.Resolve != nil {
		id, err := a.cfg.Tenancy.Resolve(r, u)
		if err != nil || id != 0 {
			return id, err
		}
	}
	if sess := sessionOf(r); sess != nil {
		return sess.TenantID, nil
	}
	return 0, nil
}

// tenantlessPath lists what a user without a workspace may still reach.
func (a *Panel) tenantlessPath(p string) bool {
	rel := "/" + strings.TrimLeft(strings.TrimPrefix(p, a.cfg.Prefix), "/")
	return a.publicPath(p) || rel == "/auth/logout" || rel == "/auth/tenant"
}

// renderNoWorkspace answers a signed-in account that belongs to no active
// workspace: it offers to create one when signup is on, otherwise explains
// that an invitation is needed.
func (a *Panel) renderNoWorkspace(w http.ResponseWriter, r *http.Request, user *User) {
	c := &Context{W: w, R: r, Panel: a, User: user, sess: sessionOf(r)}
	w.WriteHeader(http.StatusForbidden)
	if err := a.renderStandalone(c, "auth/no_workspace.html", map[string]any{
		"Signup":        a.cfg.Tenancy.Signup != nil,
		"WorkspaceName": defaultWorkspaceName(displayName(user)),
	}); err != nil {
		a.log.Error("steward: render no-workspace page", "err", err)
	}
}

func (a *Panel) isMember(db *gorm.DB, tenantID, userID uint) bool {
	var n int64
	db.Model(&TenantUser{}).Where("tenant_id = ? AND user_id = ?", tenantID, userID).Count(&n)
	return n > 0
}

func (a *Panel) firstMembership(db *gorm.DB, userID uint) uint {
	var ids []uint
	db.Model(&TenantUser{}).
		Joins("JOIN "+prefixed("tenants")+" t ON t.id = "+prefixed("tenant_users")+".tenant_id").
		Where(prefixed("tenant_users")+".user_id = ? AND t.active = ?", userID, true).
		Order(prefixed("tenant_users")+".tenant_id").Limit(1).
		Pluck(prefixed("tenant_users")+".tenant_id", &ids)
	if len(ids) == 0 {
		return 0
	}
	return ids[0]
}

func (a *Panel) tenantRoles(db *gorm.DB, tenantID, userID uint) ([]Role, error) {
	return UserTenantRoles(db.Statement.Context, db, tenantID, userID)
}

// UserTenantRoles returns the roles an account holds inside one tenant. Add
// them to User.Roles (see MergeRoles) to authorize a request in that tenant.
// It needs no Panel, for processes that share the database.
func UserTenantRoles(ctx context.Context, db *gorm.DB, tenantID, userID uint) ([]Role, error) {
	var roles []Role
	db = db.WithContext(WithoutTenant(ctx))
	err := db.Where("id IN (?)", db.Model(&TenantUserRole{}).Select("role_id").
		Where("tenant_id = ? AND user_id = ?", tenantID, userID)).
		Order("id").Find(&roles).Error
	return roles, err
}

// exportTenantState rebuilds the tenant an export was queued in, with the
// user's roles in it, for a job that runs without a request.
func (a *Panel) exportTenantState(ctx context.Context, job *ExportJob, u *User) (*ctxTenantState, error) {
	db := a.db.WithContext(WithoutTenant(ctx))
	state := &ctxTenantState{platform: u.IsAdministrator()}
	if job.TenantID == 0 {
		if !state.platform {
			return nil, errors.New("export was queued outside any tenant")
		}
		return state, nil
	}
	if !state.platform && !a.isMember(db, job.TenantID, u.ID) {
		return nil, errors.New("the account that asked is no longer a member of the tenant")
	}
	var t Tenant
	if err := db.First(&t, job.TenantID).Error; err != nil {
		return nil, err
	}
	roles, err := a.tenantRoles(db, t.ID, u.ID)
	if err != nil {
		return nil, err
	}
	u.Roles = mergeRoles(u.Roles, roles)
	state.tenant = &t
	return state, nil
}

// tokenTenant picks the tenant a new bearer token is bound to. A non-zero
// status is a refusal to send as-is; a nil tenant with status 0 is a
// platform administrator's unbound token.
func (a *Panel) tokenTenant(ctx context.Context, u *User, slug string) (*Tenant, int, any, error) {
	db := a.db.WithContext(WithoutTenant(ctx))
	platform := u.IsAdministrator()
	if slug != "" {
		var t Tenant
		err := db.Where("slug = ? AND active = ?", slug, true).First(&t).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && !platform && !a.isMember(db, t.ID, u.ID)) {
			return nil, http.StatusForbidden, Error("You are not a member of that workspace."), nil
		}
		if err != nil {
			return nil, 0, nil, err
		}
		return &t, 0, nil, nil
	}
	tenants, err := a.Memberships(ctx, u.ID)
	if err != nil {
		return nil, 0, nil, err
	}
	switch {
	case len(tenants) == 1:
		return &tenants[0], 0, nil, nil
	case len(tenants) > 1:
		list := make([]map[string]any, 0, len(tenants))
		for _, t := range tenants {
			list = append(list, map[string]any{"slug": t.Slug, "name": t.Name})
		}
		return nil, http.StatusConflict, map[string]any{
			"message": "Choose a workspace: pass tenant with one of these slugs.",
			"tenants": list,
		}, nil
	case platform:
		return nil, 0, nil, nil
	default:
		return nil, http.StatusForbidden, Error("Your account is not a member of any active workspace."), nil
	}
}

// Memberships returns the active tenants the user belongs to, ordered by name.
func (a *Panel) Memberships(ctx context.Context, userID uint) ([]Tenant, error) {
	return UserTenants(ctx, a.db, userID)
}

// UserTenants returns the active tenants an account belongs to, ordered by
// name. It needs no Panel, for processes that share the database.
func UserTenants(ctx context.Context, db *gorm.DB, userID uint) ([]Tenant, error) {
	var out []Tenant
	db = db.WithContext(WithoutTenant(ctx))
	err := db.Where("active = ? AND id IN (?)", true,
		db.Model(&TenantUser{}).Select("tenant_id").Where("user_id = ?", userID)).
		Order("name").Find(&out).Error
	return out, err
}

// MergeRoles returns base followed by the roles of extra it lacks.
func MergeRoles(base, extra []Role) []Role { return mergeRoles(base, extra) }

func mergeRoles(base, extra []Role) []Role {
	seen := make(map[uint]bool, len(base))
	out := make([]Role, 0, len(base)+len(extra))
	for _, r := range base {
		seen[r.ID] = true
		out = append(out, r)
	}
	for _, r := range extra {
		if !seen[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// Tenant returns the tenant the request is scoped to, nil in platform mode or
// when tenancy is off.
func (c *Context) Tenant() *Tenant {
	if s := tenantStateOf(c.R); s != nil {
		return s.tenant
	}
	return nil
}

// TenantID returns the id of Tenant(), 0 when there is none.
func (c *Context) TenantID() uint {
	if t := c.Tenant(); t != nil {
		return t.ID
	}
	return 0
}

// IsTenantOwner reports whether the user owns the request's tenant. It is
// always false with TenancyConfig.DisableOwnership.
func (c *Context) IsTenantOwner() bool {
	return c.User != nil && c.Panel.owns(c.Tenant(), c.User.ID)
}

func (a *Panel) invitationsEnabled() bool {
	return a.cfg.Tenancy != nil && !a.cfg.Tenancy.DisableInvitations
}

func (a *Panel) ownershipEnabled() bool {
	return a.cfg.Tenancy != nil && !a.cfg.Tenancy.DisableOwnership
}

// owns is Tenant.OwnedBy, honouring TenancyConfig.DisableOwnership.
func (a *Panel) owns(t *Tenant, userID uint) bool {
	return a.ownershipEnabled() && t.OwnedBy(userID)
}

// IsTenantAdmin reports whether the user administers the request's tenant:
// it holds the administrator role there, or platform-wide.
func (c *Context) IsTenantAdmin() bool {
	return c.User != nil && c.Tenant() != nil && c.User.IsAdministrator()
}

// IsPlatformAdmin reports whether the user holds the administrator role
// platform-wide rather than inside a single tenant. Without tenancy it is
// the same as User.IsAdministrator.
func (c *Context) IsPlatformAdmin() bool {
	if s := tenantStateOf(c.R); s != nil {
		return s.platform
	}
	return c.User != nil && c.User.IsAdministrator()
}

// switchableTenants lists the tenants the header switcher offers: every
// tenant for a platform administrator, the user's memberships otherwise.
func (a *Panel) switchableTenants(c *Context) []Tenant {
	var out []Tenant
	if c.IsPlatformAdmin() {
		a.db.WithContext(WithoutTenant(c.Ctx())).Order("name").Find(&out)
		return out
	}
	out, _ = a.Memberships(c.Ctx(), c.User.ID)
	return out
}

// switchTenant handles POST /auth/tenant: it moves the session to another
// tenant the user belongs to. A platform administrator may pick 0 to leave
// every tenant and administer the platform.
func (a *Panel) switchTenant(c *Context) error {
	raw := c.R.PostFormValue("tenant")
	var id uint
	if _, err := fmt.Sscan(raw, &id); err != nil && raw != "" {
		return c.Redirect(a.url())
	}
	db := a.db.WithContext(WithoutTenant(c.Ctx()))
	platform := c.IsPlatformAdmin()
	if id == 0 && !platform {
		return c.Redirect(a.url())
	}
	if id != 0 && !platform && !a.isMember(db, id, c.User.ID) {
		c.Flash("error", "You are not a member of that workspace.")
		return c.Redirect(a.url())
	}
	c.sess.TenantID = id
	if err := c.login(c.User.ID); err != nil {
		return err
	}
	return c.Redirect(a.url())
}
