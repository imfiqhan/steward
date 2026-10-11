package steward

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/imfiqhan/steward/internal/session"
)

// AuthProvider is an external sign-in method: an OpenID Connect provider,
// Sign in with Apple, a SAML identity provider. Steward owns the routes, the
// state cookie and the session; a provider only builds the redirect and
// verifies what comes back.
//
// Providers are opt-in: only those listed in Config.AuthProviders (or
// returned by Config.AuthProviderSource) get routes and a login button.
type AuthProvider interface {
	// ID is the provider's URL slug, e.g. "google". It must be unique.
	ID() string
	// Label is the button text, e.g. "Continue with Google".
	Label() string
	// Icon names the button icon: a Lucide name or an overlay SVG name.
	Icon() string
	// AuthURL returns where to send the browser. It may record values it
	// needs at the callback (nonce, PKCE verifier) in req.Values.
	AuthURL(ctx context.Context, req *SSORequest) (string, error)
	// Exchange verifies the callback request and returns who signed in.
	Exchange(ctx context.Context, req *SSORequest, r *http.Request) (*ExternalIdentity, error)
}

// MetadataProvider is implemented by providers that publish service-provider
// metadata (SAML). It is served at {Prefix}/auth/sso/{id}/metadata.
type MetadataProvider interface {
	Metadata(ctx context.Context, callbackURL string) (body []byte, contentType string, err error)
}

// AuthProviderSource supplies providers that are not known at startup, such
// as one SAML connection per tenant.
type AuthProviderSource interface {
	// Provider returns the provider with this ID, or nil when there is none.
	Provider(ctx context.Context, id string) (AuthProvider, error)
	// Discover returns the provider for an email address or organization
	// slug typed on the login page, or nil when there is none.
	Discover(ctx context.Context, hint string) (AuthProvider, error)
}

// SSORequest carries one sign-in attempt from AuthURL to Exchange.
type SSORequest struct {
	// State is the opaque value the identity provider must echo back
	// (OAuth "state", SAML "RelayState"). Steward checks it.
	State string
	// CallbackURL is the absolute URL the provider must return to.
	CallbackURL string
	// Values holds provider data kept until the callback in a sealed,
	// short-lived cookie.
	Values map[string]string
}

// ExternalIdentity is a verified account at an identity provider.
type ExternalIdentity struct {
	Provider      string
	Subject       string // stable, unique per provider
	Email         string
	EmailVerified bool
	Name          string
	// TenantID, when set, is the tenant the provider belongs to (a tenant's
	// own SAML connection). The session is moved into it after sign-in.
	TenantID uint
	Raw      map[string]any
}

// UserIdentity links an account to an identity at an external provider.
type UserIdentity struct {
	ID          uint   `gorm:"primaryKey"`
	UserID      uint   `gorm:"index;not null"`
	Provider    string `gorm:"size:64;not null;uniqueIndex:idx_identity_provider_subject"`
	Subject     string `gorm:"size:255;not null;uniqueIndex:idx_identity_provider_subject"`
	Email       string `gorm:"size:255"`
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

func (UserIdentity) TableName() string { return prefixed("user_identities") }

// ErrNoLinkedAccount is returned by the default external-user resolver when
// no account is linked to the identity.
var ErrNoLinkedAccount = errors.New("no account is linked to this sign-in")

// LoginRefusedError is returned by LoginAs when Config.LoginCheck refuses
// the account. Its message is safe to show.
type LoginRefusedError struct{ Reason error }

func (e *LoginRefusedError) Error() string { return e.Reason.Error() }
func (e *LoginRefusedError) Unwrap() error { return e.Reason }

// LoginAs signs user in on this request after the application has verified
// them by its own means (an SSO callback, a magic link). It runs
// Config.LoginCheck, starts the two-factor challenge when one is due, and
// otherwise issues the session. It returns where to send the browser.
func (c *Context) LoginAs(user *User) (string, error) {
	a := c.Panel
	if a.cfg.LoginCheck != nil {
		if err := a.cfg.LoginCheck(c.Ctx(), user); err != nil {
			return "", &LoginRefusedError{Reason: err}
		}
	}
	if a.twoFactorRequired(user) {
		if err := a.beginTwoFactor(c, user.ID); err != nil {
			return "", err
		}
		return a.url("auth/2fa"), nil
	}
	if err := c.login(user.ID); err != nil {
		return "", err
	}
	return a.url("/"), nil
}

// ---- routes ------------------------------------------------------------------

const ssoCookie = "steward_sso"

// ssoMaxAge bounds how long a sign-in may sit at the identity provider.
const ssoMaxAge = 10 * time.Minute

// ssoState is sealed into ssoCookie between the redirect and the callback.
type ssoState struct {
	Provider string            `json:"p"`
	State    string            `json:"s"`
	Values   map[string]string `json:"v,omitempty"`
	Expires  int64             `json:"e"`
}

func (a *Panel) ssoEnabled() bool {
	return len(a.cfg.AuthProviders) > 0 || a.cfg.AuthProviderSource != nil
}

func (a *Panel) verifyAuthProviders() []error {
	var errs []error
	seen := map[string]bool{}
	for _, p := range a.cfg.AuthProviders {
		if p == nil {
			errs = append(errs, errors.New("steward: Config.AuthProviders contains nil"))
			continue
		}
		id := p.ID()
		switch {
		case id == "" || strings.ContainsAny(id, "/?#% "):
			errs = append(errs, fmt.Errorf("steward: auth provider ID %q must be a URL slug", id))
		case seen[id]:
			errs = append(errs, fmt.Errorf("steward: auth provider ID %q is used twice", id))
		}
		seen[id] = true
	}
	return errs
}

// provider finds a provider by ID among the configured ones, then the source.
func (a *Panel) provider(ctx context.Context, id string) (AuthProvider, error) {
	for _, p := range a.cfg.AuthProviders {
		if p.ID() == id {
			return p, nil
		}
	}
	if a.cfg.AuthProviderSource != nil {
		return a.cfg.AuthProviderSource.Provider(ctx, id)
	}
	return nil, nil
}

// publicBase returns the scheme and host the browser reached the panel on.
func (a *Panel) publicBase(r *http.Request) string {
	if a.cfg.PublicURL != "" {
		return strings.TrimRight(a.cfg.PublicURL, "/")
	}
	scheme := "http"
	if a.secureRequest(r) {
		scheme = "https"
	}
	host := r.Host
	if a.cfg.TrustProxy {
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host = strings.TrimSpace(strings.Split(h, ",")[0])
		}
	}
	return scheme + "://" + host
}

// secureRequest reports whether the browser used HTTPS, trusting
// X-Forwarded-Proto only with Config.TrustProxy.
func (a *Panel) secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return a.cfg.TrustProxy && strings.EqualFold(
		strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

func (a *Panel) ssoCallbackURL(r *http.Request, id string) string {
	return a.publicBase(r) + a.url("auth/sso", id, "callback")
}

// ssoStart handles GET {Prefix}/auth/sso/{provider}.
func (a *Panel) ssoStart(c *Context) error {
	p, err := a.provider(c.Ctx(), c.R.PathValue("provider"))
	if err != nil {
		return err
	}
	if p == nil {
		http.NotFound(c.W, c.R)
		return nil
	}
	state, err := session.NewToken()
	if err != nil {
		return err
	}
	req := &SSORequest{State: state, CallbackURL: a.ssoCallbackURL(c.R, p.ID()), Values: map[string]string{}}
	dest, err := p.AuthURL(c.Ctx(), req)
	if err != nil {
		a.log.Error("steward: sso start", "provider", p.ID(), "err", err)
		return a.ssoFail(c, "Sign-in with "+p.Label()+" is unavailable right now.")
	}
	if err := a.setSSOCookie(c, &ssoState{
		Provider: p.ID(), State: state, Values: req.Values,
		Expires: time.Now().Add(ssoMaxAge).Unix(),
	}); err != nil {
		return err
	}
	http.Redirect(c.W, c.R, dest, http.StatusFound)
	return nil
}

// ssoCallback handles GET and POST {Prefix}/auth/sso/{provider}/callback.
// POST arrives cross-site (Apple form_post, SAML ACS), so it is exempt from
// CSRF and relies on the sealed state cookie instead.
func (a *Panel) ssoCallback(c *Context) error {
	id := c.R.PathValue("provider")
	st := a.readSSOCookie(c.R)
	a.clearSSOCookie(c)

	p, err := a.provider(c.Ctx(), id)
	if err != nil {
		return err
	}
	if p == nil {
		http.NotFound(c.W, c.R)
		return nil
	}
	if err := c.R.ParseForm(); err != nil {
		return a.ssoFail(c, "The sign-in response could not be read.")
	}
	got := c.R.FormValue("state")
	if got == "" {
		got = c.R.FormValue("RelayState")
	}
	if st == nil || st.Provider != id || time.Now().Unix() > st.Expires ||
		subtle.ConstantTimeCompare([]byte(got), []byte(st.State)) != 1 {
		return a.ssoFail(c, "That sign-in attempt expired or was started elsewhere. Please try again.")
	}
	if e := c.R.FormValue("error"); e != "" {
		a.log.Info("steward: sso cancelled", "provider", id, "error", e)
		return a.ssoFail(c, "Sign-in with "+p.Label()+" was cancelled.")
	}

	req := &SSORequest{State: st.State, CallbackURL: a.ssoCallbackURL(c.R, id), Values: st.Values}
	ident, err := p.Exchange(c.Ctx(), req, c.R)
	if err != nil {
		a.log.Info("steward: sso exchange failed", "provider", id, "err", err)
		return a.ssoFail(c, "Sign-in with "+p.Label()+" could not be verified.")
	}
	ident.Provider = id

	user, err := a.ResolveExternalUser(c.Ctx(), ident)
	if errors.Is(err, ErrNoLinkedAccount) {
		a.log.Info("steward: sso without account", "provider", id, "email", ident.Email)
		return a.ssoFail(c, "No account is linked to that "+p.Label()+" sign-in. Ask an administrator to invite you.")
	}
	if err != nil {
		return err
	}

	dest, err := c.LoginAs(user)
	var refused *LoginRefusedError
	if errors.As(err, &refused) {
		return a.ssoFail(c, refused.Error())
	}
	if err != nil {
		return err
	}
	if ident.TenantID != 0 && a.TenancyEnabled() {
		c.sess.TenantID = ident.TenantID
		a.saveSession(c)
	}
	a.log.Info("steward: login", "user", user.Username, "via", id)
	if dest == a.url("/") {
		c.Flash("success", "Welcome back, "+displayName(user)+".")
	}
	http.Redirect(c.W, c.R, dest, http.StatusFound)
	return nil
}

// ssoMetadata handles GET {Prefix}/auth/sso/{provider}/metadata.
func (a *Panel) ssoMetadata(c *Context) error {
	p, err := a.provider(c.Ctx(), c.R.PathValue("provider"))
	if err != nil {
		return err
	}
	mp, ok := p.(MetadataProvider)
	if p == nil || !ok {
		http.NotFound(c.W, c.R)
		return nil
	}
	body, ctype, err := mp.Metadata(c.Ctx(), a.ssoCallbackURL(c.R, p.ID()))
	if err != nil {
		return err
	}
	c.W.Header().Set("Content-Type", ctype)
	_, err = c.W.Write(body)
	return err
}

// ssoDiscover handles POST {Prefix}/auth/sso/discover: an email address or
// organization slug is mapped to a provider through AuthProviderSource.
func (a *Panel) ssoDiscover(c *Context) error {
	hint := strings.TrimSpace(c.R.PostFormValue("organization"))
	if hint == "" || a.cfg.AuthProviderSource == nil {
		return a.ssoFail(c, "Enter your work email or organization.")
	}
	p, err := a.cfg.AuthProviderSource.Discover(c.Ctx(), hint)
	if err != nil {
		return err
	}
	if p == nil {
		return a.ssoFail(c, "No single sign-on is set up for that organization.")
	}
	return c.Redirect(a.url("auth/sso", p.ID()))
}

// ssoCallbackPath matches {Prefix}/auth/sso/{provider}/callback, whose POST
// is authenticated by the sealed state cookie rather than the CSRF token.
func (a *Panel) ssoCallbackPath(p string) bool {
	if !a.ssoEnabled() {
		return false
	}
	rel := strings.TrimPrefix(p, a.cfg.Prefix)
	return strings.HasPrefix(rel, "/auth/sso/") && strings.HasSuffix(rel, "/callback") &&
		strings.Count(rel, "/") == 4
}

func (a *Panel) ssoFail(c *Context, msg string) error {
	c.W.WriteHeader(http.StatusUnprocessableEntity)
	return a.renderLogin(c, msg, "")
}

func (a *Panel) setSSOCookie(c *Context, st *ssoState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	val, err := a.codec.Encode(&session.Data{CSRF: string(raw)})
	if err != nil {
		return err
	}
	http.SetCookie(c.W, a.ssoCookieFor(c.R, val, int(ssoMaxAge.Seconds())))
	return nil
}

func (a *Panel) readSSOCookie(r *http.Request) *ssoState {
	ck, err := r.Cookie(ssoCookie)
	if err != nil {
		return nil
	}
	d, err := a.codec.Decode(ck.Value)
	if err != nil {
		return nil
	}
	var st ssoState
	if json.Unmarshal([]byte(d.CSRF), &st) != nil {
		return nil
	}
	return &st
}

func (a *Panel) clearSSOCookie(c *Context) {
	http.SetCookie(c.W, a.ssoCookieFor(c.R, "", -1))
}

// ssoCookieFor builds the state cookie. Over HTTPS it is SameSite=None so a
// cross-site POST callback carries it; browsers reject SameSite=None without
// Secure, so plain HTTP falls back to Lax, which covers GET callbacks only.
func (a *Panel) ssoCookieFor(r *http.Request, val string, maxAge int) *http.Cookie {
	ck := &http.Cookie{
		Name: ssoCookie, Value: val, Path: a.url("auth/sso"),
		HttpOnly: true, MaxAge: maxAge, SameSite: http.SameSiteLaxMode,
	}
	if a.secureRequest(r) {
		ck.Secure = true
		ck.SameSite = http.SameSiteNoneMode
	}
	return ck
}

// ---- account resolution --------------------------------------------------------

// ResolveExternalUser maps a verified identity to an account. With
// Config.ResolveExternalUser set it defers to that function; otherwise it
// uses a linked identity, then, with Config.LinkByVerifiedEmail, an account
// whose email matches a verified email. Either way the identity is linked to
// the account it resolved to, and the account comes back with Roles loaded.
func (a *Panel) ResolveExternalUser(ctx context.Context, ident *ExternalIdentity) (*User, error) {
	resolve := a.cfg.ResolveExternalUser
	if resolve == nil && a.cfg.LinkByVerifiedEmail {
		resolve = MatchVerifiedEmail(a.db)
	}
	return ResolveIdentity(ctx, a.db, ident, resolve)
}

// ResolveIdentity maps an external identity to an account without a Panel,
// for processes that verify identities themselves (a mobile API checking a
// Google ID token). A linked identity wins; otherwise resolve decides, and a
// nil resolve, nil account or ErrNoLinkedAccount refuses. The identity is
// linked to the account it resolves to, which comes back with Roles loaded.
func ResolveIdentity(ctx context.Context, db *gorm.DB, ident *ExternalIdentity,
	resolve func(context.Context, *ExternalIdentity) (*User, error)) (*User, error) {
	db = db.WithContext(WithoutTenant(ctx))
	if ident.Provider == "" || ident.Subject == "" {
		return nil, errors.New("steward: external identity without provider or subject")
	}

	var link UserIdentity
	err := db.Where("provider = ? AND subject = ?", ident.Provider, ident.Subject).First(&link).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if resolve == nil {
			return nil, ErrNoLinkedAccount
		}
		u, err := resolve(ctx, ident)
		if err != nil {
			return nil, err
		}
		if u == nil || u.ID == 0 {
			return nil, ErrNoLinkedAccount
		}
		link = UserIdentity{UserID: u.ID, Provider: ident.Provider, Subject: ident.Subject, Email: ident.Email}
		if err := db.Create(&link).Error; err != nil {
			return nil, err
		}
	}

	db.Model(&UserIdentity{}).Where("id = ?", link.ID).
		Updates(map[string]any{"last_login_at": time.Now(), "email": ident.Email})

	var user User
	if err := db.Preload("Roles").First(&user, link.UserID).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// MatchVerifiedEmail returns a resolver for ResolveIdentity that picks the
// account whose email equals the identity's, only when the provider marked
// that email verified.
func MatchVerifiedEmail(db *gorm.DB) func(context.Context, *ExternalIdentity) (*User, error) {
	return func(ctx context.Context, ident *ExternalIdentity) (*User, error) {
		if !ident.EmailVerified || ident.Email == "" {
			return nil, ErrNoLinkedAccount
		}
		var u User
		err := db.WithContext(WithoutTenant(ctx)).
			Where("LOWER(email) = ?", strings.ToLower(ident.Email)).First(&u).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNoLinkedAccount
		}
		if err != nil {
			return nil, err
		}
		return &u, nil
	}
}

// Identities returns the external sign-ins linked to an account.
func (a *Panel) Identities(ctx context.Context, userID uint) ([]UserIdentity, error) {
	var out []UserIdentity
	err := a.db.WithContext(WithoutTenant(ctx)).Where("user_id = ?", userID).Order("provider").Find(&out).Error
	return out, err
}

// ---- login page ----------------------------------------------------------------

type loginProviderVM struct {
	ID, Label, Icon, URL string
}

func (a *Panel) loginProviders() []loginProviderVM {
	out := make([]loginProviderVM, 0, len(a.cfg.AuthProviders))
	for _, p := range a.cfg.AuthProviders {
		out = append(out, loginProviderVM{ID: p.ID(), Label: p.Label(), Icon: p.Icon(), URL: a.url("auth/sso", p.ID())})
	}
	return out
}

// renderLogin renders the login page with an optional error.
func (a *Panel) renderLogin(c *Context, errMsg, username string) error {
	return a.renderStandalone(c, "auth/login.html", map[string]any{
		"Error":         errMsg,
		"Username":      username,
		"CanReset":      a.cfg.Mailer != nil && !a.cfg.DisablePasswordLogin,
		"PasswordLogin": !a.cfg.DisablePasswordLogin,
		"Providers":     a.loginProviders(),
		"Discovery":     a.cfg.AuthProviderSource != nil,
	})
}
