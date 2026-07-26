# Steward

A server-rendered admin-panel framework for Go — a rewrite of the excellent
[dcat-admin](https://github.com/jqhph/dcat-admin) (Laravel), built on
[Tabler](https://tabler.io), HTMX, GORM, and Go generics.

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

## Features

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
  `Accept: application/json`.
- **Single binary** — templates, Tabler, htmx, and icons ship via `go:embed`;
  override any template by dropping a file in your project.
- **Scaffolding CLI** — `steward new`, `steward make:resource` (from a field
  spec, a live database, or a Go struct) with DB-type → field-type inference.

## What's here today

Working end-to-end: auth (sessions, CSRF, password reset via SMTP), versioned
migrations with seeding, Grid (filters, quick-search DSL, CSV export, batch
delete, JSON API), Form (21 field kinds, Laravel-style rules, uploads, typed
hooks), Detail (+ relation grids), RBAC (permission matcher, policies,
role/permission/user admin), menu administration with sync-from-code,
operation log, settings KV, scheduler page, dark mode, `no_ui` builds, and
the `steward` CLI (`new`, `make:resource`, `make:migration`, `publish`).

**Backlog** (deliberately deferred): nested hasMany/embeds forms, tree grids,
inline cell editing, custom row/batch actions, complex headers, column
show/hide, drag-drop menu ordering, `make:resource --from-db/--from-struct`,
Redis cache & S3 storage drivers, full-text Searcher, cron-expression
schedules, extensions system.

## Development

```sh
make build   # build library + example
make test    # run tests
make lint    # golangci-lint
make run     # run the example app (SQLite, http://localhost:8080/admin)
```

## License

MIT — see [LICENSE](LICENSE). Vendored frontend assets keep their own
(MIT/0BSD/Apache-2.0) licenses.
