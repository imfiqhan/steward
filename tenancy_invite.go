package steward

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/imfiqhan/steward/internal/session"
)

// TenantInvitation offers one email address a membership with one role. Its
// link carries an HMAC of the row's nonce, so it can be shown again at any
// time and is revoked by renewing the nonce or deleting the row.
type TenantInvitation struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Email          string    `gorm:"size:255;not null"`
	RoleID         uint      `gorm:"not null"`
	Nonce          string    `gorm:"size:64;not null"`
	InvitedBy      uint      `gorm:"not null"`
	ExpiresAt      time.Time `gorm:"not null"`
	AcceptedAt     *time.Time
	AcceptedUserID *uint
	CreatedAt      time.Time

	// Link is the acceptance URL, filled in when the row is shown.
	Link string `gorm:"-"`
}

func (TenantInvitation) TableName() string { return prefixed("tenant_invitations") }

// InvitationTTL is how long an invitation link stays valid.
const InvitationTTL = 7 * 24 * time.Hour

const inviteCookie = "steward_invite"

// inviteCookieMaxAge bounds how long an opened invitation waits for the
// visitor to finish signing in.
const inviteCookieMaxAge = time.Hour

// Status is "accepted", "expired" or "pending".
func (inv *TenantInvitation) Status() string {
	switch {
	case inv.AcceptedAt != nil:
		return "accepted"
	case time.Now().After(inv.ExpiresAt):
		return "expired"
	}
	return "pending"
}

type requestBaseKey struct{}

// inviteError is a refusal worded for the visitor holding the link.
type inviteError string

func (e inviteError) Error() string { return string(e) }

// ---- tokens ----------------------------------------------------------------------

func (a *Panel) inviteToken(inv *TenantInvitation) string {
	id := strconv.FormatUint(uint64(inv.ID), 10)
	m := hmac.New(sha256.New, a.cfg.SecretKey)
	m.Write([]byte("steward-invite:" + id + ":" + inv.Nonce))
	return id + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:24])
}

// invitationByToken returns the pending invitation a token names, or an error
// whose message can be shown to the visitor.
func (a *Panel) invitationByToken(ctx context.Context, token string) (*TenantInvitation, *Tenant, error) {
	bad := inviteError("This invitation link is not valid. Ask for a new one.")
	idPart, _, ok := strings.Cut(token, ".")
	id, err := strconv.ParseUint(idPart, 10, 64)
	if !ok || err != nil {
		return nil, nil, bad
	}
	db := a.db.WithContext(WithoutTenant(ctx))
	var inv TenantInvitation
	if err := db.First(&inv, id).Error; err != nil {
		return nil, nil, bad
	}
	if subtle.ConstantTimeCompare([]byte(a.inviteToken(&inv)), []byte(token)) != 1 {
		return nil, nil, bad
	}
	switch inv.Status() {
	case "accepted":
		return nil, nil, inviteError("This invitation has already been used.")
	case "expired":
		return nil, nil, inviteError("This invitation has expired. Ask for a new one.")
	}
	var t Tenant
	if err := db.First(&t, inv.TenantID).Error; err != nil || !t.Active {
		return nil, nil, bad
	}
	return &inv, &t, nil
}

func (a *Panel) inviteURL(ctx context.Context, inv *TenantInvitation) string {
	base := strings.TrimRight(a.cfg.PublicURL, "/")
	if base == "" {
		base, _ = ctx.Value(requestBaseKey{}).(string)
	}
	return base + a.url("auth/invite", a.inviteToken(inv))
}

// ---- workspace roles ---------------------------------------------------------------

// workspaceRoles returns the roles a tenant may assign, per TenancyConfig.Roles.
func (a *Panel) workspaceRoles(ctx context.Context) ([]Role, error) {
	db := a.db.WithContext(WithoutTenant(ctx)).Order("id")
	if slugs := a.cfg.Tenancy.Roles; slugs != nil {
		db = db.Where("slug IN ?", slugs)
	}
	var roles []Role
	err := db.Find(&roles).Error
	return roles, err
}

func (a *Panel) workspaceRoleOptions(c *Context) Options {
	roles, err := a.workspaceRoles(c.Ctx())
	if err != nil {
		return Options{}
	}
	o := Options{}
	for _, r := range roles {
		o[strconv.FormatUint(uint64(r.ID), 10)] = r.Name
	}
	return o
}

// allowedRoleIDs filters raw role IDs to those a tenant may assign. An ID
// outside the list is an error rather than silently dropped.
func (a *Panel) allowedRoleIDs(ctx context.Context, raw []string) ([]uint, error) {
	roles, err := a.workspaceRoles(ctx)
	if err != nil {
		return nil, err
	}
	allowed := map[uint]bool{}
	for _, r := range roles {
		allowed[r.ID] = true
	}
	out := make([]uint, 0, len(raw))
	for _, s := range raw {
		if strings.TrimSpace(s) == "" {
			continue
		}
		id, err := strconv.ParseUint(s, 10, 64)
		if err != nil || !allowed[uint(id)] {
			return nil, errors.New("that role cannot be assigned in a workspace")
		}
		out = append(out, uint(id))
	}
	return out, nil
}

func (a *Panel) administratorRoleID(ctx context.Context) uint {
	var r Role
	if err := a.db.WithContext(WithoutTenant(ctx)).Where("slug = ?", RoleAdministrator).First(&r).Error; err != nil {
		return 0
	}
	return r.ID
}

// canAddMember asks TenancyConfig.CanAddMember, allowing when it is unset.
func (a *Panel) canAddMember(ctx context.Context, tenantID uint) error {
	if a.cfg.Tenancy == nil || a.cfg.Tenancy.CanAddMember == nil {
		return nil
	}
	return a.cfg.Tenancy.CanAddMember(ctx, tenantID)
}

// ---- resource ----------------------------------------------------------------------

// invitationsPolicy lets the administrators of a tenant manage its
// invitations. Creating one needs a tenant to invite into.
type invitationsPolicy struct{}

func (invitationsPolicy) ViewAny(c *Context) bool { return c.IsTenantAdmin() || c.IsPlatformAdmin() }
func (invitationsPolicy) View(c *Context, _ *TenantInvitation) bool {
	return c.IsTenantAdmin() || c.IsPlatformAdmin()
}
func (invitationsPolicy) Create(c *Context) bool                  { return c.IsTenantAdmin() }
func (invitationsPolicy) Update(*Context, *TenantInvitation) bool { return false }
func (invitationsPolicy) Delete(c *Context, _ *TenantInvitation) bool {
	return c.IsTenantAdmin() || c.IsPlatformAdmin()
}

// invitationRepo fills in Link on the rows it returns.
type invitationRepo struct {
	*GormRepository[TenantInvitation]
	panel *Panel
}

func (r invitationRepo) List(ctx context.Context, q *ListQuery) ([]TenantInvitation, int64, error) {
	rows, n, err := r.GormRepository.List(ctx, q)
	for i := range rows {
		rows[i].Link = r.panel.inviteURL(ctx, &rows[i])
	}
	return rows, n, err
}

func (r invitationRepo) Find(ctx context.Context, id string) (*TenantInvitation, error) {
	inv, err := r.GormRepository.Find(ctx, id)
	if err == nil {
		inv.Link = r.panel.inviteURL(ctx, inv)
	}
	return inv, err
}

func (a *Panel) registerInvitationsResource() {
	repo, err := NewGormRepository[TenantInvitation](a.db)
	if err != nil {
		a.verifyErrs = append(a.verifyErrs, err)
		return
	}
	res := Register[TenantInvitation](a).Slug("auth/invitations").Title("Invitations").
		Icon("mail-plus").Group("Admin").
		Repository(invitationRepo{GormRepository: repo, panel: a}).
		Policy(invitationsPolicy{})

	roleName := func(id uint) string {
		var r Role
		if err := a.db.WithContext(WithoutTenant(context.Background())).First(&r, id).Error; err != nil {
			return ""
		}
		return r.Name
	}
	status := func(inv *TenantInvitation) template.HTML {
		switch inv.Status() {
		case "accepted":
			return template.HTML(`<span class="badge" data-variant="secondary">Accepted</span>`)
		case "expired":
			return template.HTML(`<span class="badge" data-variant="outline">Expired</span>`)
		}
		return template.HTML(`<span class="badge">Pending</span>`)
	}

	res.Grid(func(g *Grid[TenantInvitation]) {
		g.DisableEdit()
		g.Column("Email").Sortable()
		g.ColumnFunc("Role", "Role", func(inv *TenantInvitation) template.HTML {
			return template.HTML(template.HTMLEscapeString(roleName(inv.RoleID)))
		})
		g.ColumnFunc("Status", "Status", status)
		g.Column("ExpiresAt", "Expires").Sortable()
		g.Column("CreatedAt", "Sent").Sortable()
		g.QuickSearch("Email")
		g.RowAction(NewAction("renew", "Renew link", func(c *Context, ids []string) (*Envelope, error) {
			db := a.db.WithContext(c.Ctx())
			for _, id := range ids {
				nonce, err := session.NewToken()
				if err != nil {
					return nil, err
				}
				if err := db.Model(&TenantInvitation{}).Where("id = ? AND accepted_at IS NULL", id).
					Updates(map[string]any{"nonce": nonce, "expires_at": time.Now().Add(InvitationTTL)}).Error; err != nil {
					return nil, err
				}
			}
			return Success("A new link was made. The old one no longer works.").Refresh(), nil
		}).Icon("refresh-cw").Confirm("Make a new link? The current link stops working."))
	})

	res.Detail(func(d *Detail[TenantInvitation]) {
		d.Field("Email")
		d.FieldFunc("Role", "Role", func(inv *TenantInvitation) template.HTML {
			return template.HTML(template.HTMLEscapeString(roleName(inv.RoleID)))
		})
		d.FieldFunc("Status", "Status", status)
		d.FieldFunc("Link", "Invitation link", func(inv *TenantInvitation) template.HTML {
			if inv.Status() != "pending" {
				return template.HTML(`<span class="text-muted-foreground">No longer usable</span>`)
			}
			link := template.HTMLEscapeString(inv.Link)
			return template.HTML(`<code class="break-all">`+link+`</code> <button type="button" class="steward-copy" data-steward-copy="`+
				link+`" aria-label="Copy invitation link">`) + a.renderer.icon("copy", "size-3") + `</button>`
		}).Block()
		d.Field("ExpiresAt", "Expires")
		d.Field("AcceptedAt", "Accepted")
		d.Field("CreatedAt", "Sent")
	})

	res.Form(func(f *Form[TenantInvitation]) {
		f.Email("Email").Rules("required|email|max:255").
			Help("They join by opening the invitation link and signing in with any account.")
		f.Select("RoleID", "Role").OptionsFunc(a.workspaceRoleOptions).Rules("required")
		f.Saving(func(c *Context, inv *TenantInvitation) error {
			t := c.Tenant()
			if t == nil {
				return errors.New("choose a workspace before inviting")
			}
			inv.Email = strings.ToLower(strings.TrimSpace(inv.Email))
			if _, err := a.allowedRoleIDs(c.Ctx(), []string{strconv.FormatUint(uint64(inv.RoleID), 10)}); err != nil {
				return err
			}
			db := a.db.WithContext(WithoutTenant(c.Ctx()))
			var members int64
			db.Model(&User{}).Where("LOWER(email) = ? AND id IN (?)", inv.Email,
				db.Model(&TenantUser{}).Select("user_id").Where("tenant_id = ?", t.ID)).Count(&members)
			if members > 0 {
				return errors.New(inv.Email + " is already a member of this workspace")
			}
			var pending int64
			db.Model(&TenantInvitation{}).Where("tenant_id = ? AND email = ? AND accepted_at IS NULL AND expires_at > ?",
				t.ID, inv.Email, time.Now()).Count(&pending)
			if pending > 0 {
				return errors.New(inv.Email + " already has a pending invitation; renew its link instead")
			}
			if err := a.canAddMember(c.Ctx(), t.ID); err != nil {
				return err
			}
			nonce, err := session.NewToken()
			if err != nil {
				return err
			}
			inv.Nonce = nonce
			inv.InvitedBy = c.User.ID
			inv.ExpiresAt = time.Now().Add(InvitationTTL)
			return nil
		})
		f.Saved(func(c *Context, inv *TenantInvitation, created bool) error {
			if !created || a.cfg.Mailer == nil {
				return nil
			}
			return a.mailInvitation(c, inv)
		})
	})
}

func (a *Panel) mailInvitation(c *Context, inv *TenantInvitation) error {
	t := c.Tenant()
	link := a.inviteURL(c.Ctx(), inv)
	who := displayName(c.User)
	subject := fmt.Sprintf("%s invited you to %s on %s", who, t.Name, a.cfg.Brand)
	text := fmt.Sprintf("%s invited you to join %s on %s.\n\nOpen this link to accept:\n%s\n\nThe link expires on %s.\n",
		who, t.Name, a.cfg.Brand, link, inv.ExpiresAt.Format("2 January 2006"))
	html := fmt.Sprintf(`<p>%s invited you to join <strong>%s</strong> on %s.</p><p><a href="%s">Accept the invitation</a></p><p>The link expires on %s.</p>`,
		template.HTMLEscapeString(who), template.HTMLEscapeString(t.Name), template.HTMLEscapeString(a.cfg.Brand),
		template.HTMLEscapeString(link), inv.ExpiresAt.Format("2 January 2006"))
	if err := a.cfg.Mailer.Send(c.Ctx(), Mail{To: []string{inv.Email}, Subject: subject, Text: text, HTML: html}); err != nil {
		a.log.Error("steward: send invitation", "email", inv.Email, "err", err)
		return errors.New("the invitation was saved but the email could not be sent; copy the link instead")
	}
	return nil
}

// ---- accepting -----------------------------------------------------------------------

// invitePage handles GET {Prefix}/auth/invite/{token}.
func (a *Panel) invitePage(c *Context) error {
	token := c.R.PathValue("token")
	inv, t, err := a.invitationByToken(c.Ctx(), token)
	data := map[string]any{
		"Token":         token,
		"Providers":     a.loginProviders(),
		"PasswordLogin": !a.cfg.DisablePasswordLogin,
		"SignedIn":      c.User != nil,
	}
	if err != nil {
		data["Error"] = err.Error()
		c.W.WriteHeader(http.StatusNotFound)
		return a.renderStandalone(c, "auth/invite.html", data)
	}
	data["Workspace"] = t.Name
	data["Role"] = a.roleName(c.Ctx(), inv.RoleID)
	if c.User != nil {
		data["Username"] = displayName(c.User)
	} else if err := a.setInviteCookie(c, token); err != nil {
		return err
	}
	return a.renderStandalone(c, "auth/invite.html", data)
}

// inviteAccept handles POST {Prefix}/auth/invite/{token} for a signed-in user.
func (a *Panel) inviteAccept(c *Context) error {
	if c.User == nil {
		return c.Redirect(a.url("auth/invite", c.R.PathValue("token")))
	}
	inv, t, err := a.invitationByToken(c.Ctx(), c.R.PathValue("token"))
	if err != nil {
		c.Flash("error", err.Error())
		return c.Redirect(a.url("/"))
	}
	if err := a.acceptInvite(c.Ctx(), inv, c.User.ID); err != nil {
		c.Flash("error", err.Error())
		return c.Redirect(a.url("/"))
	}
	c.sess.TenantID = t.ID
	c.Flash("success", "You joined "+t.Name+".")
	return c.Redirect(a.url("/"))
}

func (a *Panel) roleName(ctx context.Context, id uint) string {
	var r Role
	if err := a.db.WithContext(WithoutTenant(ctx)).First(&r, id).Error; err != nil {
		return ""
	}
	return r.Name
}

// acceptInvite makes userID a member of the invitation's tenant with its
// role, and marks the invitation used. The seat was counted when the
// invitation was created, so CanAddMember is not asked again.
func (a *Panel) acceptInvite(ctx context.Context, inv *TenantInvitation, userID uint) error {
	return a.db.WithContext(WithoutTenant(ctx)).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		res := tx.Model(&TenantInvitation{}).
			Where("id = ? AND accepted_at IS NULL AND expires_at > ?", inv.ID, now).
			Updates(map[string]any{"accepted_at": now, "accepted_user_id": userID})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errors.New("this invitation has already been used or has expired")
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&TenantUser{TenantID: inv.TenantID, UserID: userID, CreatedAt: now}).Error; err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&TenantUserRole{TenantID: inv.TenantID, UserID: userID, RoleID: inv.RoleID, CreatedAt: now}).Error
	})
}

// acceptPendingInvite runs once a visitor who opened an invitation link is
// signed in, whichever way they signed in. It moves the session into the
// tenant they joined and reports the outcome as a flash.
func (a *Panel) acceptPendingInvite(w http.ResponseWriter, r *http.Request, user *User) {
	token := a.readInviteCookie(r)
	if token == "" {
		return
	}
	http.SetCookie(w, a.inviteCookieFor(r, "", -1))
	sess := sessionOf(r)
	if sess == nil {
		return
	}
	flash := session.Flash{Type: "success"}
	inv, t, err := a.invitationByToken(r.Context(), token)
	if err == nil {
		err = a.acceptInvite(r.Context(), inv, user.ID)
	}
	if err != nil {
		flash = session.Flash{Type: "error", Message: err.Error()}
	} else {
		sess.TenantID = t.ID
		flash.Message = "You joined " + t.Name + "."
	}
	sess.Flashes = append(sess.Flashes, flash)
	a.saveSessionW(w, r, sess)
}

func (a *Panel) setInviteCookie(c *Context, token string) error {
	val, err := a.codec.Encode(&session.Data{CSRF: token})
	if err != nil {
		return err
	}
	http.SetCookie(c.W, a.inviteCookieFor(c.R, val, int(inviteCookieMaxAge.Seconds())))
	return nil
}

// pendingInviteToken is the invitation the visitor opened, or "" when there
// is none or invitations are off.
func (a *Panel) pendingInviteToken(r *http.Request) string {
	if !a.invitationsEnabled() {
		return ""
	}
	return a.readInviteCookie(r)
}

func (a *Panel) readInviteCookie(r *http.Request) string {
	ck, err := r.Cookie(inviteCookie)
	if err != nil || ck.Value == "" {
		return ""
	}
	d, err := a.codec.Decode(ck.Value)
	if err != nil {
		return ""
	}
	return d.CSRF
}

// inviteCookieFor is Lax: it is read on same-site navigations only, and an
// SSO round trip carries the token in the sealed SSO state instead.
func (a *Panel) inviteCookieFor(r *http.Request, val string, maxAge int) *http.Cookie {
	p := a.cfg.Prefix
	if p == "" {
		p = "/"
	}
	return &http.Cookie{
		Name: inviteCookie, Value: val, Path: p, HttpOnly: true, MaxAge: maxAge,
		SameSite: http.SameSiteLaxMode, Secure: a.secureRequest(r),
	}
}
