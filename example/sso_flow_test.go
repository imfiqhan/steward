package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// fakeIDP is an AuthProvider whose "identity provider" is a map from
// authorization code to identity. AuthURL points at a host the client never
// contacts; the test plays the IdP by calling the callback itself.
type fakeIDP struct {
	id    string
	codes map[string]steward.ExternalIdentity
}

func (p *fakeIDP) ID() string    { return p.id }
func (p *fakeIDP) Label() string { return "Continue with " + p.id }
func (p *fakeIDP) Icon() string  { return "google" }

func (p *fakeIDP) AuthURL(_ context.Context, req *steward.SSORequest) (string, error) {
	req.Values["nonce"] = "n-" + req.State[:6]
	q := url.Values{"state": {req.State}, "redirect_uri": {req.CallbackURL}}
	return "https://idp.invalid/authorize?" + q.Encode(), nil
}

func (p *fakeIDP) Exchange(_ context.Context, req *steward.SSORequest, r *http.Request) (*steward.ExternalIdentity, error) {
	if req.Values["nonce"] != "n-"+req.State[:6] {
		return nil, errors.New("nonce lost between redirect and callback")
	}
	ident, ok := p.codes[r.FormValue("code")]
	if !ok {
		return nil, errors.New("unknown code")
	}
	return &ident, nil
}

type ssoFixture struct {
	db   *gorm.DB
	app  *steward.Panel
	base string
	idp  *fakeIDP
}

func newSSOFixture(t *testing.T, mutate func(*steward.Config)) *ssoFixture {
	t.Helper()
	db := testDB(t)
	idp := &fakeIDP{id: "fake", codes: map[string]steward.ExternalIdentity{}}
	cfg := steward.Config{
		Prefix: "/admin", DB: db, SecretKey: []byte("sso-flow-test-secret-key"),
		AuthProviders: []steward.AuthProvider{idp},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	app, err := steward.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	return &ssoFixture{db: db, app: app, base: serve(t, app).URL + "/admin", idp: idp}
}

func (fx *ssoFixture) user(t *testing.T, username, email string) steward.User {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	u := steward.User{Username: username, Name: username, Password: string(hash)}
	if email != "" {
		u.Email = &email
	}
	if err := fx.db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

// browser returns a client that stops at the first redirect, so each hop of
// the flow can be inspected.
func (fx *ssoFixture) browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// start begins a sign-in and returns the state the IdP would echo back.
func (fx *ssoFixture) start(t *testing.T, c *http.Client) string {
	t.Helper()
	resp, err := c.Get(fx.base + "/auth/sso/" + fx.idp.id)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("start: %d", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Host != "idp.invalid" {
		t.Fatalf("start redirected to %s", loc)
	}
	if got := loc.Query().Get("redirect_uri"); !strings.HasSuffix(got, "/admin/auth/sso/fake/callback") {
		t.Fatalf("callback URL %q", got)
	}
	return loc.Query().Get("state")
}

func (fx *ssoFixture) callback(t *testing.T, c *http.Client, state, code string) (int, string, string) {
	t.Helper()
	q := url.Values{"state": {state}, "code": {code}}
	resp, err := c.Get(fx.base + "/auth/sso/" + fx.idp.id + "/callback?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := readAll(resp)
	return resp.StatusCode, resp.Header.Get("Location"), body
}

func readAll(resp *http.Response) string {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String()
		}
	}
}

func dashboardStatus(t *testing.T, c *http.Client, base string) int {
	t.Helper()
	resp, err := c.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestSSOIsOffWithoutProviders(t *testing.T) {
	fx := newSSOFixture(t, func(c *steward.Config) { c.AuthProviders = nil })
	c := fx.browser()
	resp, err := c.Get(fx.base + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(resp)
	_ = resp.Body.Close()
	if strings.Contains(body, "steward-sso-providers") || !strings.Contains(body, `name="password"`) {
		t.Fatal("login page without providers should show only the password form")
	}
	resp, err = c.Get(fx.base + "/auth/sso/fake")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || !strings.HasSuffix(loc, "/auth/login") {
		t.Fatalf("unconfigured SSO route: %d → %s, want the login redirect", resp.StatusCode, loc)
	}
}

func TestSSOLinkedIdentitySignsIn(t *testing.T) {
	fx := newSSOFixture(t, nil)
	u := fx.user(t, "erin", "")
	fx.db.Create(&steward.UserIdentity{UserID: u.ID, Provider: "fake", Subject: "sub-erin"})
	fx.idp.codes["good"] = steward.ExternalIdentity{Subject: "sub-erin", Email: "erin@example.com"}

	c := fx.browser()
	resp, _ := c.Get(fx.base + "/auth/login")
	body := readAll(resp)
	_ = resp.Body.Close()
	if !strings.Contains(body, `href="/admin/auth/sso/fake"`) {
		t.Fatal("login page is missing the provider button")
	}

	state := fx.start(t, c)
	status, loc, body := fx.callback(t, c, state, "good")
	if status != http.StatusFound || loc != "/admin" {
		t.Fatalf("callback: %d → %q %s", status, loc, firstLine(body))
	}
	if got := dashboardStatus(t, c, fx.base); got != http.StatusOK {
		t.Fatalf("dashboard after SSO: %d", got)
	}

	// The state is single-use: the cookie is cleared by the callback.
	status, _, _ = fx.callback(t, c, state, "good")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("replayed callback: %d, want 422", status)
	}
}

func TestSSORefusesForgedState(t *testing.T) {
	fx := newSSOFixture(t, nil)
	u := fx.user(t, "erin", "")
	fx.db.Create(&steward.UserIdentity{UserID: u.ID, Provider: "fake", Subject: "sub-erin"})
	fx.idp.codes["good"] = steward.ExternalIdentity{Subject: "sub-erin"}

	c := fx.browser()
	fx.start(t, c)
	if status, _, _ := fx.callback(t, c, "forged", "good"); status != http.StatusUnprocessableEntity {
		t.Fatalf("forged state: %d", status)
	}

	// A callback with no sign-in started in this browser.
	if status, _, _ := fx.callback(t, fx.browser(), "anything", "good"); status != http.StatusUnprocessableEntity {
		t.Fatalf("callback without a started sign-in: %d", status)
	}
	if got := dashboardStatus(t, c, fx.base); got != http.StatusFound {
		t.Fatalf("still signed out expected, dashboard gave %d", got)
	}
}

func TestSSOUnlinkedIdentity(t *testing.T) {
	fx := newSSOFixture(t, nil)
	fx.user(t, "frank", "frank@example.com")
	fx.idp.codes["verified"] = steward.ExternalIdentity{Subject: "sub-frank", Email: "Frank@example.com", EmailVerified: true}

	c := fx.browser()
	status, _, body := fx.callback(t, c, fx.start(t, c), "verified")
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "No account is linked") {
		t.Fatalf("unlinked identity without email linking: %d %s", status, firstLine(body))
	}

	fx = newSSOFixture(t, func(c *steward.Config) { c.LinkByVerifiedEmail = true })
	frank := fx.user(t, "frank", "frank@example.com")
	fx.idp.codes["verified"] = steward.ExternalIdentity{Subject: "sub-frank", Email: "Frank@example.com", EmailVerified: true}
	fx.idp.codes["unverified"] = steward.ExternalIdentity{Subject: "sub-other", Email: "frank@example.com"}

	c = fx.browser()
	if status, _, _ := fx.callback(t, c, fx.start(t, c), "unverified"); status != http.StatusUnprocessableEntity {
		t.Fatalf("unverified email linked: %d", status)
	}
	c = fx.browser()
	if status, loc, _ := fx.callback(t, c, fx.start(t, c), "verified"); status != http.StatusFound || loc != "/admin" {
		t.Fatalf("verified email: %d → %s", status, loc)
	}
	var link steward.UserIdentity
	if err := fx.db.Where("provider = ? AND subject = ?", "fake", "sub-frank").First(&link).Error; err != nil || link.UserID != frank.ID {
		t.Fatalf("identity not linked to frank: %+v %v", link, err)
	}
}

func TestSSOHonoursTwoFactorAndLoginCheck(t *testing.T) {
	fx := newSSOFixture(t, func(c *steward.Config) {
		c.LoginCheck = func(_ context.Context, u *steward.User) error {
			if u.Username == "suspended" {
				return errors.New("This account is suspended.")
			}
			return nil
		}
	})
	enrolled := fx.user(t, "grace", "")
	now := time.Now()
	fx.db.Model(&enrolled).Updates(map[string]any{"two_factor_secret": "JBSWY3DPEHPK3PXP", "two_factor_confirmed_at": now})
	fx.db.Create(&steward.UserIdentity{UserID: enrolled.ID, Provider: "fake", Subject: "sub-grace"})
	suspended := fx.user(t, "suspended", "")
	fx.db.Create(&steward.UserIdentity{UserID: suspended.ID, Provider: "fake", Subject: "sub-susp"})
	fx.idp.codes["grace"] = steward.ExternalIdentity{Subject: "sub-grace"}
	fx.idp.codes["susp"] = steward.ExternalIdentity{Subject: "sub-susp"}

	c := fx.browser()
	if status, loc, _ := fx.callback(t, c, fx.start(t, c), "grace"); status != http.StatusFound || loc != "/admin/auth/2fa" {
		t.Fatalf("enrolled user via SSO: %d → %s, want the 2FA challenge", status, loc)
	}
	if got := dashboardStatus(t, c, fx.base); got == http.StatusOK {
		t.Fatal("SSO skipped the second factor")
	}

	c = fx.browser()
	status, _, body := fx.callback(t, c, fx.start(t, c), "susp")
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "suspended") {
		t.Fatalf("LoginCheck refusal: %d %s", status, firstLine(body))
	}
}

func TestSSOPostCallbackNeedsNoCSRF(t *testing.T) {
	fx := newSSOFixture(t, nil)
	u := fx.user(t, "heidi", "")
	fx.db.Create(&steward.UserIdentity{UserID: u.ID, Provider: "fake", Subject: "sub-heidi"})
	fx.idp.codes["good"] = steward.ExternalIdentity{Subject: "sub-heidi"}

	c := fx.browser()
	state := fx.start(t, c)
	resp, err := c.PostForm(fx.base+"/auth/sso/fake/callback", url.Values{"state": {state}, "code": {"good"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/admin" {
		t.Fatalf("form_post callback: %d → %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestPasswordLoginCanBeDisabled(t *testing.T) {
	fx := newSSOFixture(t, func(c *steward.Config) { c.DisablePasswordLogin = true })
	c := fx.browser()
	resp, _ := c.Get(fx.base + "/auth/login")
	body := readAll(resp)
	_ = resp.Body.Close()
	if strings.Contains(body, `name="password"`) || !strings.Contains(body, "steward-sso-providers") {
		t.Fatal("login page should offer only the providers")
	}
	resp, err := c.PostForm(fx.base+"/auth/login", url.Values{
		"username": {"admin"}, "password": {"admin"}, "_token": {csrfFrom(t, body)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("password POST with login disabled: %d", resp.StatusCode)
	}
}

func TestDuplicateProviderIDsFailVerify(t *testing.T) {
	db := testDB(t)
	a, b := &fakeIDP{id: "dup"}, &fakeIDP{id: "dup"}
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("sso-flow-test-secret-key"),
		AuthProviders: []steward.AuthProvider{a, b}})
	if err != nil {
		t.Fatal(err)
	}
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err == nil || !strings.Contains(err.Error(), "used twice") {
		t.Fatalf("Verify: %v", err)
	}
}
