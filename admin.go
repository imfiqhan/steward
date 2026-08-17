package steward

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"reflect"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/imfiqhan/steward/internal/migrations"
	"github.com/imfiqhan/steward/internal/ratelimit"
	"github.com/imfiqhan/steward/internal/session"
	"github.com/imfiqhan/steward/migrate"
)

// Config configures one Admin. DB and SecretKey are required; everything else
// has a working default.
type Config struct {
	DB *gorm.DB

	// Prefix is the URL the panel mounts under (default "/admin").
	Prefix string

	// Brand names the panel in the sidebar and titles (default "Steward").
	Brand string

	// CurrencySymbol prefixes every Currency field (default "$"). A single
	// field overrides it with Field.Symbol.
	CurrencySymbol string

	// SignedURLTTL is how long a link to a stored file stays good
	// (default 15 minutes).
	SignedURLTTL time.Duration

	// PublicUploads makes the default disk public. Prefer naming a disk in
	// Disks and setting Disk.Public, which lets one panel keep both kinds.
	PublicUploads bool

	// Disks are the named places files can be stored, each public or private.
	// A File or Image field picks one with Field.Disk; without Disks a panel
	// has exactly one, named by DefaultDisk, backed by Storage.
	//
	//	Disks: map[string]steward.Disk{
	//	    "public":  {Public: true},                 // local, under UploadDir/public
	//	    "private": {},                             // local, gated and signed
	//	    "media":   {Storage: s3, Public: false},   // presigned S3
	//	}
	Disks map[string]Disk

	// ExportDisk is where finished background exports are written. Empty means
	// DefaultDisk, which is usually right — but not when the default disk's
	// directory is served by something other than the panel, since an export
	// carries whatever rows its owner could read.
	ExportDisk string

	// DefaultDisk is where an upload goes when its field names no disk
	// (default "local").
	DefaultDisk string

	// TablePrefix names the framework tables (default "admin_"). It is
	// process-global; two Admins with different prefixes in one process are
	// not supported.
	TablePrefix string

	// SecretKey signs and encrypts sessions, CSRF, and remember tokens.
	// Changing it invalidates all sessions. Minimum 16 bytes.
	SecretKey []byte

	// BackgroundExportRows is the match size past which a whole-table export
	// becomes a job rather than a download. Zero means the default (10,000);
	// negative always streams, whatever the size.
	BackgroundExportRows int

	// DisableExportWorker stops the panel process from building queued
	// exports. Something else then has to call RunPendingExports — a worker's
	// scheduler — or nothing does and they stay pending.
	DisableExportWorker bool

	// DisableNotifications hides the bell in the header and unmounts its
	// endpoints. The table is still created, so turning it back on later
	// needs no migration.
	DisableNotifications bool

	// DisableAutoMigrate skips running the embedded framework migrations at
	// Build. Recommended in production: run them explicitly via the app's
	// `migrate up` command instead.
	DisableAutoMigrate bool

	// Dev re-parses templates on every request and serves assets uncached.
	Dev bool

	// TemplatesFS overlays the embedded templates; files here win. Paths
	// mirror the embedded tree, e.g. "layout/sidebar.html".
	TemplatesFS fs.FS

	// AssetsFS overlays the embedded static assets (e.g. extra icons under
	// "icons/name.svg").
	AssetsFS fs.FS

	// UploadDir is LocalStorage's root when no Storage is supplied
	// (default "./uploads").
	UploadDir string

	Cache   Cache   // default: in-process MemoryCache
	Storage Storage // default: LocalStorage at UploadDir

	// Searcher backs quick search and the command palette for resources that
	// declared Searchable. Without one they fall back to SQL LIKE.
	Searcher Searcher
	Mailer   Mailer // optional; enables password reset

	// AuthExcept lists extra path patterns (relative to Prefix, * globs)
	// that skip authentication and permission checks.
	AuthExcept []string

	// GridActions chooses how every grid presents a row's actions:
	// GridActionsButtons (the default) lays them side by side, GridActionsMenu
	// collapses them behind one trigger. A single grid can differ via
	// Grid.ActionStyle.
	GridActions GridActionStyle

	// FilterLayout chooses where every grid's filter panel lives:
	// FiltersAbove (the default) opens it in place between the toolbar and the
	// rows, FiltersDrawer opens it over the page from the right. A single grid
	// can differ via Grid.FilterLayout.
	FilterLayout GridFilterLayout

	// Require2FA makes TOTP two-factor authentication mandatory: an account
	// that has not enrolled is redirected to its profile page and can reach
	// nothing else until it does. Off by default, in which case each user
	// decides for themselves from the same page.
	//
	// Bearer-token clients are exempt — they hold an explicit credential that
	// is already separately scoped, and have no session to enrol through.
	Require2FA bool

	// LoginCheck runs after the password (and second factor, if any) has been
	// accepted and before the session is issued. A returned error refuses the
	// login and its message is shown on the form, so it is the seam for
	// application-level account state: "suspended", "not yet activated",
	// "outside permitted hours".
	//
	// It cannot be used to *grant* a login, only to withhold one.
	LoginCheck func(ctx context.Context, u *AdminUser) error

	// EnableTokenAuth accepts "Authorization: Bearer <token>" alongside the
	// session cookie, and mounts POST/DELETE {Prefix}/auth/token so API and
	// mobile clients can mint and revoke their own credentials.
	//
	// Off by default: enabling it exposes a credential-issuing endpoint that
	// takes a username and password, so it should be a deliberate choice
	// rather than something an upgrade turns on. Tokens inherit their user's
	// roles, permissions, and policies — scope an API client by giving it its
	// own AdminUser with a restricted role, not the administrator account.
	EnableTokenAuth bool

	// TokenTTL bounds how long an issued token stays valid. Zero means the
	// default of 30 days; a negative duration means tokens never expire.
	TokenTTL time.Duration

	// TokenRateLimit caps attempts on {Prefix}/auth/token within
	// TokenRateWindow, counted per username. Client IPs are capped in the same
	// window at six times this figure — looser, because a proxy collapses many
	// clients onto one address, so the per-username bound is what really
	// protects an account. Successful and failed attempts both count.
	//
	// Zero means 5 per window; a negative value disables limiting. Limits are
	// per process, so N replicas admit N times the rate.
	TokenRateLimit int

	// TokenRateWindow is the rate-limit window. Zero means one minute.
	TokenRateWindow time.Duration

	Logger *slog.Logger
}

// Admin is the panel: a plain http.Handler serving everything under
// Config.Prefix. Register resources against it, then mount it (ginsteward.
// Mount or http.Handle) — the first request triggers Build automatically.
type Admin struct {
	cfg Config
	db  *gorm.DB
	log *slog.Logger

	codec    *session.Codec
	renderer *renderer

	registry []resourceEntry

	// exportWake nudges the queued-export worker; nil when the panel is not
	// running one.
	exportWake chan struct{}
	bySlug     map[string]resourceEntry
	byType     map[reflect.Type]resourceEntry

	// disks are the configured storage disks, always holding at least the
	// default one.
	disks map[string]Disk

	// commandSources are extra searchable sections in the command palette.
	commandSources []namedCommandSource

	mux          *http.ServeMux
	handler      http.Handler
	assetVersion string

	dash         *Dashboard
	tokenLimiter *ratelimit.Limiter
	twoFALimiter *ratelimit.Limiter

	buildOnce sync.Once
	buildErr  error
	built     bool

	verifyErrs []error
}

type resourceEntry interface {
	meta() *resourceMeta
	compile(a *Admin) error
	registerRoutes(a *Admin, mux *http.ServeMux)
	renderRelation(c *Context, title string, q *ListQuery) (*detailRelVM, error)
	menuVisible(c *Context) bool
}

// New validates the config and returns an unbuilt Admin. Resource
// registration happens between New and Build.
func New(cfg Config) (*Admin, error) {
	if cfg.DB == nil {
		return nil, errors.New("steward: Config.DB is required")
	}
	if len(cfg.SecretKey) < 16 {
		return nil, errors.New("steward: Config.SecretKey must be at least 16 bytes")
	}
	// Normalised to either "" (mounted at the root) or "/segment", with no
	// trailing slash, so that every pattern and URL below can concatenate it
	// without special-casing. Build URLs with a.url, never by concatenation:
	// at the root the prefix is empty and "" + "/x" is only correct by luck.
	cfg.Prefix = strings.TrimRight(cfg.Prefix, "/")
	if cfg.Prefix != "" && !strings.HasPrefix(cfg.Prefix, "/") {
		cfg.Prefix = "/" + cfg.Prefix
	}
	if cfg.Brand == "" {
		cfg.Brand = "Steward"
	}
	if cfg.CurrencySymbol == "" {
		cfg.CurrencySymbol = "$"
	}
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = "admin_"
	}
	if cfg.UploadDir == "" {
		cfg.UploadDir = "./uploads"
	}
	if cfg.Cache == nil {
		cfg.Cache = NewMemoryCache()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	tablePrefix.Store(cfg.TablePrefix)
	disks, err := buildDisks(&cfg)
	if err != nil {
		return nil, err
	}

	codec, err := session.NewCodec(cfg.SecretKey)
	if err != nil {
		return nil, fmt.Errorf("steward: %w", err)
	}

	a := &Admin{
		cfg:    cfg,
		db:     cfg.DB,
		log:    cfg.Logger,
		codec:  codec,
		bySlug: map[string]resourceEntry{},
		byType: map[reflect.Type]resourceEntry{},
		disks:  disks,
	}
	a.tokenLimiter = ratelimit.New(a.tokenRateWindow())
	a.twoFALimiter = ratelimit.New(twoFactorRateWindow)
	return a, nil
}

// Prefix returns the mount path ("/admin").
func (a *Admin) Prefix() string { return a.cfg.Prefix }

// DB returns the underlying GORM handle.
func (a *Admin) DB() *gorm.DB { return a.db }

// url joins segments onto the prefix.
func (a *Admin) url(parts ...string) string {
	// The leading "/" is passed to Join rather than concatenated because the
	// prefix is empty for a panel mounted at the root.
	return path.Join(append([]string{"/", a.cfg.Prefix}, parts...)...)
}

// Build freezes registration: wires join tables, runs framework migrations
// (unless disabled), compiles resources, parses templates, and constructs the
// route table. Calling it more than once is a no-op returning the first
// result; ServeHTTP calls it lazily.
func (a *Admin) Build() error {
	a.buildOnce.Do(func() {
		a.buildErr = a.build()
		if a.buildErr == nil {
			a.startExportWorker()
		}
	})
	return a.buildErr
}

// Verify runs Build and returns every configuration error collected during
// resource compilation, joined. Assert it in a test to catch bad column
// references at CI time instead of request time.
func (a *Admin) Verify() error {
	if err := a.Build(); err != nil {
		return err
	}
	return errors.Join(a.verifyErrs...)
}

func (a *Admin) build() error {
	a.registerBuiltins()

	for _, jt := range []struct {
		model any
		field string
		join  any
	}{
		{&AdminUser{}, "Roles", &RoleUser{}},
		{&Role{}, "Permissions", &RolePermission{}},
	} {
		if err := a.db.SetupJoinTable(jt.model, jt.field, jt.join); err != nil {
			return fmt.Errorf("steward: join table for %T.%s: %w", jt.model, jt.field, err)
		}
	}

	if !a.cfg.DisableAutoMigrate {
		if _, err := a.MigrationRunner(nil).Up(context.Background()); err != nil {
			return fmt.Errorf("steward: framework migrations: %w", err)
		}
	}

	for _, r := range a.registry {
		if err := r.compile(a); err != nil {
			return fmt.Errorf("steward: compiling resource %q: %w", r.meta().slug, err)
		}
	}

	if err := a.syncMenu(context.Background()); err != nil {
		return fmt.Errorf("steward: menu sync: %w", err)
	}

	rend, err := newRenderer(a)
	if err != nil {
		return fmt.Errorf("steward: templates: %w", err)
	}
	a.renderer = rend
	a.assetVersion = rend.assetVersion

	// Icon names can only be checked once the asset layers exist, so this runs
	// after the renderer rather than during resource compilation. An unknown
	// name renders blank at runtime instead of failing, which is easy to miss —
	// reporting it here means a test asserting Verify catches it.
	for _, r := range a.registry {
		m := r.meta()
		if m.icon != "" && !rend.hasIcon(m.icon) {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: icon %q not found; available: %s",
				m.slug, m.icon, strings.Join(rend.iconNames(), ", ")))
		}
	}

	// A chart tile draws nothing without its runtime, and says so only on the
	// tile itself — which reaches whoever opens the dashboard, not whoever
	// deployed it. The runtime ships with the module, so this firing means the
	// assets being served are not the ones that were built.
	// A dashboard's tree is known at boot, so the glyphs and colours it names
	// are checked here rather than found missing on the page.
	if a.dash != nil {
		for _, w := range a.dash.allWidgets() {
			if w.icon != "" && !rend.hasIcon(w.icon) {
				a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
					"dashboard widget %q: icon %q not found; available: %s",
					w.title, w.icon, strings.Join(rend.iconNames(), ", ")))
			}
			if w.tone != "" && !badgeColors[w.tone] {
				a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
					"dashboard widget %q: unknown colour %q (known colours: %s)",
					w.title, w.tone, strings.Join(badgeColorNames(), ", ")))
			}
		}
		verifyNodes(a, "dashboard layout", a.dash.nodes)
	}
	if a.dash != nil && a.dash.hasChartWidget() {
		for _, name := range chartRuntimeAssets {
			if _, err := readLayered(rend.assetLayers, name); err != nil {
				a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
					"dashboard has a chart widget but %s is not in the asset layers; "+
						"it ships with the module, so check AssetsFS is not shadowing it", name))
			}
		}
	}

	a.mux = a.buildRoutes()
	a.handler = a.wrap(a.mux)
	a.built = true
	return nil
}

// MigrationRunner returns a runner with the framework's core migrations
// registered plus any app migrations supplied. Used by Build (AutoMigrate)
// and by the app-side CLI.
func (a *Admin) MigrationRunner(app []migrate.Migration) *migrate.Runner {
	r := migrate.New(a.db, migrate.WithTable(a.cfg.TablePrefix+"migrations"))
	r.Register("core", migrations.Core(a.coreTables())...)
	if len(app) > 0 {
		r.Register("app", app...)
	}
	return r
}

func (a *Admin) coreTables() migrations.Tables {
	return migrations.Tables{
		Models: []any{
			&AdminUser{}, &Role{}, &Permission{}, &MenuItem{},
			&RoleUser{}, &RolePermission{}, &RoleMenu{}, &PermissionMenu{},
			&OperationLog{}, &Setting{},
		},
		TokenModel:          &AdminToken{},
		NotificationModel:   &Notification{},
		ExportModel:         &ExportJob{},
		UserModel:           &AdminUser{},
		AddTwoFactorColumns: twoFactorColumns,
		SeedFn:              seedDefaults,
	}
}

func seedDefaults(tx *gorm.DB, passwordHash string) error {
	role := Role{Name: "Administrator", Slug: RoleAdministrator}
	if err := tx.Create(&role).Error; err != nil {
		return err
	}
	user := AdminUser{Username: "admin", Password: passwordHash, Name: "Administrator"}
	if err := tx.Create(&user).Error; err != nil {
		return err
	}
	if err := tx.Create(&RoleUser{RoleID: role.ID, UserID: user.ID}).Error; err != nil {
		return err
	}
	menu := []MenuItem{
		{Order: 1, Title: "Dashboard", Icon: "home", URI: "/", Show: true, Source: MenuSourceCode, CodeKey: new(string)},
	}
	*menu[0].CodeKey = "_dashboard"
	for i := range menu {
		if err := tx.Create(&menu[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

// ServeHTTP implements http.Handler; the panel behaves identically however
// it is mounted (net/http, Gin via WrapH, chi, etc.).
func (a *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := a.Build(); err != nil {
		a.log.Error("steward: build failed", "err", err)
		http.Error(w, "admin panel failed to initialize; see server logs", http.StatusInternalServerError)
		return
	}
	a.handler.ServeHTTP(w, r)
}

// sessionCookie is the session cookie name.
const sessionCookie = "steward_session"

func (a *Admin) sessionFromRequest(r *http.Request) *session.Data {
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return &session.Data{}
	}
	d, err := a.codec.Decode(ck.Value)
	if err != nil {
		return &session.Data{}
	}
	return d
}

// saveSession seals the context's session into the cookie. Must run before
// the body is written.
func (a *Admin) saveSession(c *Context) {
	val, err := a.codec.Encode(c.sess)
	if err != nil {
		a.log.Error("steward: session encode", "err", err)
		return
	}
	http.SetCookie(c.W, &http.Cookie{
		Name:     sessionCookie,
		Value:    val,
		Path:     a.url("/"),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   c.R.TLS != nil,
		Expires:  time.Now().Add(session.MaxAge),
	})
}

func (a *Admin) clearSession(c *Context) {
	http.SetCookie(c.W, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     a.url("/"),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
