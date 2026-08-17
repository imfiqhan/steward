package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	steward "github.com/imfiqhan/steward"
)

// End-to-end coverage of the two-factor flow over HTTP: enrolling from the
// profile page, the login challenge, recovery codes, and the Require2FA gate.
// The unit tests in the root package cover the TOTP arithmetic; these check the
// wiring — that a password alone really does not get you in.

func new2FAServer(t *testing.T, require2FA bool) (*httptest.Server, *steward.Admin) {
	t.Helper()
	db := testDB(t)
	app, err := steward.New(steward.Config{
		// These exercise a prefixed mount; the default is the root.
		Prefix:     "/admin",
		DB:         db,
		SecretKey:  []byte("two-factor-flow-test-secret-key"),
		Require2FA: require2FA,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv, app
}

type tfaClient struct {
	t    *testing.T
	http *http.Client
	base string
}

func new2FAClient(t *testing.T, srv *httptest.Server) *tfaClient {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &tfaClient{t: t, http: &http.Client{Jar: jar}, base: srv.URL + "/admin"}
}

var formTokenRe = regexp.MustCompile(`name="_token" value="([^"]+)"`)
var metaTokenRe2 = regexp.MustCompile(`name="csrf-token" content="([^"]+)"`)

func (c *tfaClient) get(path string) (int, string) {
	c.t.Helper()
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// token finds a usable CSRF token, preferring a form field and falling back to
// the layout's meta tag.
func (c *tfaClient) token(page string) string {
	c.t.Helper()
	if m := formTokenRe.FindStringSubmatch(page); m != nil {
		return m[1]
	}
	if m := metaTokenRe2.FindStringSubmatch(page); m != nil {
		return m[1]
	}
	c.t.Fatal("no CSRF token on the page")
	return ""
}

func (c *tfaClient) post(path string, form url.Values, csrf string) (int, string) {
	c.t.Helper()
	form.Set("_token", csrf)
	resp, err := c.http.PostForm(c.base+path, form)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (c *tfaClient) login(username, password string) (int, string) {
	c.t.Helper()
	_, page := c.get("/auth/login")
	return c.post("/auth/login", url.Values{
		"username": {username}, "password": {password},
	}, c.token(page))
}

// secretRe pulls the base32 secret out of the enrolment page's manual-entry
// input, which is how the test learns what code to generate.
var secretRe = regexp.MustCompile(`id="tfa-secret"[^>]*value="([A-Z2-7]+)"`)

// codeRe matches a recovery code as rendered (xxxx-xxxx-xxxx-xxxx).
var codeRe = regexp.MustCompile(`>([A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4})<`)

// totpNow computes the current code for a base32 secret.
//
// This is an independent implementation of RFC 6238 rather than a call into the
// framework's, so the test verifies interoperability — a client and the server
// agreeing — instead of tautologically comparing Steward against itself.
func totpNow(t *testing.T, secret string) string {
	t.Helper()
	return totpStep(t, secret, 0)
}

// totpStep computes the code `offset` 30-second steps from now. Steward accepts
// one step either side of the current one, so offset 1 is a valid code that is
// distinct from the one just used — which is how these tests get a second usable
// code without sleeping for half a minute.
func totpStep(t *testing.T, secret string, offset int64) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).
		DecodeString(strings.ToUpper(secret))
	if err != nil {
		t.Fatalf("decoding secret %q: %v", secret, err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(time.Now().Unix()/30+offset))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	at := sum[len(sum)-1] & 0x0F
	value := binary.BigEndian.Uint32(sum[at:at+4]) & 0x7FFFFFFF
	return fmt.Sprintf("%06d", value%1_000_000)
}

// firstLine trims a page dump down to something readable in a failure message.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

func seedUser(t *testing.T, a *steward.Admin, username, password string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := steward.AdminUser{Username: username, Name: username, Password: string(hash)}
	if err := a.DB().Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	var role steward.Role
	if err := a.DB().Where("slug = ?", steward.RoleAdministrator).First(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB().Create(&steward.RoleUser{RoleID: role.ID, UserID: u.ID}).Error; err != nil {
		t.Fatal(err)
	}
}

// TestTwoFactorEnrolAndChallenge is the whole happy path: a user with no second
// factor enrols from their profile, then must present a code to sign in again.
func TestTwoFactorEnrolAndChallenge(t *testing.T) {
	srv, a := new2FAServer(t, false)
	seedUser(t, a, "editor", "editor-password")

	c := new2FAClient(t, srv)
	if code, body := c.login("editor", "editor-password"); code != http.StatusOK {
		t.Fatalf("login = %d: %s", code, body)
	}

	// Without enrolment the profile offers to set 2FA up.
	_, profile := c.get("/auth/profile")
	if !strings.Contains(profile, "Set up two-factor authentication") {
		t.Fatal("the profile page should offer enrolment")
	}

	// Start enrolment: the page shows a QR and the secret.
	code, page := c.post("/auth/profile/2fa/enable", url.Values{}, c.token(profile))
	if code != http.StatusOK {
		t.Fatalf("enable = %d: %s", code, page)
	}
	if !strings.Contains(page, "<svg") {
		t.Error("the enrolment page should render a QR code")
	}
	m := secretRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no secret on the enrolment page: %s", page)
	}
	secret := m[1]
	if len(secret) != 32 {
		t.Errorf("secret %q has length %d, want 32", secret, len(secret))
	}

	// A wrong code is refused and does not enable anything.
	code, rejected := c.post("/auth/profile/2fa/confirm",
		url.Values{"code": {"000000"}}, c.token(page))
	if code != http.StatusUnprocessableEntity {
		t.Errorf("confirm with a bad code = %d, want 422", code)
	}
	if !strings.Contains(rejected, "not valid") {
		t.Error("the rejection should say the code was wrong")
	}

	// The right code enables it and shows the recovery codes, once.
	code, confirmed := c.post("/auth/profile/2fa/confirm",
		url.Values{"code": {totpNow(t, secret)}}, c.token(rejected))
	if code != http.StatusOK {
		t.Fatalf("confirm = %d: %s", code, confirmed)
	}
	recovery := codeRe.FindAllStringSubmatch(confirmed, -1)
	if len(recovery) != 8 {
		t.Fatalf("got %d recovery codes, want 8", len(recovery))
	}
	// Revisiting the profile must not show them again.
	_, after := c.get("/auth/profile")
	if codeRe.MatchString(after) {
		t.Error("recovery codes must be shown only once")
	}
	if !strings.Contains(after, "recovery code") {
		t.Error("the profile should report how many recovery codes remain")
	}

	// Now sign in fresh: the password alone must not be enough.
	c2 := new2FAClient(t, srv)
	if code, body := c2.login("editor", "editor-password"); code != http.StatusOK {
		t.Fatalf("login = %d: %s", code, body)
	}
	// A parked session is redirected to the challenge, not to the login form:
	// it has nothing left to prove there.
	_, home := c2.get("/")
	if !strings.Contains(home, "Two-factor authentication") {
		t.Fatalf("a password-only session should be parked at the challenge: %s", firstLine(home))
	}
	// A resource listing is not reachable either — the session carries no user.
	_, listing := c2.get("/auth/users")
	if strings.Contains(listing, "Administrators") {
		t.Error("a parked session reached a resource listing")
	}

	// The code completes the login.
	_, challenge := c2.get("/auth/2fa")
	code, done := c2.post("/auth/2fa", url.Values{"code": {totpStep(t, secret, 1)}}, c2.token(challenge))
	if code != http.StatusOK {
		t.Fatalf("challenge = %d: %s", code, done)
	}
	_, home = c2.get("/")
	if strings.Contains(home, "Two-factor authentication") {
		t.Fatal("the challenge should have completed the login")
	}
	if code, _ := c2.get("/auth/users"); code != http.StatusOK {
		t.Errorf("GET /auth/users after 2FA = %d, want 200", code)
	}
}

// TestTwoFactorRecoveryCodeSignsInOnce checks the escape hatch and that it burns
// the code it used.
func TestTwoFactorRecoveryCodeSignsInOnce(t *testing.T) {
	srv, a := new2FAServer(t, false)
	seedUser(t, a, "editor", "editor-password")

	c := new2FAClient(t, srv)
	c.login("editor", "editor-password")
	_, profile := c.get("/auth/profile")
	_, page := c.post("/auth/profile/2fa/enable", url.Values{}, c.token(profile))
	secret := secretRe.FindStringSubmatch(page)[1]
	_, confirmed := c.post("/auth/profile/2fa/confirm",
		url.Values{"code": {totpNow(t, secret)}}, c.token(page))
	codes := codeRe.FindAllStringSubmatch(confirmed, -1)
	if len(codes) == 0 {
		t.Fatal("no recovery codes issued")
	}
	first := codes[0][1]

	// Sign in with the recovery code.
	c2 := new2FAClient(t, srv)
	c2.login("editor", "editor-password")
	_, challenge := c2.get("/auth/2fa")
	code, body := c2.post("/auth/2fa", url.Values{"recovery_code": {first}}, c2.token(challenge))
	if code != http.StatusOK {
		t.Fatalf("recovery sign-in = %d: %s", code, body)
	}
	if got, _ := c2.get("/auth/users"); got != http.StatusOK {
		t.Errorf("GET /auth/users after recovery = %d, want 200", got)
	}

	// The same code must not work twice.
	c3 := new2FAClient(t, srv)
	c3.login("editor", "editor-password")
	_, challenge3 := c3.get("/auth/2fa")
	code, body = c3.post("/auth/2fa", url.Values{"recovery_code": {first}}, c3.token(challenge3))
	if code != http.StatusUnprocessableEntity {
		t.Errorf("reusing a recovery code = %d, want 422", code)
	}
	if !strings.Contains(body, "not valid") {
		t.Error("a spent recovery code should be refused")
	}
}

// TestTwoFactorCodeCannotBeReplayed is the property that distinguishes this
// implementation from a naive one.
func TestTwoFactorCodeCannotBeReplayed(t *testing.T) {
	srv, a := new2FAServer(t, false)
	seedUser(t, a, "editor", "editor-password")

	c := new2FAClient(t, srv)
	c.login("editor", "editor-password")
	_, profile := c.get("/auth/profile")
	_, page := c.post("/auth/profile/2fa/enable", url.Values{}, c.token(profile))
	secret := secretRe.FindStringSubmatch(page)[1]
	c.post("/auth/profile/2fa/confirm", url.Values{"code": {totpNow(t, secret)}}, c.token(page))

	// Enrolment spends the step it confirmed with, so take the next one.
	reused := totpStep(t, secret, 1)

	// First use succeeds.
	c2 := new2FAClient(t, srv)
	c2.login("editor", "editor-password")
	_, ch := c2.get("/auth/2fa")
	if code, body := c2.post("/auth/2fa", url.Values{"code": {reused}}, c2.token(ch)); code != http.StatusOK {
		t.Fatalf("first use = %d: %s", code, body)
	}

	// The very same code, in the same 30-second window, must be refused.
	c3 := new2FAClient(t, srv)
	c3.login("editor", "editor-password")
	_, ch3 := c3.get("/auth/2fa")
	code, body := c3.post("/auth/2fa", url.Values{"code": {reused}}, c3.token(ch3))
	if code != http.StatusUnprocessableEntity {
		t.Errorf("replaying a code = %d, want 422 — the step should be spent", code)
	}
	if !strings.Contains(body, "not valid") {
		t.Error("a replayed code should be refused")
	}
}

// TestRequire2FAForcesEnrolment covers the panel-wide switch: an unenrolled
// account cannot browse until it enrols.
func TestRequire2FAForcesEnrolment(t *testing.T) {
	srv, a := new2FAServer(t, true)
	seedUser(t, a, "editor", "editor-password")

	c := new2FAClient(t, srv)
	if code, body := c.login("editor", "editor-password"); code != http.StatusOK {
		t.Fatalf("login = %d: %s", code, body)
	}
	// Login parks at the challenge, which redirects an unenrolled account to
	// enrolment rather than to a prompt it could not answer.
	_, page := c.get("/auth/2fa")
	if !strings.Contains(page, "Set up two-factor authentication") {
		t.Fatalf("Require2FA should force enrolment during login: %s", page)
	}
	m := secretRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no secret on the forced-enrolment page")
	}

	// Confirming completes the login and shows the recovery codes.
	code, done := c.post("/auth/2fa", url.Values{"code": {totpNow(t, m[1])}}, c.token(page))
	if code != http.StatusOK {
		t.Fatalf("forced enrolment = %d: %s", code, done)
	}
	if len(codeRe.FindAllStringSubmatch(done, -1)) != 8 {
		t.Error("forced enrolment should issue recovery codes")
	}
	if got, _ := c.get("/auth/users"); got != http.StatusOK {
		t.Errorf("GET /auth/users after enrolling = %d, want 200", got)
	}
}

// TestTwoFactorDisableNeedsPassword checks that a borrowed session cannot strip
// the second factor.
func TestTwoFactorDisableNeedsPassword(t *testing.T) {
	srv, a := new2FAServer(t, false)
	seedUser(t, a, "editor", "editor-password")

	c := new2FAClient(t, srv)
	c.login("editor", "editor-password")
	_, profile := c.get("/auth/profile")
	_, page := c.post("/auth/profile/2fa/enable", url.Values{}, c.token(profile))
	secret := secretRe.FindStringSubmatch(page)[1]
	_, confirmed := c.post("/auth/profile/2fa/confirm",
		url.Values{"code": {totpNow(t, secret)}}, c.token(page))

	// The control has to look and behave like a destructive action: Basecoat keys
	// button variants off data-variant, so a class like "btn-destructive" renders
	// as unstyled text — which is how this shipped and why it was unrecognisable.
	_, page2 := c.get("/auth/profile")
	if !strings.Contains(page2, `class="btn justify-self-start" data-variant="destructive"`) {
		t.Error("the disable control should be a destructive button, not bare text")
	}
	for _, cls := range []string{"btn-destructive", "btn-outline", "btn-ghost", "btn-sm"} {
		if strings.Contains(page2, cls) {
			t.Errorf("%q is not a Basecoat class and renders unstyled", cls)
		}
	}
	// And it must ask before removing a second factor.
	if !strings.Contains(page2, "data-steward-confirm-submit") {
		t.Error("disabling 2FA should require confirmation")
	}
	if !strings.Contains(page2, `data-confirm-danger="1"`) {
		t.Error("the confirmation should be styled as destructive")
	}
	if !strings.Contains(page2, "data-confirm-title=") {
		t.Error("the confirmation needs a title")
	}

	// A wrong password leaves it on.
	code, body := c.post("/auth/profile/2fa/disable",
		url.Values{"password": {"wrong"}}, c.token(confirmed))
	if code != http.StatusUnprocessableEntity {
		t.Errorf("disable with a wrong password = %d, want 422", code)
	}
	var user steward.AdminUser
	if err := a.DB().Where("username = ?", "editor").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if !user.TwoFactorEnabled() {
		t.Fatal("2FA was turned off without the correct password")
	}

	// The right password turns it off.
	if code, out := c.post("/auth/profile/2fa/disable",
		url.Values{"password": {"editor-password"}}, c.token(body)); code != http.StatusOK {
		t.Fatalf("disable = %d: %s", code, out)
	}
	if err := a.DB().Where("username = ?", "editor").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.TwoFactorEnabled() {
		t.Error("2FA should be off after a correct password")
	}
	// And the next sign-in needs no code.
	c2 := new2FAClient(t, srv)
	c2.login("editor", "editor-password")
	if got, _ := c2.get("/auth/users"); got != http.StatusOK {
		t.Errorf("GET /auth/users with 2FA off = %d, want 200", got)
	}
}
