# Steward

A server-rendered admin-panel framework for Go — a rewrite of the excellent
[dcat-admin](https://github.com/jqhph/dcat-admin) (Laravel), built on
[Basecoat](https://basecoatui.com) (shadcn/ui-style components on Tailwind CSS),
HTMX, GORM, and Go generics.

> **Status: pre-release, under active development.** APIs will change.

```go
app, _ := steward.New(steward.Config{DB: db, SecretKey: key})

posts := steward.Register[Post](app).Title("Posts").Icon("news")
posts.Grid(func(g *steward.Grid[Post]) {
    g.Column("Title").Limit(40).Sortable()
    g.Column("Status").Badge(map[any]string{"draft": "secondary", "published": "green"})
    g.QuickSearch("Title", "Body")
})
posts.Form(func(f *steward.Form[Post]) {
    f.Text("Title").Rules("required|max:255")
    f.Markdown("Body")
})

ginsteward.Mount(router, app) // or mount app as a plain http.Handler
```

## Highlights

- **Fluent, typed builders** — Grid / Form / Detail declared in Go; callbacks
  receive your model type, never `map[string]any`.
- **Zero-config CRUD** — `steward.Register[User](app)` alone yields a working
  resource; every field remains individually overridable.
- **RBAC + menu administration** — roles, permissions (path matching), and a
  drag-and-drop menu manager, enforced by policies at resource, row, and
  field level.
- **Versioned migrations** — embedded framework migrations plus a runner for
  your own; no silent AutoMigrate schema drift.
- **Headless-ready** — every resource endpoint also serves JSON via
  `Accept: application/json`, with opt-in bearer tokens so API scripts and
  mobile clients authenticate without a cookie or a CSRF handshake.
- **Single binary, no Node anywhere** — the UI bundle (Tailwind v4 +
  Basecoat + htmx) is compiled by the esbuild Go API and the Tailwind
  standalone binary (`make assets`), committed, and shipped via `go:embed`;
  override any template by dropping a file in your project.
- **Scaffolding CLI** — `steward new`, `steward make:resource` (from a field
  spec, a live database, or a Go struct) with DB-type → field-type inference.

## Features

Checked items work end-to-end today; unchecked items are on the roadmap.

### Resources

- [x] Typed `Grid[T]` / `Form[T]` / `Detail[T]` builders on any GORM model
- [x] Zero-config CRUD — `steward.Register[User](app)` alone is a working resource
- [x] `Repository[T]` seam (GORM default; SQLite, MySQL, Postgres) with
      preloads and base scopes
- [x] Boot-time `Verify()` — every column reference checked at startup,
      not at click time
- [x] Headless JSON API on every resource endpoint
      (`Accept: application/json`) plus a `_schema` endpoint
- [x] Bearer-token auth for API and mobile clients (`EnableTokenAuth`),
      CSRF-exempt, inheriting the token owner's roles and policies; the
      token endpoint is rate-limited per username and per client IP
- [x] HTMX fragment navigation (SPA feel, server-rendered)

### Grid

- [x] Sortable columns and display helpers (badge, bool, link, image,
      truncate, copyable, custom `Display`)
- [x] Quick-search DSL (`field:value`, `>n`, `%contains%`)
- [x] Filter panel (equals, like, greater/less, between, date range, select)
- [x] Windowed pagination (`1 … 18 19 20 … 37`) with per-page selector
- [x] CSV export
- [x] Batch delete and custom row/batch/tool actions, confirmed via
      alert dialogs
- [x] Inline editing (`Column.Editable()`, `Column.Switch()`) routed through
      form validation
- [x] Tree grids (`Grid.Tree`) and grouped column headers
- [x] Column show/hide picker (persisted per grid)
- [x] Drag-and-drop row reordering (`Grid.Reorderable`)
- [ ] Fixed (pinned) columns
- [ ] Quick-create row
- [ ] Quick search backed by a `Searcher` (SQL `LIKE` today)

### Form

- [x] 22 field kinds, including File/Image uploads via the `Storage` interface
      and a `Richtext` HTML editor whose input is allowlist-sanitized server-side
- [x] Declarative validation rules (`required|max:255|unique:posts,title,{id}`)
      with separate creation/update rules
- [x] Typed hooks — `Submitted` / `Saving` / `Saved` / `Deleting` / `Deleted`
      receive `*T`, never maps
- [x] `BelongsTo` searchable select and `MultiSelect` pivot sync
- [x] hasMany nested row forms (`steward.HasMany[T,C]`, dcat protocol)
- [x] Fieldset and divider layout
- [x] Dirty-field-only updates; 422 inline errors in both HTML and JSON
- [ ] Embeds (JSON-column nested forms)
- [ ] File/Image/BelongsTo fields inside hasMany rows
- [x] Per-request conditional fields (`Field.Show`) — hidden fields are refused
      on submit and omitted from `_schema`, not merely hidden
- [ ] Tabbed form layout

### Detail

- [x] Field renderers (badge, bool, image, link, `HTML`, custom `As`)
- [x] Embedded relation grids (`steward.RelationGrid[T,C]`)

### Dashboard & widgets

- [x] Widget templates — `card`, `metric` (KPI), `alert`, and `lazy`
      (HTMX load-after-paint)
- [x] `Dashboard` builder — widgets and their column span declared in Go,
      each with a typed data callback; a failing widget reports in place
      instead of blanking the page
- [x] Widgets fetched individually via `Lazy()`, so one slow aggregate never
      blocks the page; an empty result set reports "no data" rather than a fault
- [x] Charts via Basecoat's Chart component (bar, line, pie, doughnut, radar,
      stacked) — themed by `--chart-N`, typed column-oriented Go API, runtime
      served per page rather than bundled. `make vendor-chart` once
- [x] Aggregate helpers over `Repository[T]` — `Count`, `Sum`, `GroupCount`,
      `PeriodCount`, `PeriodSum`, chart-ready via `AggRows.Chart`; day/month/
      year buckets on SQLite, MySQL, and Postgres

Page composition stays in templates rather than a Go `Row`/`Column`/`Layout`
object graph like dcat-admin's: Tailwind plus the template overlay already
cover it, and an HTML DSL in Go would be more surface for less flexibility.

### Auth, RBAC & administration

- [x] Encrypted cookie sessions, CSRF protection, bcrypt passwords
- [x] TOTP two-factor authentication — self-service enrolment with an
      in-process QR code, single-use recovery codes, replay-proof codes, and an
      optional panel-wide `Require2FA`
- [x] `Config.LoginCheck` to refuse an account (suspended, not yet activated)
- [x] Password reset flow via the SMTP `Mailer`
- [x] Roles and permissions with dcat-compatible HTTP path matching
- [x] `Policy[T]` per action plus `RowScoper` row-level scoping;
      menu visibility derives from policies
- [x] Menu administration — drag-and-drop tree, sync-from-code
- [x] Operation log (passwords masked) and settings key-value store
- [x] Profile page and `admin:create-user`
- [ ] Permission definitions synced from registered resources (the
      menu-sync pattern: code owns the canonical entries, roles are
      granted in the DB; hand-written path rules stay as the escape hatch)

### Platform & tooling

- [x] Versioned migrations (batches, up/down/status) — no silent AutoMigrate
      drift
- [x] `steward` CLI — `new`, `make:resource` (from a field spec, a live
      database, or a Go struct, with type inference), `make:migration`,
      `publish`
- [x] App runtime commands — `serve`, `worker`, `migrate`, `menu:sync`,
      `admin:create-user`
- [x] Cron scheduler (`@every 10m`, `@daily`, five-field cron) running in a
      separate worker process, deployable independently of the panel
- [ ] Background job queue (enqueue from the panel, process in the worker)
- [x] `Cache` (in-memory built in, Redis in `contrib/`), `Storage` (local
      built in, S3 in `contrib/`), SMTP `Mailer`
- [x] `Searcher` interface with an in-memory full-text implementation
- [x] Template overlay (override any view by dropping a file), embedded
      Lucide icons, dark mode, `no_ui` build tag
- [x] Node-free asset pipeline — esbuild Go API + Tailwind standalone binary
- [x] Mount under Gin (`contrib/ginsteward`) or any `http.Handler` router

## Development

```sh
make build   # build library + example
make test    # run tests
make lint    # golangci-lint
make run     # run the example app (SQLite, http://localhost:8080/admin)
```

The example app seeds a default panel account, **admin / admin** — change it
immediately on any instance that isn't a local sandbox (or create your own
with `go run . admin:create-user` and delete the seeded one).

## License

MIT — see [LICENSE](LICENSE). Vendored frontend assets keep their own
licenses: [Basecoat](frontend/vendor/basecoat/LICENSE.md) (MIT),
[htmx](frontend/vendor/htmx/LICENSE) (0BSD),
[Lucide](assets/icons/LICENSE) (ISC).
