// Package steward is a server-rendered admin-panel framework for Go — a
// rewrite of the Laravel dcat-admin package.
//
// Steward gives an application a full admin panel from fluent, typed
// resource builders:
//
//	app, _ := steward.New(steward.Config{DB: db, SecretKey: key})
//	posts := steward.Register[Post](app).Title("Posts").Icon("news")
//	posts.Grid(func(g *steward.Grid[Post]) {
//		g.Column("Title").Sortable()
//	})
//	ginsteward.Mount(router, app)
//
// The core is a plain http.Handler; contrib/ginsteward provides a one-line
// Gin mount. Pages render server-side with Basecoat and HTMX, and every
// resource endpoint also serves JSON via Accept-header negotiation.
//
// # Source layout
//
// Admin, Context, Resource[T], Grid[T], Form[T], and Detail[T] all refer to one
// another, so splitting them across packages would only buy import cycles. They
// stay one package and the file names carry the grouping:
//
//	admin, context, routes, middleware, render, cli   the panel and its runtime
//	resource, fieldtable                              registration, model schema
//	grid, grid_render, grid_actions                   the listing
//	form, form_render, form_nested                    create and edit
//	detail                                            the show page
//	dashboard, dashboard_chart, dashboard_aggregate   widgets
//	auth, auth_token, auth_twofactor                  signing in
//	rbac, rbac_policy, models, menu, menu_sync        accounts and authorization
//	repository, repository_gorm, repository_relation  data access
//
// Pieces that need nothing from the panel live under internal/ instead, where
// the compiler holds them to it: htmlsafe (allowlist HTML sanitizing), rules
// (field validation), ratelimit, cron, qr, session, httpmatch, quickdsl.
package steward
