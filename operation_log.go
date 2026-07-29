package steward

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
)

// maskedFields are never logged in clear text.
var maskedFields = []string{"password", "password_confirmation", "old_password", "_token", "secret", "token"}

func maskValue(v string) string {
	if len(v) <= 3 {
		return "******"
	}
	return v[:3] + "******"
}

// withOperationLog records mutating requests (method, path, ip, masked
// input) after the handler runs. Failures are logged, never surfaced.
func (a *Admin) withOperationLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := userOf(r)
		mutating := r.Method == http.MethodPost || r.Method == http.MethodPut ||
			r.Method == http.MethodPatch || r.Method == http.MethodDelete
		rel := "/" + strings.TrimLeft(strings.TrimPrefix(r.URL.Path, a.cfg.Prefix), "/")
		// /auth/token is skipped like /auth/login: the request carries a
		// password and the response carries a credential.
		skip := !mutating || user == nil ||
			strings.Contains(rel, "/_upload") ||
			rel == "/auth/login" || rel == "/auth/token"

		next.ServeHTTP(w, r)
		if skip {
			return
		}

		input := map[string]any{}
		if r.Form != nil {
			for k, vs := range r.Form {
				val := strings.Join(vs, ",")
				lower := strings.ToLower(k)
				for _, m := range maskedFields {
					if strings.Contains(lower, m) {
						val = maskValue(val)
						break
					}
				}
				input[k] = val
			}
		}
		raw, _ := json.Marshal(input)
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		p := r.URL.Path
		if len(p) > 255 {
			p = p[:255]
		}
		entry := OperationLog{
			UserID: user.ID,
			Path:   p,
			Method: r.Method,
			IP:     host,
			Input:  string(raw),
		}
		// Fire-and-forget on a fresh context: the request context is done.
		go func() {
			if err := a.db.Create(&entry).Error; err != nil {
				a.log.Error("steward: operation log", "err", err)
			}
		}()
	})
}
