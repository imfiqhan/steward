package steward

import "fmt"

// Extension is Steward's Go-idiomatic take on dcat-admin's extensions: a
// package that contributes resources, pages, actions, or hooks through the
// same public API applications use. Being compiled code, extensions are
// installed with `go get` and wired with Use — there is no runtime
// marketplace, by design.
//
//	type AuditLog struct{}
//	func (AuditLog) Name() string { return "audit-log" }
//	func (AuditLog) Setup(a *steward.Admin) error {
//	    steward.Register[audit.Entry](a).Slug("audit").Group("Admin")
//	    return nil
//	}
//
//	app.Use(AuditLog{})
type Extension interface {
	Name() string
	Setup(a *Admin) error
}

// Use registers extensions; Setup runs immediately (so extensions can
// Register resources), and must happen before Build.
func (a *Admin) Use(exts ...Extension) error {
	if a.built {
		return fmt.Errorf("steward: Use called after Build")
	}
	for _, ext := range exts {
		if err := ext.Setup(a); err != nil {
			return fmt.Errorf("steward: extension %q: %w", ext.Name(), err)
		}
		a.log.Info("steward: extension enabled", "name", ext.Name())
	}
	return nil
}
