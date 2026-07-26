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
// Gin mount. Pages render server-side with Tabler, HTMX, and Alpine.js, and
// every resource endpoint also serves JSON via Accept-header negotiation.
package steward
