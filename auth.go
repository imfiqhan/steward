package steward

import (
	"errors"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func (a *Admin) loginPage(c *Context) error {
	if c.User != nil {
		return c.Redirect(a.url("/"))
	}
	return a.renderStandalone(c, "auth/login.html", map[string]any{"Error": ""})
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
		})
	}
	if username == "" || password == "" {
		return fail()
	}

	var user AdminUser
	err := a.db.WithContext(c.Ctx()).Preload("Roles").Where("username = ?", username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Equalize timing between unknown-user and wrong-password paths.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B0Wxb1c9dyO0uMYPZ1a1C6q1n1Ga"), []byte(password))
		return fail()
	}
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)) != nil {
		return fail()
	}

	if err := c.login(user.ID); err != nil {
		return err
	}
	a.log.Info("steward: login", "user", user.Username)
	c.Flash("success", "Welcome back, "+displayName(&user)+".")
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

func (a *Admin) profilePage(c *Context) error {
	return a.render(c, "auth/profile.html", "Profile", map[string]any{"Errors": map[string]string{}})
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
		return a.render(c, "auth/profile.html", "Profile", map[string]any{"Errors": errs, "Name": name})
	}

	if err := a.db.WithContext(c.Ctx()).Model(c.User).Updates(updates).Error; err != nil {
		return err
	}
	c.Flash("success", "Profile updated.")
	return c.Redirect(a.url("auth/profile"))
}
