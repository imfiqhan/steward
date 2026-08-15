package steward

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SignedURLStorage is the optional half of Storage: a backend that can hand out
// a URL good for a limited time, so the bucket behind it never has to be public.
// An S3-compatible backend implements this with its own presigning; LocalStorage
// implements it below.
//
// A Storage that does not implement it keeps working — StorageURL falls back to
// the plain URL, which for LocalStorage is the panel's own authenticated route.
type SignedURLStorage interface {
	SignedURL(ctx context.Context, name string, ttl time.Duration) (string, error)
}

// defaultSignedURLTTL is how long a signed link stays good when Config does not
// say. Long enough to open a page and click through it, short enough that a URL
// pasted into a chat expires before it is read.
const defaultSignedURLTTL = 15 * time.Minute

// ErrNotSigned reports that a name could not be signed.
var ErrNotSigned = errors.New("storage: cannot sign this name")

// signedURL appends an expiry and a signature to a path.
func signedURL(base string, key []byte, disk, name string, ttl time.Duration) string {
	exp := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	sig := uploadSignature(key, disk, name, exp)
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "exp=" + exp + "&sig=" + sig
}

// uploadSignature covers the disk, the stored name and the expiry together.
// Signing them separately would let one signature's expiry be pasted onto
// another name, or a link to one disk be pointed at another.
func uploadSignature(key []byte, disk, name, exp string) string {
	mac := hmac.New(sha256.New, key)
	for _, part := range []string{disk, name, exp} {
		mac.Write([]byte(part))
		mac.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// validUploadSignature reports whether a request carries a signature good for
// this name and still in date.
func validUploadSignature(key []byte, disk, name string, q url.Values) bool {
	exp, sig := q.Get("exp"), q.Get("sig")
	if exp == "" || sig == "" || len(key) == 0 {
		return false
	}
	secs, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > secs {
		return false
	}
	want := uploadSignature(key, disk, name, exp)
	// Constant time: a byte-by-byte comparison leaks how much of a guess was
	// right, which is enough to find the rest a byte at a time.
	return hmac.Equal([]byte(sig), []byte(want))
}

// SignedURL implements SignedURLStorage. The signature is made with the panel's
// own secret, so a local upload can be linked to for a while without the route
// that serves it being open to everyone.
func (s *LocalStorage) SignedURL(_ context.Context, name string, ttl time.Duration) (string, error) {
	cleaned, err := s.clean(name)
	if err != nil {
		return "", err
	}
	if len(s.SigningKey) == 0 {
		return "", ErrNotSigned
	}
	return signedURL(s.URL(cleaned), s.SigningKey, s.name, cleaned, ttl), nil
}

// signedURLTTL is the configured lifetime, or the default.
func (a *Admin) signedURLTTL() time.Duration {
	if a.cfg.SignedURLTTL > 0 {
		return a.cfg.SignedURLTTL
	}
	return defaultSignedURLTTL
}

// uploadGuard refuses a request for a stored file that carries neither a session
// nor a signature.
//
// The route used to be open: the file server was mounted outside the panel's
// authentication, so anyone who knew a path could read any upload without
// logging in.
func (a *Admin) uploadGuard(disk string, next http.Handler) http.Handler {
	prefix := a.uploadRoutePrefix(disk)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, prefix)
		if unescaped, err := url.PathUnescape(name); err == nil {
			name = unescaped
		}
		if a.uploadRequestAllowed(r, disk, name) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "Not found.", http.StatusNotFound)
	})
}

// uploadRequestAllowed is the check itself: a public disk, a valid signature, or
// a session that may see the panel at all.
func (a *Admin) uploadRequestAllowed(r *http.Request, disk, name string) bool {
	if d, ok := a.Disk(disk); ok && d.Public {
		return true
	}
	if validUploadSignature(a.cfg.SecretKey, disk, name, r.URL.Query()) {
		return true
	}
	return userOf(r) != nil
}
