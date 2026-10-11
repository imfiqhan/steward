package steward

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/imfiqhan/steward/internal/session"
)

const signupCookie = "steward_signup"

// signupMaxAge bounds how long a verified identity waits on the signup form.
const signupMaxAge = 15 * time.Minute

// pendingSignup is the verified identity sealed into signupCookie between the
// SSO callback and the signup form.
type pendingSignup struct {
	Provider string `json:"p"`
	Subject  string `json:"s"`
	Email    string `json:"e"`
	Name     string `json:"n"`
	Expires  int64  `json:"x"`
}

func (a *Panel) signupEnabled() bool {
	return a.cfg.Tenancy != nil && a.cfg.Tenancy.Signup != nil
}

func defaultWorkspaceName(person string) string {
	person = strings.TrimSpace(person)
	if person == "" {
		return "My workspace"
	}
	return person + "'s workspace"
}

// beginSignup keeps a verified identity that matches no account and sends the
// browser to the signup form.
func (a *Panel) beginSignup(c *Context, ident *ExternalIdentity, label string) error {
	if !ident.EmailVerified || ident.Email == "" {
		return a.ssoFail(c, "Your "+label+" account has no verified email address, so it cannot be used to sign up.")
	}
	raw, err := json.Marshal(pendingSignup{
		Provider: ident.Provider, Subject: ident.Subject, Email: ident.Email, Name: ident.Name,
		Expires: time.Now().Add(signupMaxAge).Unix(),
	})
	if err != nil {
		return err
	}
	val, err := a.codec.Encode(&session.Data{CSRF: string(raw)})
	if err != nil {
		return err
	}
	http.SetCookie(c.W, a.signupCookieFor(c.R, val, int(signupMaxAge.Seconds())))
	http.Redirect(c.W, c.R, a.url("auth/signup"), http.StatusFound)
	return nil
}

func (a *Panel) readSignupCookie(r *http.Request) *pendingSignup {
	ck, err := r.Cookie(signupCookie)
	if err != nil || ck.Value == "" {
		return nil
	}
	d, err := a.codec.Decode(ck.Value)
	if err != nil {
		return nil
	}
	var p pendingSignup
	if json.Unmarshal([]byte(d.CSRF), &p) != nil || time.Now().Unix() > p.Expires {
		return nil
	}
	return &p
}

func (a *Panel) signupCookieFor(r *http.Request, val string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: signupCookie, Value: val, Path: a.url("auth/signup"), HttpOnly: true, MaxAge: maxAge,
		SameSite: http.SameSiteLaxMode, Secure: a.secureRequest(r),
	}
}

// signupPage handles GET {Prefix}/auth/signup: the form that names the new
// workspace, for a verified identity without an account or a signed-in
// account without a workspace.
func (a *Panel) signupPage(c *Context) error {
	pending := a.readSignupCookie(c.R)
	switch {
	case pending != nil:
		return a.renderSignup(c, pending.Email, defaultWorkspaceName(firstNonEmpty(pending.Name, localPart(pending.Email))), "")
	case c.User != nil && a.hasNoWorkspace(c.Ctx(), c.User.ID):
		return a.renderSignup(c, displayName(c.User), defaultWorkspaceName(displayName(c.User)), "")
	}
	return c.Redirect(a.url("auth/login"))
}

func (a *Panel) renderSignup(c *Context, who, workspace, errMsg string) error {
	return a.renderStandalone(c, "auth/signup.html", map[string]any{
		"Who": who, "WorkspaceName": workspace, "Error": errMsg,
	})
}

// signupSubmit handles POST {Prefix}/auth/signup.
func (a *Panel) signupSubmit(c *Context) error {
	name := strings.TrimSpace(c.R.PostFormValue("workspace"))
	pending := a.readSignupCookie(c.R)
	who := ""
	switch {
	case pending != nil:
		who = pending.Email
	case c.User != nil && a.hasNoWorkspace(c.Ctx(), c.User.ID):
		who = displayName(c.User)
	default:
		return c.Redirect(a.url("auth/login"))
	}
	if name == "" || len(name) > 100 {
		c.W.WriteHeader(http.StatusUnprocessableEntity)
		return a.renderSignup(c, who, name, "Give the workspace a name of up to 100 characters.")
	}

	if pending == nil {
		t, err := a.createWorkspace(c.Ctx(), a.db.WithContext(WithoutTenant(c.Ctx())), c.User, name)
		if err != nil {
			return err
		}
		c.sess.TenantID = t.ID
		c.Flash("success", "Your workspace "+t.Name+" is ready.")
		return c.Redirect(a.url("/"))
	}

	ident := &ExternalIdentity{Provider: pending.Provider, Subject: pending.Subject,
		Email: pending.Email, EmailVerified: true, Name: pending.Name}
	var user *User
	var tenant *Tenant
	err := a.db.WithContext(WithoutTenant(c.Ctx())).Transaction(func(tx *gorm.DB) error {
		var taken int64
		tx.Model(&User{}).Where("LOWER(email) = ?", strings.ToLower(ident.Email)).Count(&taken)
		if taken > 0 {
			return errEmailTaken
		}
		u, err := newAccountFromIdentity(tx, ident)
		if err != nil {
			return err
		}
		t, err := a.createWorkspace(c.Ctx(), tx, u, name)
		if err != nil {
			return err
		}
		user, tenant = u, t
		return nil
	})
	if errors.Is(err, errEmailTaken) {
		c.W.WriteHeader(http.StatusUnprocessableEntity)
		return a.renderSignup(c, who, name, "An account already uses "+ident.Email+". Sign in to it instead.")
	}
	if err != nil {
		return err
	}
	http.SetCookie(c.W, a.signupCookieFor(c.R, "", -1))
	a.log.Info("steward: signup", "user", user.Username, "tenant", tenant.Slug, "via", ident.Provider)

	c.sess.TenantID = tenant.ID
	dest, err := c.LoginAs(user)
	var refused *LoginRefusedError
	if errors.As(err, &refused) {
		return a.renderLogin(c, refused.Error(), "")
	}
	if err != nil {
		return err
	}
	if dest == a.url("/") {
		c.Flash("success", "Welcome to "+a.cfg.Brand+". Your workspace "+tenant.Name+" is ready.")
	}
	return c.Redirect(dest)
}

var errEmailTaken = errors.New("email already in use")

func (a *Panel) hasNoWorkspace(ctx context.Context, userID uint) bool {
	var n int64
	a.db.WithContext(WithoutTenant(ctx)).Model(&TenantUser{}).Where("user_id = ?", userID).Count(&n)
	return n == 0
}

// createWorkspace creates a tenant owned by u, with u as its administrator,
// then runs SignupConfig.Created, all in tx.
func (a *Panel) createWorkspace(ctx context.Context, db *gorm.DB, u *User, name string) (*Tenant, error) {
	var t *Tenant
	err := db.Transaction(func(tx *gorm.DB) error {
		slug, err := uniqueValue(tx, &Tenant{}, "slug", slugify(name, "workspace"), 64)
		if err != nil {
			return err
		}
		t = &Tenant{Slug: slug, Name: name, Active: true}
		if a.ownershipEnabled() {
			owner := u.ID
			t.OwnerUserID = &owner
		}
		if err := tx.Create(t).Error; err != nil {
			return err
		}
		var admin Role
		if err := tx.Where("slug = ?", RoleAdministrator).First(&admin).Error; err != nil {
			return fmt.Errorf("steward: the administrator role is missing: %w", err)
		}
		adminID := admin.ID
		now := time.Now()
		if err := tx.Create(&TenantUser{TenantID: t.ID, UserID: u.ID, CreatedAt: now}).Error; err != nil {
			return err
		}
		if err := tx.Create(&TenantUserRole{TenantID: t.ID, UserID: u.ID, RoleID: adminID, CreatedAt: now}).Error; err != nil {
			return err
		}
		if a.signupEnabled() && a.cfg.Tenancy.Signup.Created != nil {
			return a.cfg.Tenancy.Signup.Created(ctx, tx, u, t)
		}
		return nil
	})
	return t, err
}

// newAccountFromIdentity creates an account for a verified external identity
// and links the identity to it. The account has no usable password; its
// email is kept only when verified and not used by another account.
func newAccountFromIdentity(tx *gorm.DB, ident *ExternalIdentity) (*User, error) {
	base := slugify(localPart(ident.Email), "")
	if base == "" {
		base = slugify(ident.Name, "user")
	}
	username, err := uniqueValue(tx, &User{}, "username", base, 120)
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(base64.RawURLEncoding.EncodeToString(secret)), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u := &User{Username: username, Password: string(hash),
		Name: firstNonEmpty(strings.TrimSpace(ident.Name), localPart(ident.Email), username)}
	if ident.EmailVerified && ident.Email != "" {
		var taken int64
		tx.Model(&User{}).Where("LOWER(email) = ?", strings.ToLower(ident.Email)).Count(&taken)
		if taken == 0 {
			email := ident.Email
			u.Email = &email
		}
	}
	if err := tx.Create(u).Error; err != nil {
		return nil, err
	}
	link := UserIdentity{UserID: u.ID, Provider: ident.Provider, Subject: ident.Subject, Email: ident.Email}
	if err := tx.Create(&link).Error; err != nil {
		return nil, err
	}
	return u, nil
}

var slugJunk = regexp.MustCompile(`[^a-z0-9]+`)

// slugify lowercases s and joins its letters and digits with dashes.
func slugify(s, fallback string) string {
	s = strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	if s == "" {
		return fallback
	}
	return s
}

// uniqueValue returns base, or base-2, base-3, … whichever is not yet used in
// column of model's table.
func uniqueValue(tx *gorm.DB, model any, column, base string, max int) (string, error) {
	for i := 1; i < 1000; i++ {
		v := base
		if i > 1 {
			v = fmt.Sprintf("%s-%d", base, i)
		}
		if len(v) > max {
			v = v[len(v)-max:]
		}
		var n int64
		if err := tx.Model(model).Where(column+" = ?", v).Count(&n).Error; err != nil {
			return "", err
		}
		if n == 0 {
			return v, nil
		}
	}
	return "", fmt.Errorf("steward: no free %s for %q", column, base)
}

func localPart(email string) string {
	at := strings.IndexByte(email, '@')
	if at < 0 {
		return email
	}
	return email[:at]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
