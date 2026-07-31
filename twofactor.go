package steward

// Time-based one-time-password (TOTP) two-factor authentication, RFC 6238.
//
// Enrolment is opt-in per account from the profile page and can be made
// mandatory panel-wide with Config.Require2FA. The shared secret and the
// recovery-code digests live on the user row; nothing else is stored, so there
// is no second credential store to operate.
//
// The design decisions worth knowing:
//
//   - A password that is correct but awaiting a second factor grants *no*
//     access. The half-authenticated state rides in the session cookie as
//     Pending2FA, a different field from UID, so every existing authorization
//     check keeps working unchanged — a pending session simply has no user.
//   - An accepted code's time step is recorded, and a step is never accepted
//     twice. Without that, a code shoulder-surfed or replayed from a proxy log
//     stays usable for the rest of its window.
//   - Recovery codes are single-use and stored as SHA-256 digests, matching how
//     bearer tokens are stored and for the same reason: they are high-entropy
//     random values, so stretching buys nothing.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/imfiqhan/steward/internal/qr"
)

// totpPeriod is the code lifetime in seconds; totpDigits its length. Both are
// the values every authenticator app defaults to, so the enrolment URI can
// omit them.
const (
	totpPeriod = 30
	totpDigits = 6

	// totpSkew is how many steps either side of the current one are accepted,
	// covering ordinary clock drift between the phone and the server.
	totpSkew = 1

	// secretBytes is the shared secret's length. RFC 4226 requires at least
	// 128 bits and recommends 160 — which is also exactly 32 base32 characters,
	// so the secret displays without padding.
	secretBytes = 20

	// recoveryCodeCount is how many single-use codes an enrolment mints.
	recoveryCodeCount = 8

	// recoveryCodeBytes gives 80 bits per code, formatted as four groups of
	// four base32 characters.
	recoveryCodeBytes = 10

	// pending2FAMaxAge bounds how long a password-verified session may wait at
	// the challenge before it must start over.
	pending2FAMaxAge = 10 * time.Minute

	// twoFactorRateLimit caps challenge attempts per user per window, so a
	// stolen password cannot be paired with a brute-forced six-digit code.
	twoFactorRateLimit  = 6
	twoFactorRateWindow = time.Minute
)

// base32Secret encodes without padding and in upper case, the form
// authenticator apps expect when a secret is typed by hand.
var base32Secret = base32.StdEncoding.WithPadding(base32.NoPadding)

// TwoFactorEnabled reports whether the account has completed enrolment.
func (u *AdminUser) TwoFactorEnabled() bool {
	return u.TwoFactorSecret != "" && u.TwoFactorConfirmedAt != nil
}

// newTOTPSecret mints a fresh shared secret in base32.
func newTOTPSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32Secret.EncodeToString(b), nil
}

// totpCode computes the code for one time step.
func totpCode(secret string, step int64) (string, error) {
	key, err := base32Secret.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("steward: bad TOTP secret: %w", err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))

	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)

	// RFC 4226 dynamic truncation: the low nibble of the last byte selects a
	// four-byte window, whose top bit is cleared to keep the value positive.
	offset := sum[len(sum)-1] & 0x0F
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7FFFFFFF

	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, value%mod), nil
}

// verifyTOTP checks code against the secret across the accepted skew window
// and returns the step that matched. A step at or below notBefore is refused,
// which is what makes a code single-use.
func verifyTOTP(secret, code string, now time.Time, notBefore int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	current := now.Unix() / totpPeriod
	for delta := int64(-totpSkew); delta <= totpSkew; delta++ {
		step := current + delta
		if step <= notBefore {
			continue
		}
		want, err := totpCode(secret, step)
		if err != nil {
			return 0, false
		}
		// Constant-time compare so a timing signal cannot leak the digits.
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// otpauthURI builds the enrolment URI an authenticator app reads from the QR
// code. Digits, period, and algorithm are left implicit at their defaults so
// the URI stays short enough for a small, dense-free QR symbol.
func (a *Admin) otpauthURI(username, secret string) string {
	issuer := a.cfg.Brand
	label := url.PathEscape(issuer + ":" + username)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// ---- recovery codes ---------------------------------------------------------

// newRecoveryCodes returns the plaintext codes to show the user once, and the
// digest list to store.
func newRecoveryCodes() (plain []string, stored string, err error) {
	digests := make([]string, 0, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		b := make([]byte, recoveryCodeBytes)
		if _, err := rand.Read(b); err != nil {
			return nil, "", err
		}
		raw := base32Secret.EncodeToString(b) // 16 characters
		code := raw[0:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:16]
		plain = append(plain, code)
		digests = append(digests, recoveryHash(code))
	}
	return plain, strings.Join(digests, "\n"), nil
}

// recoveryHash normalizes a code and digests it. Normalizing means a user may
// type it lower case, with or without the separators.
func recoveryHash(code string) string {
	norm := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// consumeRecoveryCode spends a code if it matches, returning the remaining
// digest list. Every stored digest is compared in constant time so neither the
// match position nor the number of unused codes leaks through timing.
func consumeRecoveryCode(stored, code string) (remaining string, ok bool) {
	if strings.TrimSpace(code) == "" {
		return stored, false
	}
	want := recoveryHash(code)
	var keep []string
	for _, h := range strings.Split(stored, "\n") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(h), []byte(want)) == 1 && !ok {
			ok = true
			continue // spent
		}
		keep = append(keep, h)
	}
	return strings.Join(keep, "\n"), ok
}

// countRecoveryCodes reports how many unused codes remain.
func countRecoveryCodes(stored string) int {
	n := 0
	for _, h := range strings.Split(stored, "\n") {
		if strings.TrimSpace(h) != "" {
			n++
		}
	}
	return n
}

// ---- login challenge --------------------------------------------------------

// twoFactorRequired reports whether the user must clear a second factor before
// the session becomes authenticated: either they enrolled, or the panel
// mandates enrolment for everyone.
func (a *Admin) twoFactorRequired(u *AdminUser) bool {
	return u.TwoFactorEnabled() || a.cfg.Require2FA
}

// beginTwoFactor parks a password-verified session at the challenge.
func (a *Admin) beginTwoFactor(c *Context, uid uint) error {
	c.sess.UID = 0
	c.sess.Pending2FA = uid
	c.sess.PendingAt = time.Now().Unix()
	a.saveSession(c)
	return nil
}

// pendingTwoFactorUser resolves the parked account, or nil when there is none
// or the wait has expired.
func (a *Admin) pendingTwoFactorUser(c *Context) *AdminUser {
	if c.sess == nil || c.sess.Pending2FA == 0 {
		return nil
	}
	if time.Since(time.Unix(c.sess.PendingAt, 0)) > pending2FAMaxAge {
		return nil
	}
	var u AdminUser
	if err := a.db.WithContext(c.Ctx()).Preload("Roles").First(&u, c.sess.Pending2FA).Error; err != nil {
		return nil
	}
	return &u
}

// clearPendingTwoFactor drops the parked state.
func (a *Admin) clearPendingTwoFactor(c *Context) {
	c.sess.Pending2FA = 0
	c.sess.PendingAt = 0
}

// twoFactorChallengePage renders the code prompt. A user who lands here
// without a parked session goes back to the login form.
func (a *Admin) twoFactorChallengePage(c *Context) error {
	user := a.pendingTwoFactorUser(c)
	if user == nil {
		return c.Redirect(a.url("auth/login"))
	}
	// A mandated-but-unenrolled account is sent to enrolment, not a challenge
	// it could never answer.
	if !user.TwoFactorEnabled() {
		return a.twoFactorEnrolPage(c, user, "")
	}
	return a.renderStandalone(c, "auth/twofactor.html", map[string]any{
		"Username":    user.Username,
		"HasRecovery": countRecoveryCodes(user.TwoFactorRecovery) > 0,
	})
}

// twoFactorChallengeSubmit verifies a TOTP code or a recovery code and, on
// success, completes the login.
func (a *Admin) twoFactorChallengeSubmit(c *Context) error {
	if err := c.R.ParseForm(); err != nil {
		return err
	}
	user := a.pendingTwoFactorUser(c)
	if user == nil {
		return c.Redirect(a.url("auth/login"))
	}
	if !user.TwoFactorEnabled() {
		return a.twoFactorEnrolSubmit(c, user)
	}

	fail := func(msg string) error {
		c.W.WriteHeader(http.StatusUnprocessableEntity)
		return a.renderStandalone(c, "auth/twofactor.html", map[string]any{
			"Username":    user.Username,
			"HasRecovery": countRecoveryCodes(user.TwoFactorRecovery) > 0,
			"Error":       msg,
		})
	}

	// Throttle before any comparison: a six-digit code is guessable if
	// attempts are unbounded.
	if !a.allowTwoFactorAttempt("2fa:" + fmt.Sprint(user.ID)) {
		return fail("Too many attempts — wait a minute and try again.")
	}

	code := strings.TrimSpace(c.R.PostFormValue("code"))
	recovery := strings.TrimSpace(c.R.PostFormValue("recovery_code"))

	switch {
	case recovery != "":
		remaining, ok := consumeRecoveryCode(user.TwoFactorRecovery, recovery)
		if !ok {
			return fail("That recovery code is not valid.")
		}
		if err := a.db.WithContext(c.Ctx()).Model(user).
			Update("two_factor_recovery", remaining).Error; err != nil {
			return err
		}
		left := countRecoveryCodes(remaining)
		a.clearPendingTwoFactor(c)
		if err := c.login(user.ID); err != nil {
			return err
		}
		a.log.Info("steward: login via recovery code", "user", user.Username, "remaining", left)
		if left == 0 {
			c.Flash("warning", "That was your last recovery code — generate a new set from your profile.")
		} else {
			c.Flash("warning", fmt.Sprintf("Signed in with a recovery code. %d left.", left))
		}
		return c.Redirect(a.url("/"))

	case code != "":
		step, ok := verifyTOTP(user.TwoFactorSecret, code, time.Now(), user.TwoFactorLastStep)
		if !ok {
			return fail("That code is not valid. Codes change every 30 seconds.")
		}
		if err := a.db.WithContext(c.Ctx()).Model(user).
			Update("two_factor_last_step", step).Error; err != nil {
			return err
		}
		a.clearPendingTwoFactor(c)
		if err := c.login(user.ID); err != nil {
			return err
		}
		a.log.Info("steward: login", "user", user.Username, "2fa", true)
		c.Flash("success", "Welcome back, "+displayName(user)+".")
		return c.Redirect(a.url("/"))

	default:
		return fail("Enter the six-digit code from your authenticator app.")
	}
}

// ---- enrolment --------------------------------------------------------------

// twoFactorSetup is the data an enrolment view needs.
type twoFactorSetup struct {
	Secret string
	URI    string
	QR     template.HTML
}

// prepareEnrolment issues (or reuses) an unconfirmed secret and renders its QR
// code. The secret is persisted before confirmation so the QR the user scanned
// still matches when they submit the code; TwoFactorConfirmedAt staying nil is
// what keeps 2FA switched off until then.
func (a *Admin) prepareEnrolment(ctx context.Context, user *AdminUser) (*twoFactorSetup, error) {
	if user.TwoFactorSecret == "" || user.TwoFactorConfirmedAt != nil {
		secret, err := newTOTPSecret()
		if err != nil {
			return nil, err
		}
		if err := a.db.WithContext(ctx).Model(user).Updates(map[string]any{
			"two_factor_secret":       secret,
			"two_factor_confirmed_at": nil,
		}).Error; err != nil {
			return nil, err
		}
		user.TwoFactorSecret = secret
		user.TwoFactorConfirmedAt = nil
	}
	uri := a.otpauthURI(user.Username, user.TwoFactorSecret)
	setup := &twoFactorSetup{Secret: user.TwoFactorSecret, URI: uri}
	if code, err := qr.Encode(uri); err == nil {
		setup.QR = template.HTML(code.SVG(4)) //nolint:gosec // generated markup, no user data interpolated
	} else {
		// A brand long enough to overflow the symbol must not block enrolment:
		// the secret is still shown for manual entry.
		a.log.Warn("steward: 2FA QR code", "err", err)
	}
	return setup, nil
}

// twoFactorEnrolPage renders forced enrolment during login (Require2FA with an
// account that has not enrolled yet).
func (a *Admin) twoFactorEnrolPage(c *Context, user *AdminUser, errMsg string) error {
	setup, err := a.prepareEnrolment(c.Ctx(), user)
	if err != nil {
		return err
	}
	return a.renderStandalone(c, "auth/twofactor_enrol.html", map[string]any{
		"Username": user.Username,
		"Setup":    setup,
		"Error":    errMsg,
	})
}

// twoFactorEnrolSubmit confirms a forced enrolment and completes the login.
func (a *Admin) twoFactorEnrolSubmit(c *Context, user *AdminUser) error {
	if !a.allowTwoFactorAttempt("2fa-enrol:" + fmt.Sprint(user.ID)) {
		return a.twoFactorEnrolPage(c, user, "Too many attempts — wait a minute and try again.")
	}
	code := strings.TrimSpace(c.R.PostFormValue("code"))
	step, ok := verifyTOTP(user.TwoFactorSecret, code, time.Now(), 0)
	if !ok {
		c.W.WriteHeader(http.StatusUnprocessableEntity)
		return a.twoFactorEnrolPage(c, user, "That code is not valid. Codes change every 30 seconds.")
	}
	plain, stored, err := newRecoveryCodes()
	if err != nil {
		return err
	}
	now := time.Now()
	if err := a.db.WithContext(c.Ctx()).Model(user).Updates(map[string]any{
		"two_factor_confirmed_at": now,
		"two_factor_recovery":     stored,
		"two_factor_last_step":    step,
	}).Error; err != nil {
		return err
	}
	a.clearPendingTwoFactor(c)
	if err := c.login(user.ID); err != nil {
		return err
	}
	a.log.Info("steward: 2FA enrolled", "user", user.Username)
	// The codes are shown once, on the page that follows the redirect.
	return a.renderStandalone(c, "auth/twofactor_codes.html", map[string]any{
		"Codes":    plain,
		"Continue": a.url("/"),
	})
}

// ---- profile management -----------------------------------------------------

// twoFactorVM is the profile page's two-factor section.
type twoFactorVM struct {
	Enabled       bool
	Required      bool
	Setup         *twoFactorSetup // non-nil while enrolling
	RecoveryLeft  int
	RecoveryCodes []string // shown once, immediately after enrolling
	Error         string
}

// twoFactorProfileVM builds the section for a normal profile render.
func (a *Admin) twoFactorProfileVM(c *Context) *twoFactorVM {
	return &twoFactorVM{
		Enabled:      c.User.TwoFactorEnabled(),
		Required:     a.cfg.Require2FA,
		RecoveryLeft: countRecoveryCodes(c.User.TwoFactorRecovery),
	}
}

// twoFactorEnableStart shows the QR code and asks for a confirming code.
func (a *Admin) twoFactorEnableStart(c *Context) error {
	if c.User.TwoFactorEnabled() {
		return c.Redirect(a.url("auth/profile"))
	}
	setup, err := a.prepareEnrolment(c.Ctx(), c.User)
	if err != nil {
		return err
	}
	vm := a.twoFactorProfileVM(c)
	vm.Setup = setup
	return a.renderProfile(c, vm, nil)
}

// twoFactorConfirm completes enrolment from the profile page.
func (a *Admin) twoFactorConfirm(c *Context) error {
	if err := c.R.ParseForm(); err != nil {
		return err
	}
	if c.User.TwoFactorEnabled() {
		return c.Redirect(a.url("auth/profile"))
	}
	if !a.allowTwoFactorAttempt("2fa-enrol:" + fmt.Sprint(c.User.ID)) {
		return a.twoFactorReject(c, "Too many attempts — wait a minute and try again.")
	}
	code := strings.TrimSpace(c.R.PostFormValue("code"))
	step, ok := verifyTOTP(c.User.TwoFactorSecret, code, time.Now(), 0)
	if !ok {
		return a.twoFactorReject(c, "That code is not valid. Codes change every 30 seconds.")
	}
	plain, stored, err := newRecoveryCodes()
	if err != nil {
		return err
	}
	if err := a.db.WithContext(c.Ctx()).Model(c.User).Updates(map[string]any{
		"two_factor_confirmed_at": time.Now(),
		"two_factor_recovery":     stored,
		"two_factor_last_step":    step,
	}).Error; err != nil {
		return err
	}
	a.log.Info("steward: 2FA enrolled", "user", c.User.Username)
	vm := &twoFactorVM{Enabled: true, Required: a.cfg.Require2FA,
		RecoveryLeft: len(plain), RecoveryCodes: plain}
	return a.renderProfile(c, vm, nil)
}

// twoFactorReject re-renders the enrolment step with an error.
func (a *Admin) twoFactorReject(c *Context, msg string) error {
	setup, err := a.prepareEnrolment(c.Ctx(), c.User)
	if err != nil {
		return err
	}
	vm := a.twoFactorProfileVM(c)
	vm.Setup, vm.Error = setup, msg
	c.W.WriteHeader(http.StatusUnprocessableEntity)
	return a.renderProfile(c, vm, nil)
}

// twoFactorDisable turns 2FA off, re-authenticating with the account password
// first so a borrowed session cannot strip the second factor.
func (a *Admin) twoFactorDisable(c *Context) error {
	if err := c.R.ParseForm(); err != nil {
		return err
	}
	if a.cfg.Require2FA {
		c.Flash("error", "Two-factor authentication is required for this panel.")
		return c.Redirect(a.url("auth/profile"))
	}
	if !a.checkPassword(c.User, c.R.PostFormValue("password")) {
		vm := a.twoFactorProfileVM(c)
		vm.Error = "Enter your current password to turn off two-factor authentication."
		c.W.WriteHeader(http.StatusUnprocessableEntity)
		return a.renderProfile(c, vm, nil)
	}
	if err := a.db.WithContext(c.Ctx()).Model(c.User).Updates(map[string]any{
		"two_factor_secret":       "",
		"two_factor_confirmed_at": nil,
		"two_factor_recovery":     "",
		"two_factor_last_step":    0,
	}).Error; err != nil {
		return err
	}
	a.log.Info("steward: 2FA disabled", "user", c.User.Username)
	c.Flash("success", "Two-factor authentication turned off.")
	return c.Redirect(a.url("auth/profile"))
}

// twoFactorRegenerateCodes issues a fresh set of recovery codes, invalidating
// the old ones. Password-gated for the same reason as disabling.
func (a *Admin) twoFactorRegenerateCodes(c *Context) error {
	if err := c.R.ParseForm(); err != nil {
		return err
	}
	if !c.User.TwoFactorEnabled() {
		return c.Redirect(a.url("auth/profile"))
	}
	if !a.checkPassword(c.User, c.R.PostFormValue("password")) {
		vm := a.twoFactorProfileVM(c)
		vm.Error = "Enter your current password to replace your recovery codes."
		c.W.WriteHeader(http.StatusUnprocessableEntity)
		return a.renderProfile(c, vm, nil)
	}
	plain, stored, err := newRecoveryCodes()
	if err != nil {
		return err
	}
	if err := a.db.WithContext(c.Ctx()).Model(c.User).
		Update("two_factor_recovery", stored).Error; err != nil {
		return err
	}
	a.log.Info("steward: 2FA recovery codes replaced", "user", c.User.Username)
	vm := a.twoFactorProfileVM(c)
	vm.RecoveryCodes, vm.RecoveryLeft = plain, len(plain)
	return a.renderProfile(c, vm, nil)
}

// checkPassword compares a plaintext password against the stored hash.
func (a *Admin) checkPassword(u *AdminUser, password string) bool {
	if password == "" {
		return false
	}
	return comparePassword(u.Password, password)
}

// ---- enforcement ------------------------------------------------------------

// allowTwoFactorAttempt consumes one unit of a challenge or enrolment budget.
// Unlike the token endpoint's limiter this one is never disabled: a six-digit
// code is small enough that unbounded guessing would defeat the second factor
// entirely.
func (a *Admin) allowTwoFactorAttempt(key string) bool {
	if a.twoFALimiter == nil {
		return true
	}
	ok, _ := a.twoFALimiter.allow(key, twoFactorRateLimit, time.Now())
	return ok
}

// twoFactorGate redirects a signed-in but unenrolled user to the profile
// page when Config.Require2FA is set. It runs after auth, so the user exists.
func (a *Admin) twoFactorGate(next http.Handler) http.Handler {
	if !a.cfg.Require2FA {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := userOf(r)
		// Bearer clients carry no session to enrol through; token auth is
		// already an explicit, separately-scoped credential.
		if user == nil || user.TwoFactorEnabled() || tokenOf(r) != nil {
			next.ServeHTTP(w, r)
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, a.cfg.Prefix)
		rel = "/" + strings.TrimLeft(rel, "/")
		// The enrolment flow itself, logout, and assets must stay reachable.
		if strings.HasPrefix(rel, "/auth/") || strings.HasPrefix(rel, "/_assets/") {
			next.ServeHTTP(w, r)
			return
		}
		if wantsJSONLike(r) {
			a.deny(w, r, http.StatusForbidden, "two-factor enrolment required")
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", a.url("auth/profile"))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		http.Redirect(w, r, a.url("auth/profile"), http.StatusFound)
	})
}

// twoFactorColumns is the schema this feature adds, applied by core migration
// 0004 so existing installations pick it up on `migrate up`.
func twoFactorColumns(tx *gorm.DB, model any) error {
	mig := tx.Migrator()
	for _, col := range []string{
		"TwoFactorSecret", "TwoFactorConfirmedAt", "TwoFactorRecovery", "TwoFactorLastStep",
	} {
		if mig.HasColumn(model, col) {
			continue
		}
		if err := mig.AddColumn(model, col); err != nil {
			return err
		}
	}
	return nil
}
