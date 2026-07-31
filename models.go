package steward

import (
	"slices"
	"sync/atomic"
	"time"
)

// tablePrefix names the framework's own tables ("admin_" by default, set from
// Config.TablePrefix at New). It is process-global because GORM resolves
// TableName statically per type; running two Admins with different prefixes
// in one process is not supported.
var tablePrefix atomic.Value // string

func init() { tablePrefix.Store("admin_") }

func prefixed(name string) string { return tablePrefix.Load().(string) + name }

// AdminUser is a panel account. Email is optional and only required for the
// password-reset flow.
type AdminUser struct {
	ID            uint    `gorm:"primaryKey"`
	Username      string  `gorm:"size:120;uniqueIndex"`
	Password      string  `gorm:"size:100"` // bcrypt hash
	Name          string  `gorm:"size:255"`
	Avatar        string  `gorm:"size:255"`
	Email         *string `gorm:"size:255;uniqueIndex"`
	RememberToken string  `gorm:"size:100"`
	CreatedAt     time.Time
	UpdatedAt     time.Time

	// Two-factor authentication (see twofactor.go). Enrolment is complete only
	// once TwoFactorConfirmedAt is set, so a scanned-but-unverified secret
	// never locks anyone out. TwoFactorLastStep records the most recently
	// accepted time step, which is what makes a code single-use.
	TwoFactorSecret      string `gorm:"size:64"`
	TwoFactorConfirmedAt *time.Time
	TwoFactorRecovery    string `gorm:"type:text"` // newline-separated SHA-256 digests
	TwoFactorLastStep    int64  `gorm:"default:0"`

	Roles []Role `gorm:"many2many:admin_role_users;joinForeignKey:user_id;joinReferences:role_id"`
}

func (AdminUser) TableName() string { return prefixed("users") }

// IsAdministrator reports whether the user holds the built-in administrator
// role (seeded with ID 1), which short-circuits every permission check.
func (u *AdminUser) IsAdministrator() bool {
	return u.HasRole(RoleAdministrator)
}

// HasRole reports whether the user holds any of the given role slugs. Roles
// must be loaded (the auth middleware preloads them), so this costs no query.
//
// It answers "who is this?", which belongs in a resource's Policy or a form
// field's Show predicate. It is not a substitute for permissions: those live in
// the database precisely so an operator can change them without a deploy.
func (u *AdminUser) HasRole(slugs ...string) bool {
	for _, r := range u.Roles {
		if slices.Contains(slugs, r.Slug) {
			return true
		}
	}
	return false
}

// RoleAdministrator is the slug of the seeded super-user role.
const RoleAdministrator = "administrator"

// Role groups permissions and users.
type Role struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:50"`
	Slug      string `gorm:"size:50;uniqueIndex"`
	CreatedAt time.Time
	UpdatedAt time.Time

	Permissions []Permission `gorm:"many2many:admin_role_permissions;joinForeignKey:role_id;joinReferences:permission_id"`
}

func (Role) TableName() string { return prefixed("roles") }

// Permission is a named grant matched against HTTP requests. HTTPMethod is a
// comma-separated method list (empty = any); HTTPPath is a newline-separated
// list of path patterns with * globs, each optionally prefixed "GET,POST:".
type Permission struct {
	ID         uint   `gorm:"primaryKey"`
	Name       string `gorm:"size:50"`
	Slug       string `gorm:"size:50;uniqueIndex"`
	HTTPMethod string `gorm:"size:255"`
	HTTPPath   string `gorm:"type:text"`
	Order      int    `gorm:"default:0"`
	ParentID   uint   `gorm:"default:0"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (Permission) TableName() string { return prefixed("permissions") }

// MenuSource records who owns a menu row: rows synced from registered
// resources ("code") reconcile on boot; hand-created rows ("db") are never
// touched by sync.
type MenuSource string

const (
	MenuSourceCode MenuSource = "code"
	MenuSourceDB   MenuSource = "db"
)

// MenuItem is one sidebar node. CodeKey ties a "code" row to its resource
// slug; Overridden marks rows edited in the UI so sync leaves them alone.
type MenuItem struct {
	ID         uint       `gorm:"primaryKey"`
	ParentID   uint       `gorm:"default:0"`
	Order      int        `gorm:"default:0"`
	Title      string     `gorm:"size:100"`
	Icon       string     `gorm:"size:100"`
	URI        string     `gorm:"size:255"`
	Show       bool       `gorm:"default:true"`
	Source     MenuSource `gorm:"size:10;default:db"`
	CodeKey    *string    `gorm:"size:100;uniqueIndex"`
	Overridden bool       `gorm:"default:false"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (MenuItem) TableName() string { return prefixed("menu") }

// Join models make the pivot tables prefix-aware; New wires them into GORM
// via SetupJoinTable so the many2many tags' static names never materialize.

// RoleUser links users to roles.
type RoleUser struct {
	RoleID    uint `gorm:"primaryKey"`
	UserID    uint `gorm:"primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (RoleUser) TableName() string { return prefixed("role_users") }

// RolePermission links roles to permissions.
type RolePermission struct {
	RoleID       uint `gorm:"primaryKey"`
	PermissionID uint `gorm:"primaryKey"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (RolePermission) TableName() string { return prefixed("role_permissions") }

// RoleMenu hides menu rows from specific roles (explicit override; visibility
// normally derives from policies).
type RoleMenu struct {
	RoleID    uint `gorm:"primaryKey"`
	MenuID    uint `gorm:"primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (RoleMenu) TableName() string { return prefixed("role_menu") }

// PermissionMenu binds menu rows to permissions.
type PermissionMenu struct {
	PermissionID uint `gorm:"primaryKey"`
	MenuID       uint `gorm:"primaryKey"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (PermissionMenu) TableName() string { return prefixed("permission_menu") }

// OperationLog records one admin request (input is masked JSON).
type OperationLog struct {
	ID        uint      `gorm:"primaryKey"`
	UserID    uint      `gorm:"index"`
	Path      string    `gorm:"size:255"`
	Method    string    `gorm:"size:10"`
	IP        string    `gorm:"size:45"`
	Input     string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"index"`
}

func (OperationLog) TableName() string { return prefixed("operation_log") }

// Setting is one row of the slug→value KV store.
type Setting struct {
	Slug      string `gorm:"primaryKey;size:100"`
	Value     string `gorm:"type:longtext"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Setting) TableName() string { return prefixed("settings") }

// AdminToken is a bearer credential for API and mobile clients, belonging to
// an AdminUser and inheriting that user's roles, permissions, and policies.
//
// Hash holds a SHA-256 of the token, not a bcrypt digest: tokens carry 256
// bits of entropy, so a fast hash is sound and — unlike bcrypt — lets lookup
// be a single indexed query instead of a scan over every row.
type AdminToken struct {
	ID         uint   `gorm:"primaryKey"`
	UserID     uint   `gorm:"index;not null"`
	Name       string `gorm:"size:120"` // client label: "iPhone", "CI deploy"
	Hash       string `gorm:"size:64;uniqueIndex"`
	LastUsedAt *time.Time
	ExpiresAt  *time.Time `gorm:"index"`
	CreatedAt  time.Time
}

func (AdminToken) TableName() string { return prefixed("tokens") }
