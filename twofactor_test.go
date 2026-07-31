package steward

import (
	"strings"
	"testing"
	"time"
)

// TestTOTPRFC6238Vectors checks totpCode against the SHA-1 test vectors in
// RFC 6238 appendix B, which use the ASCII secret "12345678901234567890".
// They are published for eight digits, so the expected six-digit code is the
// low six digits of each.
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := base32Secret.EncodeToString([]byte("12345678901234567890"))
	for _, tc := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},          // 8-digit: 94287082
		{1111111109, "081804"},  // 07081804
		{1111111111, "050471"},  // 14050471
		{1234567890, "005924"},  // 89005924
		{2000000000, "279037"},  // 69279037
		{20000000000, "353130"}, // 65353130
	} {
		step := tc.unix / totpPeriod
		got, err := totpCode(secret, step)
		if err != nil {
			t.Fatalf("t=%d: %v", tc.unix, err)
		}
		if got != tc.want {
			t.Errorf("t=%d: code %s, want %s", tc.unix, got, tc.want)
		}
	}
}

func TestVerifyTOTPAcceptsSkewWindow(t *testing.T) {
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	current := now.Unix() / totpPeriod

	for _, delta := range []int64{-1, 0, 1} {
		code, err := totpCode(secret, current+delta)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := verifyTOTP(secret, code, now, 0); !ok {
			t.Errorf("step offset %d should be accepted", delta)
		}
	}
	for _, delta := range []int64{-2, 2, 10} {
		code, err := totpCode(secret, current+delta)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := verifyTOTP(secret, code, now, 0); ok {
			t.Errorf("step offset %d should be outside the window", delta)
		}
	}
}

// TestVerifyTOTPRejectsReplay is the property that makes a code single-use:
// once a step has been accepted, notBefore refuses it and everything older.
func TestVerifyTOTPRejectsReplay(t *testing.T) {
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	code, err := totpCode(secret, now.Unix()/totpPeriod)
	if err != nil {
		t.Fatal(err)
	}
	step, ok := verifyTOTP(secret, code, now, 0)
	if !ok {
		t.Fatal("first use should be accepted")
	}
	if _, ok := verifyTOTP(secret, code, now, step); ok {
		t.Error("the same code must not be accepted twice")
	}
	// A code from an earlier step in the skew window is also spent.
	older, err := totpCode(secret, now.Unix()/totpPeriod-1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := verifyTOTP(secret, older, now, step); ok {
		t.Error("a code older than the last accepted step must be refused")
	}
}

func TestVerifyTOTPRejectsMalformed(t *testing.T) {
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, code := range []string{"", "12345", "1234567", "abcdef", "   "} {
		if _, ok := verifyTOTP(secret, code, now, 0); ok {
			t.Errorf("code %q should be rejected", code)
		}
	}
}

func TestVerifyTOTPRejectsBadSecret(t *testing.T) {
	if _, ok := verifyTOTP("not!base32!", "123456", time.Now(), 0); ok {
		t.Error("an undecodable secret must never verify")
	}
}

func TestNewTOTPSecretIsDecodableAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		s, err := newTOTPSecret()
		if err != nil {
			t.Fatal(err)
		}
		if len(s) != 32 { // 20 bytes, base32, unpadded
			t.Fatalf("secret %q has length %d, want 32", s, len(s))
		}
		if _, err := base32Secret.DecodeString(s); err != nil {
			t.Fatalf("secret %q does not decode: %v", s, err)
		}
		if seen[s] {
			t.Fatalf("secret %q generated twice", s)
		}
		seen[s] = true
	}
}

func TestRecoveryCodesRoundTrip(t *testing.T) {
	plain, stored, err := newRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != recoveryCodeCount {
		t.Fatalf("got %d codes, want %d", len(plain), recoveryCodeCount)
	}
	if countRecoveryCodes(stored) != recoveryCodeCount {
		t.Fatalf("stored %d digests, want %d", countRecoveryCodes(stored), recoveryCodeCount)
	}
	// The plaintext must not appear in what is persisted.
	for _, c := range plain {
		if strings.Contains(stored, c) {
			t.Fatalf("plaintext code %q leaked into the stored digests", c)
		}
	}
	// Every code works exactly once, and spending one leaves the rest usable.
	remaining := stored
	for i, c := range plain {
		var ok bool
		remaining, ok = consumeRecoveryCode(remaining, c)
		if !ok {
			t.Fatalf("code %d (%s) should have been accepted", i, c)
		}
		if got, want := countRecoveryCodes(remaining), recoveryCodeCount-i-1; got != want {
			t.Fatalf("after spending %d codes, %d remain, want %d", i+1, got, want)
		}
		if _, reused := consumeRecoveryCode(remaining, c); reused {
			t.Fatalf("code %d (%s) was accepted twice", i, c)
		}
	}
}

func TestRecoveryCodeNormalization(t *testing.T) {
	plain, stored, err := newRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	code := plain[0]
	for _, variant := range []string{
		code,
		strings.ToLower(code),
		strings.ReplaceAll(code, "-", ""),
		"  " + strings.ToLower(strings.ReplaceAll(code, "-", "")) + "  ",
	} {
		if _, ok := consumeRecoveryCode(stored, variant); !ok {
			t.Errorf("variant %q should be accepted", variant)
		}
	}
}

func TestConsumeRecoveryCodeRejectsUnknownAndEmpty(t *testing.T) {
	_, stored, err := newRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "   ", "aaaa-bbbb-cccc-dddd", "nonsense"} {
		remaining, ok := consumeRecoveryCode(stored, bad)
		if ok {
			t.Errorf("code %q should be rejected", bad)
		}
		if countRecoveryCodes(remaining) != recoveryCodeCount {
			t.Errorf("a rejected attempt must not spend a code (%q)", bad)
		}
	}
}

func TestTwoFactorEnabledNeedsConfirmation(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		user AdminUser
		want bool
	}{
		{"fresh account", AdminUser{}, false},
		{"secret scanned but never confirmed", AdminUser{TwoFactorSecret: "ABC"}, false},
		{"confirmed without a secret", AdminUser{TwoFactorConfirmedAt: &now}, false},
		{"enrolled", AdminUser{TwoFactorSecret: "ABC", TwoFactorConfirmedAt: &now}, true},
	}
	for _, tc := range cases {
		if got := tc.user.TwoFactorEnabled(); got != tc.want {
			t.Errorf("%s: TwoFactorEnabled() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestOtpauthURI(t *testing.T) {
	a := &Admin{cfg: Config{Brand: "Kominfo Jatim"}}
	uri := a.otpauthURI("reporter01", "JBSWY3DPEHPK3PXP")
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("wrong scheme: %s", uri)
	}
	for _, want := range []string{
		"Kominfo%20Jatim:reporter01",
		"secret=JBSWY3DPEHPK3PXP",
		"issuer=Kominfo+Jatim",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI %s is missing %q", uri, want)
		}
	}
}

func TestTwoFactorRequired(t *testing.T) {
	now := time.Now()
	enrolled := &AdminUser{TwoFactorSecret: "ABC", TwoFactorConfirmedAt: &now}
	bare := &AdminUser{}

	optional := &Admin{cfg: Config{}}
	if !optional.twoFactorRequired(enrolled) {
		t.Error("an enrolled user always faces a challenge")
	}
	if optional.twoFactorRequired(bare) {
		t.Error("an unenrolled user should pass straight through when 2FA is optional")
	}

	mandatory := &Admin{cfg: Config{Require2FA: true}}
	if !mandatory.twoFactorRequired(bare) {
		t.Error("Require2FA must stop an unenrolled user")
	}
}

func TestHasRole(t *testing.T) {
	u := &AdminUser{Roles: []Role{{Slug: "reporter"}, {Slug: "photographer"}}}
	if !u.HasRole("reporter") {
		t.Error("expected reporter")
	}
	if !u.HasRole("redaktur", "photographer") {
		t.Error("expected a match on the second slug")
	}
	if u.HasRole("redaktur") {
		t.Error("did not expect redaktur")
	}
	if u.HasRole() {
		t.Error("no slugs should never match")
	}
	if u.IsAdministrator() {
		t.Error("did not expect administrator")
	}
	admin := &AdminUser{Roles: []Role{{Slug: RoleAdministrator}}}
	if !admin.IsAdministrator() {
		t.Error("expected administrator")
	}
}
