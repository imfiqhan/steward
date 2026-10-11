package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// workspaceFixture is a tenancy panel with an SSO provider and one tenant:
//
//	acme: olivia (owner, administrator), adam (administrator), eve (enumerator)
//	carol: no membership
type workspaceFixture struct {
	db    *gorm.DB
	all   *gorm.DB
	base  string
	idp   *fakeIDP
	acme  steward.Tenant
	users map[string]steward.User
	roles map[string]steward.Role
}

func newWorkspaceFixture(t *testing.T, tc *steward.TenancyConfig) *workspaceFixture {
	t.Helper()
	db := testDB(t)
	idp := &fakeIDP{id: "fake", codes: map[string]steward.ExternalIdentity{}}
	if tc.Roles == nil {
		tc.Roles = []string{steward.RoleAdministrator, "enumerator"}
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin", DB: db, SecretKey: []byte("workspace-test-secret-key"),
		AuthProviders: []steward.AuthProvider{idp}, Tenancy: tc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	all := db.WithContext(steward.WithoutTenant(context.Background()))
	fx := &workspaceFixture{db: db, all: all, base: serve(t, app).URL + "/admin", idp: idp,
		users: map[string]steward.User{}, roles: map[string]steward.Role{}}

	for _, slug := range []string{"enumerator", "supervisor"} {
		r := steward.Role{Name: strings.ToUpper(slug[:1]) + slug[1:], Slug: slug}
		if err := all.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	var roles []steward.Role
	all.Find(&roles)
	for _, r := range roles {
		fx.roles[r.Slug] = r
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	for _, name := range []string{"olivia", "adam", "eve", "carol"} {
		u := steward.User{Username: name, Name: strings.ToUpper(name[:1]) + name[1:], Password: string(hash)}
		if err := all.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		fx.users[name] = u
	}
	owner := fx.users["olivia"].ID
	fx.acme = steward.Tenant{Slug: "acme", Name: "Acme", Active: true, OwnerUserID: &owner}
	if err := all.Create(&fx.acme).Error; err != nil {
		t.Fatal(err)
	}
	fx.join(t, fx.acme.ID, "olivia", steward.RoleAdministrator)
	fx.join(t, fx.acme.ID, "adam", steward.RoleAdministrator)
	fx.join(t, fx.acme.ID, "eve", "enumerator")
	return fx
}

func (fx *workspaceFixture) join(t *testing.T, tenantID uint, user, role string) {
	t.Helper()
	u := fx.users[user]
	if err := fx.all.Create(&steward.TenantUser{TenantID: tenantID, UserID: u.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.all.Create(&steward.TenantUserRole{TenantID: tenantID, UserID: u.ID, RoleID: fx.roles[role].ID}).Error; err != nil {
		t.Fatal(err)
	}
}

// wsClient is a cookie-keeping client that does not follow redirects, so each
// hop of a flow can be checked.
type wsClient struct {
	t    *testing.T
	http *http.Client
	base string
}

func (fx *workspaceFixture) client(t *testing.T) *wsClient {
	jar, _ := cookiejar.New(nil)
	return &wsClient{t: t, base: fx.base, http: &http.Client{Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (fx *workspaceFixture) signIn(t *testing.T, user string) *wsClient {
	c := fx.client(t)
	_, page, _ := c.do(http.MethodGet, "/auth/login", nil, "")
	status, _, loc := c.do(http.MethodPost, "/auth/login", url.Values{"username": {user}, "password": {"secret"}}, csrfOf(t, page))
	if status != http.StatusFound {
		t.Fatalf("login %s: %d", user, status)
	}
	_ = loc
	return c
}

// do sends a request; a form with a CSRF token is posted as a browser would.
// It returns the status, body and Location header.
func (c *wsClient) do(method, path string, form url.Values, csrf string) (int, string, string) {
	c.t.Helper()
	var body io.Reader
	if form != nil {
		if csrf != "" {
			form.Set("_token", csrf)
		}
		body = strings.NewReader(form.Encode())
	}
	target := path
	if !strings.HasPrefix(path, "http") {
		target = c.base + path
	}
	req, _ := http.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if method != http.MethodGet && method != http.MethodPost {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header.Get("Location")
}

// json sends a form as the panel's own JavaScript does and returns the
// status and envelope body.
func (c *wsClient) json(method, path string, form url.Values, csrf string) (int, string) {
	c.t.Helper()
	form.Set("_token", csrf)
	req, _ := http.NewRequest(method, c.base+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (c *wsClient) page(path string) (int, string) {
	c.t.Helper()
	status, body, _ := c.do(http.MethodGet, path, nil, "")
	return status, body
}

func csrfOf(t *testing.T, page string) string {
	t.Helper()
	if m := formTokenRe.FindStringSubmatch(page); m != nil {
		return m[1]
	}
	if m := metaTokenRe2.FindStringSubmatch(page); m != nil {
		return m[1]
	}
	t.Fatal("no CSRF token on the page")
	return ""
}

func userForm(u steward.User, roles ...steward.Role) url.Values {
	f := url.Values{"Username": {u.Username}, "Name": {u.Name}}
	for _, r := range roles {
		f.Add("Roles", itoa(r.ID))
	}
	return f
}

func TestWorkspaceOwnerAndAdminRules(t *testing.T) {
	fx := newWorkspaceFixture(t, &steward.TenancyConfig{})
	admin, enum := fx.roles[steward.RoleAdministrator], fx.roles["enumerator"]
	olivia, adam, eve := fx.users["olivia"], fx.users["adam"], fx.users["eve"]

	ac := fx.signIn(t, "adam")
	_, page := ac.page("/auth/users")
	tok := csrfOf(t, page)

	if status, body, _ := ac.do(http.MethodDelete, "/auth/users/"+itoa(olivia.ID), nil, tok); status < 400 ||
		!strings.Contains(body, "owner cannot be removed") {
		t.Fatalf("adam removing the owner: %d %s", status, body)
	}
	if status, _ := ac.page("/auth/users/" + itoa(olivia.ID) + "/edit"); status != http.StatusForbidden {
		t.Fatalf("adam editing the owner's account: %d, want 403", status)
	}

	// The role picker offers workspace roles only, and a forged ID is refused.
	_, create := ac.page("/auth/users/create")
	if strings.Contains(create, ">Supervisor<") || !strings.Contains(create, ">Enumerator<") {
		t.Fatal("the role picker does not match TenancyConfig.Roles")
	}
	if status, body := ac.json(http.MethodPut, "/auth/users/"+itoa(eve.ID),
		userForm(eve, fx.roles["supervisor"]), tok); status < 400 || !strings.Contains(body, "cannot be assigned") {
		t.Fatalf("assigning a role outside the list: %d %s", status, body)
	}

	oc := fx.signIn(t, "olivia")
	_, page = oc.page("/auth/users")
	otok := csrfOf(t, page)
	if status, body := oc.json(http.MethodPut, "/auth/users/"+itoa(olivia.ID), userForm(olivia, enum), otok); status < 400 ||
		!strings.Contains(body, "must stay an administrator") {
		t.Fatalf("owner dropping the administrator role: %d %s", status, body)
	}

	// Transferring ownership: only to an administrator, and the old owner
	// loses the settings page.
	_, ws := oc.page("/auth/workspace")
	if !strings.Contains(ws, "Adam (adam)") || strings.Contains(ws, "Eve (eve)") {
		t.Fatal("transfer candidates are not the other administrators")
	}
	if status, _, _ := oc.do(http.MethodPost, "/auth/workspace",
		url.Values{"action": {"transfer"}, "owner": {itoa(eve.ID)}}, csrfOf(t, ws)); status != http.StatusUnprocessableEntity {
		t.Fatalf("transfer to an enumerator: %d, want 422", status)
	}
	if status, _, _ := oc.do(http.MethodPost, "/auth/workspace",
		url.Values{"action": {"transfer"}, "owner": {itoa(adam.ID)}}, csrfOf(t, ws)); status != http.StatusFound {
		t.Fatalf("transfer to adam: %d", status)
	}
	var tn steward.Tenant
	fx.all.First(&tn, fx.acme.ID)
	if !tn.OwnedBy(adam.ID) {
		t.Fatalf("owner after transfer: %v", tn.OwnerUserID)
	}
	if status, _ := oc.page("/auth/workspace"); status != http.StatusForbidden {
		t.Fatalf("former owner opening workspace settings: %d, want 403", status)
	}

	// Now olivia is an ordinary administrator and may step down while adam
	// remains; removing the last administrator is refused.
	if status, body := oc.json(http.MethodPut, "/auth/users/"+itoa(olivia.ID), userForm(olivia, enum), otok); status >= 400 {
		t.Fatalf("olivia stepping down: %d %s", status, body)
	}
	fx.all.Model(&steward.Tenant{}).Where("id = ?", fx.acme.ID).Update("owner_user_id", nil)
	acme2 := fx.signIn(t, "adam")
	_, page = acme2.page("/auth/users")
	if status, body := acme2.json(http.MethodPut, "/auth/users/"+itoa(adam.ID), userForm(adam, enum), csrfOf(t, page)); status < 400 ||
		!strings.Contains(body, "at least one administrator") {
		t.Fatalf("removing the last administrator: %d %s", status, body)
	}
	_ = admin
}

func TestWorkspaceSwitchNeedsNoPermission(t *testing.T) {
	fx := newWorkspaceFixture(t, &steward.TenancyConfig{})
	other := steward.Tenant{Slug: "globex", Name: "Globex", Active: true}
	fx.all.Create(&other)
	fx.join(t, other.ID, "eve", "enumerator")

	ec := fx.signIn(t, "eve")
	_, page := ec.page("/")
	status, _, _ := ec.do(http.MethodPost, "/auth/tenant", url.Values{"tenant": {itoa(other.ID)}}, csrfOf(t, page))
	if status != http.StatusFound {
		t.Fatalf("enumerator switching workspace: %d, want 302", status)
	}
}

var inviteLinkRe = regexp.MustCompile(`(https?://[^"<\s]+/admin/auth/invite/[0-9]+\.[A-Za-z0-9_-]+)`)

func (fx *workspaceFixture) invite(t *testing.T, c *wsClient, email string, role steward.Role) (int, string) {
	t.Helper()
	_, page := c.page("/auth/invitations/create")
	return c.json(http.MethodPost, "/auth/invitations", url.Values{"Email": {email}, "RoleID": {itoa(role.ID)}}, csrfOf(t, page))
}

func (fx *workspaceFixture) inviteLink(t *testing.T, c *wsClient, email string) string {
	t.Helper()
	var inv steward.TenantInvitation
	if err := fx.all.Where("email = ?", email).Order("id DESC").First(&inv).Error; err != nil {
		t.Fatal(err)
	}
	_, detail := c.page("/auth/invitations/" + itoa(inv.ID))
	m := inviteLinkRe.FindStringSubmatch(detail)
	if m == nil {
		t.Fatalf("no invitation link on the detail page")
	}
	return m[1]
}

func TestInvitationAcceptedThroughSSO(t *testing.T) {
	fx := newWorkspaceFixture(t, &steward.TenancyConfig{})
	oc := fx.signIn(t, "olivia")

	if status, body := fx.invite(t, oc, "Field.Worker@example.com", fx.roles["supervisor"]); status < 400 {
		t.Fatalf("inviting with a role outside the list: %d %s", status, body)
	}
	if status, body := fx.invite(t, oc, "Field.Worker@example.com", fx.roles["enumerator"]); status >= 400 {
		t.Fatalf("inviting: %d %s", status, body)
	}
	if status, body := fx.invite(t, oc, "field.worker@example.com", fx.roles["enumerator"]); status < 400 ||
		!strings.Contains(body, "pending invitation") {
		t.Fatalf("inviting the same email twice: %d %s", status, body)
	}
	link := fx.inviteLink(t, oc, "field.worker@example.com")

	// A stranger opens the link and signs in with an identity whose email is
	// not the invited one (Apple's private relay).
	fx.idp.codes["relay"] = steward.ExternalIdentity{Subject: "apple-1", Email: "x1@privaterelay.example", EmailVerified: true, Name: "Fieldy"}
	b := fx.client(t)
	if status, body, _ := b.do(http.MethodGet, link, nil, ""); status != http.StatusOK || !strings.Contains(body, "Join Acme") {
		t.Fatalf("opening the invitation: %d", status)
	}
	sso := &ssoFixture{base: fx.base, idp: fx.idp}
	state := sso.start(t, b.http)
	status, loc, body := sso.callback(t, b.http, state, "relay")
	if status != http.StatusFound || !strings.HasSuffix(strings.TrimSuffix(loc, "/"), "/admin") {
		t.Fatalf("callback: %d %s %s", status, loc, firstLine(body))
	}
	if status, home := b.page("/"); status != http.StatusOK || !strings.Contains(home, "You joined Acme") {
		t.Fatalf("landing after accepting: %d", status)
	}

	var u steward.User
	if err := fx.all.Where("username = ?", "x1").First(&u).Error; err != nil {
		t.Fatalf("no account created for the invitee: %v", err)
	}
	roles, _ := steward.UserTenantRoles(context.Background(), fx.db, fx.acme.ID, u.ID)
	if len(roles) != 1 || roles[0].Slug != "enumerator" {
		t.Fatalf("invitee's roles in acme: %v", roles)
	}
	if status, body, _ := fx.client(t).do(http.MethodGet, link, nil, ""); status != http.StatusNotFound ||
		!strings.Contains(body, "already been used") {
		t.Fatalf("reusing the link: %d", status)
	}
}

func TestInvitationAcceptedWithPasswordAndExpiry(t *testing.T) {
	fx := newWorkspaceFixture(t, &steward.TenancyConfig{})
	oc := fx.signIn(t, "olivia")
	if status, body := fx.invite(t, oc, "carol@example.com", fx.roles["enumerator"]); status >= 400 {
		t.Fatalf("inviting carol: %d %s", status, body)
	}
	link := fx.inviteLink(t, oc, "carol@example.com")

	cc := fx.client(t)
	if status, _, _ := cc.do(http.MethodGet, link, nil, ""); status != http.StatusOK {
		t.Fatalf("carol opening the link: %d", status)
	}
	_, page, _ := cc.do(http.MethodGet, "/auth/login", nil, "")
	if status, _, _ := cc.do(http.MethodPost, "/auth/login", url.Values{"username": {"carol"}, "password": {"secret"}}, csrfOf(t, page)); status != http.StatusFound {
		t.Fatalf("carol login: %d", status)
	}
	if status, home := cc.page("/"); status != http.StatusOK || !strings.Contains(home, "You joined Acme") {
		t.Fatalf("carol after login: %d", status)
	}

	// Renewing a link retires the old one; an expired link is refused.
	if status, body := fx.invite(t, oc, "late@example.com", fx.roles["enumerator"]); status >= 400 {
		t.Fatalf("inviting late: %d %s", status, body)
	}
	old := fx.inviteLink(t, oc, "late@example.com")
	var inv steward.TenantInvitation
	fx.all.Where("email = ?", "late@example.com").First(&inv)
	_, list := oc.page("/auth/invitations")
	if status, body := oc.json(http.MethodPost, "/auth/invitations/_action/renew", url.Values{"ids": {itoa(inv.ID)}}, csrfOf(t, list)); status >= 400 {
		t.Fatalf("renewing: %d %s", status, body)
	}
	if status, _, _ := fx.client(t).do(http.MethodGet, old, nil, ""); status != http.StatusNotFound {
		t.Fatalf("old link after renewal: %d, want 404", status)
	}
	fresh := fx.inviteLink(t, oc, "late@example.com")
	fx.all.Model(&steward.TenantInvitation{}).Where("id = ?", inv.ID).Update("expires_at", time.Now().Add(-time.Hour))
	if status, body, _ := fx.client(t).do(http.MethodGet, fresh, nil, ""); status != http.StatusNotFound || !strings.Contains(body, "expired") {
		t.Fatalf("expired link: %d", status)
	}
}

func TestCanAddMemberRefusesInvitations(t *testing.T) {
	fx := newWorkspaceFixture(t, &steward.TenancyConfig{
		CanAddMember: func(context.Context, uint) error { return errors.New("the Free plan allows 3 members") },
	})
	oc := fx.signIn(t, "olivia")
	if status, body := fx.invite(t, oc, "more@example.com", fx.roles["enumerator"]); status < 400 ||
		!strings.Contains(body, "Free plan allows 3 members") {
		t.Fatalf("invite over the limit: %d %s", status, body)
	}
	_, page := oc.page("/auth/users/create")
	f := url.Values{"Username": {"zed"}, "Name": {"Zed"}, "Password": {"secret1"}}
	if status, body := oc.json(http.MethodPost, "/auth/users", f, csrfOf(t, page)); status < 400 ||
		!strings.Contains(body, "Free plan allows 3 members") {
		t.Fatalf("creating a member over the limit: %d %s", status, body)
	}
}

func TestSignupCreatesOwnedWorkspace(t *testing.T) {
	var created []string
	fx := newWorkspaceFixture(t, &steward.TenancyConfig{Signup: &steward.SignupConfig{
		Created: func(_ context.Context, _ *gorm.DB, u *steward.User, tn *steward.Tenant) error {
			created = append(created, u.Username+"@"+tn.Slug)
			return nil
		},
	}})
	sso := &ssoFixture{base: fx.base, idp: fx.idp}
	fx.idp.codes["new"] = steward.ExternalIdentity{Subject: "g-9", Email: "rina@survey.example", EmailVerified: true, Name: "Rina"}
	fx.idp.codes["unverified"] = steward.ExternalIdentity{Subject: "g-10", Email: "x@example.com", Name: "X"}

	b := fx.client(t)
	if _, login := b.page("/auth/login"); !strings.Contains(login, "create your workspace") {
		t.Fatal("login page does not mention signup")
	}
	status, loc, _ := sso.callback(t, b.http, sso.start(t, b.http), "unverified")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("unverified identity: %d %s", status, loc)
	}

	status, loc, _ = sso.callback(t, b.http, sso.start(t, b.http), "new")
	if status != http.StatusFound || !strings.HasSuffix(loc, "/admin/auth/signup") {
		t.Fatalf("callback for a new identity: %d %s", status, loc)
	}
	_, form := b.page("/auth/signup")
	if !strings.Contains(form, "Rina&#39;s workspace") {
		t.Fatal("signup form does not suggest a workspace name")
	}
	status, _, loc = b.do(http.MethodPost, "/auth/signup", url.Values{"workspace": {"Nusantara Survey"}}, csrfOf(t, form))
	if status != http.StatusFound || !strings.HasSuffix(strings.TrimSuffix(loc, "/"), "/admin") {
		t.Fatalf("signup submit: %d %s", status, loc)
	}
	if status, home := b.page("/"); status != http.StatusOK || !strings.Contains(home, "Nusantara Survey is ready") {
		t.Fatalf("after signup: %d", status)
	}

	var tn steward.Tenant
	if err := fx.all.Where("slug = ?", "nusantara-survey").First(&tn).Error; err != nil {
		t.Fatal(err)
	}
	var u steward.User
	fx.all.Where("username = ?", "rina").First(&u)
	if !tn.OwnedBy(u.ID) || u.Email == nil || *u.Email != "rina@survey.example" {
		t.Fatalf("tenant owner %v, user %+v", tn.OwnerUserID, u)
	}
	roles, _ := steward.UserTenantRoles(context.Background(), fx.db, tn.ID, u.ID)
	if len(roles) != 1 || roles[0].Slug != steward.RoleAdministrator {
		t.Fatalf("owner's roles: %v", roles)
	}
	if len(created) != 1 || created[0] != "rina@nusantara-survey" {
		t.Fatalf("Signup.Created calls: %v", created)
	}
	if status, _ := b.page("/auth/workspace"); status != http.StatusOK {
		t.Fatalf("owner opening workspace settings: %d", status)
	}

	// Signing in again with the same identity reuses the account.
	again := fx.client(t)
	if status, loc, _ := sso.callback(t, again.http, sso.start(t, again.http), "new"); status != http.StatusFound || !strings.HasSuffix(strings.TrimSuffix(loc, "/"), "/admin") {
		t.Fatalf("second sign-in: %d %s", status, loc)
	}
}

func TestNoWorkspacePage(t *testing.T) {
	fx := newWorkspaceFixture(t, &steward.TenancyConfig{Signup: &steward.SignupConfig{}})
	cc := fx.signIn(t, "carol")
	status, page := cc.page("/")
	if status != http.StatusForbidden || !strings.Contains(page, "Create workspace") {
		t.Fatalf("carol without a workspace: %d", status)
	}
	status, _, _ = cc.do(http.MethodPost, "/auth/signup", url.Values{"workspace": {"Carol Co"}}, csrfOf(t, page))
	if status != http.StatusFound {
		t.Fatalf("carol creating a workspace: %d", status)
	}
	var tn steward.Tenant
	if err := fx.all.Where("slug = ?", "carol-co").First(&tn).Error; err != nil || !tn.OwnedBy(fx.users["carol"].ID) {
		t.Fatalf("carol's workspace: %+v %v", tn, err)
	}
	// A member of a workspace cannot create more this way.
	if status, _, _ := cc.do(http.MethodPost, "/auth/signup", url.Values{"workspace": {"Second"}}, csrfOf(t, page)); status != http.StatusFound {
		t.Fatalf("second workspace attempt: %d", status)
	}
	var n int64
	fx.all.Model(&steward.Tenant{}).Where("owner_user_id = ?", fx.users["carol"].ID).Count(&n)
	if n != 1 {
		t.Fatalf("carol owns %d workspaces, want 1", n)
	}
}

func TestOwnerAndInvitationMigrationRollsBack(t *testing.T) {
	db := testDB(t)
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("workspace-test-secret-key"), Tenancy: &steward.TenancyConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	runner := app.MigrationRunner(nil)
	if err := runner.Down(t.Context(), 1); err != nil {
		t.Fatalf("rolling back 0008: %v", err)
	}
	if db.Migrator().HasTable(&steward.TenantInvitation{}) || db.Migrator().HasColumn(&steward.Tenant{}, "OwnerUserID") {
		t.Fatal("0008 down left its table or column behind")
	}
	if _, err := runner.Up(t.Context()); err != nil {
		t.Fatalf("re-applying 0008: %v", err)
	}
	if !db.Migrator().HasTable(&steward.TenantInvitation{}) || !db.Migrator().HasColumn(&steward.Tenant{}, "OwnerUserID") {
		t.Fatal("0008 up did not restore its table and column")
	}
}

func TestInvitationsAndOwnershipCanBeTurnedOff(t *testing.T) {
	var created []*steward.Tenant
	fx := newWorkspaceFixture(t, &steward.TenancyConfig{
		DisableInvitations: true, DisableOwnership: true,
		Signup: &steward.SignupConfig{Created: func(_ context.Context, _ *gorm.DB, _ *steward.User, tn *steward.Tenant) error {
			created = append(created, tn)
			return nil
		}},
	})
	oc := fx.signIn(t, "olivia")
	for _, path := range []string{"/auth/invitations", "/auth/invite/1.abc", "/auth/workspace"} {
		if status, _ := oc.page(path); status != http.StatusNotFound {
			t.Fatalf("%s with the feature off: %d, want 404", path, status)
		}
	}
	if _, home := oc.page("/"); strings.Contains(home, "Workspace settings") || strings.Contains(home, ">Invitations<") {
		t.Fatal("the menus still offer a feature that is off")
	}

	// olivia's owner_user_id is ignored: another administrator may remove her.
	ac := fx.signIn(t, "adam")
	_, page := ac.page("/auth/users")
	if status, body, _ := ac.do(http.MethodDelete, "/auth/users/"+itoa(fx.users["olivia"].ID), nil, csrfOf(t, page)); status >= 400 {
		t.Fatalf("removing the recorded owner with ownership off: %d %s", status, body)
	}

	// Signup still works and records no owner.
	sso := &ssoFixture{base: fx.base, idp: fx.idp}
	fx.idp.codes["new"] = steward.ExternalIdentity{Subject: "g-1", Email: "new@example.com", EmailVerified: true, Name: "New"}
	b := fx.client(t)
	sso.callback(t, b.http, sso.start(t, b.http), "new")
	_, form := b.page("/auth/signup")
	if status, _, _ := b.do(http.MethodPost, "/auth/signup", url.Values{"workspace": {"Solo"}}, csrfOf(t, form)); status != http.StatusFound {
		t.Fatalf("signup: %d", status)
	}
	if len(created) != 1 || created[0].OwnerUserID != nil {
		t.Fatalf("signup with ownership off recorded an owner: %+v", created)
	}
}
