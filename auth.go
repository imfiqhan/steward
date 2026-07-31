package steward

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/imfiqhan/steward/internal/session"
)

// errBadCredentials covers both an unknown username and a wrong password, so
// callers cannot accidentally tell the two apart in a response.
var errBadCredentials = errors.New("steward: invalid credentials")

// dummyHash is compared against when the username is unknown, so that path
// costs the same as a real bcrypt check.
const dummyHash = "$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B0Wxb1c9dyO0uMYPZ1a1C6q1n1Ga"

// authenticate verifies a username/password pair with Roles preloaded for
// downstream permission checks. Used by both the login form and, when
// Config.EnableTokenAuth is set, the token endpoint.
func (a *Admin) authenticate(ctx context.Context, username, password string) (*AdminUser, error) {
	if username == "" || password == "" {
		return nil, errBadCredentials
	}
	var user AdminUser
	err := a.db.WithContext(ctx).Preload("Roles").Where("username = ?", username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Equalize timing between unknown-user and wrong-password paths.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password))
		return nil, errBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if !comparePassword(user.Password, password) {
		return nil, errBadCredentials
	}
	return &user, nil
}

// comparePassword checks a plaintext password against a stored bcrypt hash.
func comparePassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func (a *Admin) loginPage(c *Context) error {
	if c.User != nil {
		return c.Redirect(a.url("/"))
	}
	return a.renderStandalone(c, "auth/login.html", map[string]any{
		"Error":    "",
		"CanReset": a.cfg.Mailer != nil,
	})
}

func (a *Admin) loginSubmit(c *Context) error {
	if err := c.R.ParseForm(); err != nil {
		return err
	}
	username := strings.TrimSpace(c.R.PostFormValue("username"))
	password := c.R.PostFormValue("password")

	fail := func() error {
		c.W.WriteHeader(http.StatusUnprocessableEntity)
		return a.renderStandalone(c, "auth/login.html", map[string]any{
			"Error":    "These credentials do not match our records.",
			"Username": username,
			"CanReset": a.cfg.Mailer != nil,
		})
	}
	user, err := a.authenticate(c.Ctx(), username, password)
	if errors.Is(err, errBadCredentials) {
		return fail()
	}
	if err != nil {
		return err
	}

	// Application-level account state gets its say before any session exists,
	// so a suspended account never reaches the panel or the 2FA challenge.
	if a.cfg.LoginCheck != nil {
		if err := a.cfg.LoginCheck(c.Ctx(), user); err != nil {
			a.log.Info("steward: login refused", "user", user.Username, "reason", err)
			c.W.WriteHeader(http.StatusUnprocessableEntity)
			return a.renderStandalone(c, "auth/login.html", map[string]any{
				"Error":    err.Error(),
				"Username": username,
				"CanReset": a.cfg.Mailer != nil,
			})
		}
	}

	// A correct password alone is not a session when a second factor is due.
	if a.twoFactorRequired(user) {
		if err := a.beginTwoFactor(c, user.ID); err != nil {
			return err
		}
		return c.Redirect(a.url("auth/2fa"))
	}

	if err := c.login(user.ID); err != nil {
		return err
	}
	a.log.Info("steward: login", "user", user.Username)
	c.Flash("success", "Welcome back, "+displayName(user)+".")
	return c.Redirect(a.url("/"))
}

func (a *Admin) logoutHandler(c *Context) error {
	c.logout()
	return c.Redirect(a.url("auth/login"))
}

func displayName(u *AdminUser) string {
	if u.Name != "" {
		return u.Name
	}
	return u.Username
}

// ---- password reset (enabled when Config.Mailer is set) ---------------------
//
// The reset token is a sealed session payload with a sentinel CSRF value —
// stateless, expiring, and invalidated by rotating the secret key.

const resetPurpose = "steward-password-reset"

func (a *Admin) makeResetToken(uid uint) (string, error) {
	return a.codec.Encode(&session.Data{UID: uid, CSRF: resetPurpose})
}

// parseResetToken accepts tokens younger than an hour.
func (a *Admin) parseResetToken(token string) (uint, bool) {
	d, err := a.codec.Decode(token)
	if err != nil || d.CSRF != resetPurpose || d.UID == 0 {
		return 0, false
	}
	if time.Since(time.Unix(d.IssuedAt, 0)) > time.Hour {
		return 0, false
	}
	return d.UID, true
}

func (a *Admin) forgotPage(c *Context) error {
	return a.renderStandalone(c, "auth/forgot.html", map[string]any{"Sent": false})
}

func (a *Admin) forgotSubmit(c *Context) error {
	if err := c.R.ParseForm(); err != nil {
		return err
	}
	email := strings.TrimSpace(c.R.PostFormValue("email"))
	// Constant response whether or not the account exists.
	respond := func() error {
		return a.renderStandalone(c, "auth/forgot.html", map[string]any{"Sent": true, "Email": email})
	}
	if email == "" {
		return respond()
	}
	var user AdminUser
	err := a.db.WithContext(c.Ctx()).Where("email = ?", email).First(&user).Error
	if err != nil {
		return respond()
	}
	token, err := a.makeResetToken(user.ID)
	if err != nil {
		return err
	}
	link := requestOrigin(c.R) + a.url("auth/reset") + "?token=" + url.QueryEscape(token)
	mail := Mail{
		To:      []string{email},
		Subject: a.cfg.Brand + ": reset your password",
		Text: "Someone requested a password reset for your " + a.cfg.Brand + " account.\n\n" +
			"Reset it within one hour: " + link + "\n\nIf this wasn't you, ignore this message.",
	}
	if err := a.cfg.Mailer.Send(c.Ctx(), mail); err != nil {
		a.log.Error("steward: reset mail", "err", err)
	}
	return respond()
}

// requestOrigin reconstructs scheme://host for building absolute links.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (a *Admin) resetPage(c *Context) error {
	token := c.R.URL.Query().Get("token")
	if _, ok := a.parseResetToken(token); !ok {
		c.W.WriteHeader(http.StatusForbidden)
		return a.renderStandalone(c, "auth/reset.html", map[string]any{"Invalid": true})
	}
	return a.renderStandalone(c, "auth/reset.html", map[string]any{"Token": token})
}

func (a *Admin) resetSubmit(c *Context) error {
	if err := c.R.ParseForm(); err != nil {
		return err
	}
	token := c.R.PostFormValue("token")
	uid, ok := a.parseResetToken(token)
	if !ok {
		c.W.WriteHeader(http.StatusForbidden)
		return a.renderStandalone(c, "auth/reset.html", map[string]any{"Invalid": true})
	}
	password := c.R.PostFormValue("password")
	confirm := c.R.PostFormValue("password_confirmation")
	if len(password) < 5 || len(password) > 72 {
		return a.renderStandalone(c, "auth/reset.html", map[string]any{
			"Token": token, "Error": "Password must be between 5 and 72 characters."})
	}
	if password != confirm {
		return a.renderStandalone(c, "auth/reset.html", map[string]any{
			"Token": token, "Error": "Password confirmation does not match."})
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := a.db.WithContext(c.Ctx()).Model(&AdminUser{}).Where("id = ?", uid).
		Update("password", string(hash)).Error; err != nil {
		return err
	}
	c.Flash("success", "Password updated — sign in with your new password.")
	return c.Redirect(a.url("auth/login"))
}

// renderProfile renders the profile page. tf carries the two-factor section's
// state (nil for the plain view); errs and name preserve a rejected edit.
func (a *Admin) renderProfile(c *Context, tf *twoFactorVM, errs map[string]string, name ...string) error {
	if tf == nil {
		tf = a.twoFactorProfileVM(c)
	}
	if errs == nil {
		errs = map[string]string{}
	}
	data := map[string]any{"Errors": errs, "TwoFactor": tf}
	if len(name) > 0 {
		data["Name"] = name[0]
	}
	return a.render(c, "auth/profile.html", "Profile", data)
}

func (a *Admin) profilePage(c *Context) error {
	return a.renderProfile(c, nil, nil)
}

func (a *Admin) profileSubmit(c *Context) error {
	if err := c.R.ParseForm(); err != nil {
		return err
	}
	name := strings.TrimSpace(c.R.PostFormValue("name"))
	oldPassword := c.R.PostFormValue("old_password")
	newPassword := c.R.PostFormValue("password")
	confirm := c.R.PostFormValue("password_confirmation")

	errs := map[string]string{}
	if name == "" {
		errs["name"] = "Name is required."
	}
	updates := map[string]any{"name": name}

	if newPassword != "" {
		switch {
		case len(newPassword) < 5 || len(newPassword) > 72:
			errs["password"] = "Password must be between 5 and 72 characters."
		case newPassword != confirm:
			errs["password_confirmation"] = "Password confirmation does not match."
		case bcrypt.CompareHashAndPassword([]byte(c.User.Password), []byte(oldPassword)) != nil:
			errs["old_password"] = "Current password is incorrect."
		default:
			hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			updates["password"] = string(hash)
		}
	}

	if len(errs) > 0 {
		c.W.WriteHeader(http.StatusUnprocessableEntity)
		return a.renderProfile(c, nil, errs, name)
	}

	if err := a.db.WithContext(c.Ctx()).Model(c.User).Updates(updates).Error; err != nil {
		return err
	}
	c.Flash("success", "Profile updated.")
	return c.Redirect(a.url("auth/profile"))
}
