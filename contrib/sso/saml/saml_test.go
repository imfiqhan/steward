package saml_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/xml"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	crewjam "github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
	"github.com/imfiqhan/steward/contrib/sso/saml"
)

func keyPair(t *testing.T, cn string) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return key, cert
}

type fixedSession struct{ s *crewjam.Session }

func (f fixedSession) GetSession(http.ResponseWriter, *http.Request, *crewjam.IdpAuthnRequest) *crewjam.Session {
	return f.s
}

type spProvider struct{ md *crewjam.EntityDescriptor }

func (p *spProvider) GetServiceProvider(*http.Request, string) (*crewjam.EntityDescriptor, error) {
	return p.md, nil
}

// idp starts a crewjam identity provider that signs in one fixed user.
func idp(t *testing.T, sp *spProvider) *httptest.Server {
	t.Helper()
	key, cert := keyPair(t, "idp")
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	meta, sso := *base, *base
	meta.Path, sso.Path = "/metadata", "/sso"
	id := &crewjam.IdentityProvider{
		Key: key, Certificate: cert, MetadataURL: meta, SSOURL: sso,
		ServiceProviderProvider: sp,
		SessionProvider: fixedSession{&crewjam.Session{
			ID: "sess", CreateTime: time.Now(), ExpireTime: time.Now().Add(time.Hour), Index: "1",
			NameID: "ivan@corp.example", NameIDFormat: string(crewjam.EmailAddressNameIDFormat),
			UserEmail: "ivan@corp.example", UserCommonName: "Ivan Petrov",
		}},
	}
	mux.Handle("/", id.Handler())
	return srv
}

var (
	inputRe = regexp.MustCompile(`name="(SAMLResponse|RelayState)" value="([^"]*)"`)
)

func TestSAMLSignIn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/t.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := saml.Migration().Up(db); err != nil {
		t.Fatal(err)
	}
	spRegistry := &spProvider{}
	idpSrv := idp(t, spRegistry)
	if err := db.Create(&saml.Connection{
		TenantID: 7, Slug: "corp", Name: "Corp", Domains: "corp.example",
		IDPMetadataURL: idpSrv.URL + "/metadata", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	spKey, spCert := keyPair(t, "sp")
	src, err := saml.NewSource(saml.Config{DB: db, Key: spKey, Certificate: spCert})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	p, err := src.Discover(ctx, "Ivan@Corp.Example")
	if err != nil || p == nil || p.ID() != "saml-corp" {
		t.Fatalf("discover by email: %v %v", p, err)
	}
	if p, _ := src.Discover(ctx, "nobody@else.example"); p != nil {
		t.Fatal("discovered a connection for an unknown domain")
	}

	callback := "https://panel.test/admin/auth/sso/saml-corp/callback"
	body, ctype, err := p.(steward.MetadataProvider).Metadata(ctx, callback)
	if err != nil || !strings.Contains(ctype, "xml") {
		t.Fatalf("SP metadata: %v %s", err, ctype)
	}
	spMD, err := samlsp.ParseMetadata(body)
	if err != nil {
		t.Fatal(err)
	}
	spRegistry.md = spMD

	req := &steward.SSORequest{State: "relay-1", CallbackURL: callback, Values: map[string]string{}}
	authURL, err := p.AuthURL(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	page := readBody(t, resp)
	fields := map[string]string{}
	for _, m := range inputRe.FindAllStringSubmatch(page, -1) {
		fields[m[1]] = htmlUnescape(m[2])
	}
	if fields["RelayState"] != "relay-1" || fields["SAMLResponse"] == "" {
		t.Fatalf("IdP response form: %q", page)
	}

	post := func() (*steward.ExternalIdentity, error) {
		form := url.Values{"SAMLResponse": {fields["SAMLResponse"]}, "RelayState": {fields["RelayState"]}}
		r, _ := http.NewRequest(http.MethodPost, callback, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		_ = r.ParseForm()
		return p.Exchange(ctx, req, r)
	}
	// Checked first: a response is refused before it counts as used.
	other := &steward.SSORequest{State: "relay-2", CallbackURL: callback, Values: map[string]string{"rid": "id-not-ours"}}
	form := url.Values{"SAMLResponse": {fields["SAMLResponse"]}}
	r, _ := http.NewRequest(http.MethodPost, callback, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	if _, err := p.Exchange(ctx, other, r); err == nil {
		t.Fatal("accepted a response to a request this sign-in did not make")
	}

	id, err := post()
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "ivan@corp.example" || id.Email != "ivan@corp.example" || !id.EmailVerified ||
		id.Name != "Ivan Petrov" || id.TenantID != 7 {
		t.Fatalf("identity %+v", id)
	}

	if _, err := post(); err == nil {
		t.Fatal("the same assertion was accepted twice")
	}

}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var b strings.Builder
	buf := make([]byte, 8192)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return b.String()
}

func htmlUnescape(s string) string {
	var out string
	if err := xml.Unmarshal([]byte("<x>"+s+"</x>"), &out); err != nil {
		return s
	}
	return out
}
