// Package ginsteward mounts a steward.Admin on a Gin router. It is the only
// package in the module that imports Gin; the core stays a plain
// http.Handler, so any router works via the same pattern.
package ginsteward

import (
	"net/http"

	"github.com/gin-gonic/gin"

	steward "github.com/imfiqhan/steward"
)

// Mount builds the admin and registers it under its prefix. Build errors
// (bad config, failed migrations, invalid resource definitions) surface here
// rather than on the first request.
func Mount(r gin.IRouter, a *steward.Admin) error {
	if err := a.Build(); err != nil {
		return err
	}
	h := gin.WrapH(a)
	prefix := a.Prefix()
	r.Any(prefix, h)
	r.Any(prefix+"/*path", h)
	return nil
}

var _ http.Handler = (*steward.Admin)(nil)
