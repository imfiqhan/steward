package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// TenantMemo is a tenant-owned record.
type TenantMemo struct {
	ID uint `gorm:"primaryKey"`
	steward.TenantScoped
	Title string `gorm:"size:100"`
}

type tenancyFixture struct {
	app     *steward.Panel
	db      *gorm.DB
	base    string
	tenants map[string]steward.Tenant
	users   map[string]steward.User
	memos   map[string]TenantMemo
}

// newTenancyFixture builds a panel with two tenants:
//
//	acme:   alice (tenant administrator), a memo "acme-memo"
//	globex: alice and bob (tenant administrators), a memo "globex-memo"
//	carol:  no membership
//	admin:  the seeded platform administrator
func newTenancyFixture(t *testing.T) *tenancyFixture {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&TenantMemo{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix:    "/admin",
		DB:        db,
		SecretKey: []byte("tenancy-test-secret-key"),
		Tenancy:   &steward.TenancyConfig{},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[TenantMemo](app).Slug("memos").Form(func(f *steward.Form[TenantMemo]) {
		f.Text("Title").Rules("required|unique:tenant_memos,title,{id}")
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}

	fx := &tenancyFixture{
		app: app, db: db, base: serve(t, app).URL + "/admin",
		tenants: map[string]steward.Tenant{}, users: map[string]steward.User{}, memos: map[string]TenantMemo{},
	}
	all := db.WithContext(steward.WithoutTenant(context.Background()))

	var tenantAdmin steward.Role
	if err := all.Where("slug = ?", steward.RoleAdministrator).First(&tenantAdmin).Error; err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	for _, slug := range []string{"acme", "globex"} {
		tn := steward.Tenant{Slug: slug, Name: strings.ToUpper(slug[:1]) + slug[1:], Active: true}
		if err := all.Create(&tn).Error; err != nil {
			t.Fatal(err)
		}
		fx.tenants[slug] = tn
	}
	for _, name := range []string{"alice", "bob", "carol"} {
		u := steward.User{Username: name, Name: name, Password: string(hash)}
		if err := all.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		fx.users[name] = u
	}
	member := func(user, tenant string) {
		tu := steward.TenantUser{TenantID: fx.tenants[tenant].ID, UserID: fx.users[user].ID}
		if err := all.Create(&tu).Error; err != nil {
			t.Fatal(err)
		}
		tr := steward.TenantUserRole{TenantID: fx.tenants[tenant].ID, UserID: fx.users[user].ID, RoleID: tenantAdmin.ID}
		if err := all.Create(&tr).Error; err != nil {
			t.Fatal(err)
		}
	}
	member("alice", "acme")
	member("alice", "globex")
	member("bob", "globex")

	for _, slug := range []string{"acme", "globex"} {
		m := TenantMemo{Title: slug + "-memo"}
		ctx := steward.WithTenant(context.Background(), fx.tenants[slug].ID)
		if err := db.WithContext(ctx).Create(&m).Error; err != nil {
			t.Fatal(err)
		}
		fx.memos[slug] = m
	}
	return fx
}

func (fx *tenancyFixture) client(t *testing.T, username, password string) *tfaClient {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &tfaClient{t: t, http: &http.Client{Jar: jar}, base: fx.base}
	if status, body := c.login(username, password); status >= 400 {
		t.Fatalf("login %s: %d %s", username, status, firstLine(body))
	}
	return c
}

func memoTitles(t *testing.T, c *tfaClient) []string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, c.base+"/memos", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /memos: %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			Title string `json:"Title"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	titles := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		titles = append(titles, d.Title)
	}
	return titles
}

func titlesAre(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}

func TestTenantPluginScopesQueries(t *testing.T) {
	fx := newTenancyFixture(t)
	acme := steward.WithTenant(context.Background(), fx.tenants["acme"].ID)
	db := fx.db.WithContext(acme)

	var memos []TenantMemo
	if err := db.Find(&memos).Error; err != nil {
		t.Fatal(err)
	}
	if len(memos) != 1 || memos[0].Title != "acme-memo" {
		t.Fatalf("acme sees %+v", memos)
	}

	var n int64
	if err := db.Table("tenant_memos").Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("table count in acme = %d, %v", n, err)
	}

	res := db.Model(&TenantMemo{}).Where("id = ?", fx.memos["globex"].ID).Update("title", "stolen")
	if res.Error != nil || res.RowsAffected != 0 {
		t.Fatalf("cross-tenant update touched %d rows, %v", res.RowsAffected, res.Error)
	}
	res = db.Delete(&TenantMemo{}, fx.memos["globex"].ID)
	if res.Error != nil || res.RowsAffected != 0 {
		t.Fatalf("cross-tenant delete touched %d rows, %v", res.RowsAffected, res.Error)
	}

	foreign := TenantMemo{TenantScoped: steward.TenantScoped{TenantID: fx.tenants["globex"].ID}, Title: "planted"}
	if err := db.Create(&foreign).Error; err == nil {
		t.Fatal("created a row for another tenant")
	}

	if err := fx.db.WithContext(context.Background()).Find(&memos).Error; !errors.Is(err, steward.ErrNoTenant) {
		t.Fatalf("unscoped query: got %v, want ErrNoTenant", err)
	}

	all := fx.db.WithContext(steward.WithoutTenant(context.Background()))
	if err := all.Model(&TenantMemo{}).Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("WithoutTenant count = %d, %v", n, err)
	}
}

func TestTenantPanelIsolation(t *testing.T) {
	fx := newTenancyFixture(t)

	bob := fx.client(t, "bob", "secret")
	if got := memoTitles(t, bob); !titlesAre(got, "globex-memo") {
		t.Fatalf("bob sees %v", got)
	}
	status, _ := bob.get("/memos/" + itoa(fx.memos["acme"].ID))
	if status != http.StatusNotFound {
		t.Fatalf("bob opening acme's memo: %d, want 404", status)
	}

	// A tenant bob is not in cannot be selected.
	_, page := bob.get("/")
	bob.post("/auth/tenant", url.Values{"tenant": {itoa(fx.tenants["acme"].ID)}}, bob.token(page))
	if got := memoTitles(t, bob); !titlesAre(got, "globex-memo") {
		t.Fatalf("bob after switching to acme sees %v", got)
	}

	// The platform pages are closed to a tenant administrator.
	if status, _ := bob.get("/auth/tenants"); status != http.StatusForbidden {
		t.Fatalf("bob opening tenants: %d, want 403", status)
	}
	if status, _ := bob.get("/auth/permissions"); status != http.StatusForbidden {
		t.Fatalf("bob opening permissions: %d, want 403", status)
	}

	// The users page lists members of the tenant only.
	_, users := bob.get("/auth/users")
	if !strings.Contains(users, "alice") || strings.Contains(users, "carol") {
		t.Fatal("bob's users page does not match globex's membership")
	}

	alice := fx.client(t, "alice", "secret")
	if got := memoTitles(t, alice); !titlesAre(got, "acme-memo") {
		t.Fatalf("alice (first membership acme) sees %v", got)
	}
	_, page = alice.get("/")
	if !strings.Contains(page, `id="steward-tenant"`) {
		t.Fatal("alice belongs to two tenants but the switcher is missing")
	}
	alice.post("/auth/tenant", url.Values{"tenant": {itoa(fx.tenants["globex"].ID)}}, alice.token(page))
	if got := memoTitles(t, alice); !titlesAre(got, "globex-memo") {
		t.Fatalf("alice after switching to globex sees %v", got)
	}

	// Signing in works; the redirect lands on a page carol may not enter.
	jar, _ := cookiejar.New(nil)
	carol := &tfaClient{t: t, http: &http.Client{Jar: jar}, base: fx.base}
	if status, body := carol.login("carol", "secret"); status != http.StatusForbidden ||
		!strings.Contains(body, "not a member of any active workspace") {
		t.Fatalf("carol after login: %d %s", status, firstLine(body))
	}
	if status, _ := carol.get("/memos"); status != http.StatusForbidden {
		t.Fatalf("carol without a membership: %d, want 403", status)
	}

	admin := fx.client(t, "admin", "admin")
	if got := memoTitles(t, admin); !titlesAre(got, "acme-memo", "globex-memo") {
		t.Fatalf("platform admin in platform mode sees %v", got)
	}
	if status, _ := admin.get("/auth/tenants"); status != http.StatusOK {
		t.Fatalf("platform admin opening tenants: %d", status)
	}
}

func TestTenantFormWritesAreScoped(t *testing.T) {
	fx := newTenancyFixture(t)
	bob := fx.client(t, "bob", "secret")

	// unique: is per tenant: acme already has "acme-memo".
	_, page := bob.get("/memos/create")
	status, body := bob.post("/memos", url.Values{"Title": {"acme-memo"}}, bob.token(page))
	if status >= 400 {
		t.Fatalf("creating a title another tenant uses: %d %s", status, firstLine(body))
	}

	all := fx.db.WithContext(steward.WithoutTenant(context.Background()))
	var created TenantMemo
	if err := all.Where("title = ? AND id <> ?", "acme-memo", fx.memos["acme"].ID).First(&created).Error; err != nil {
		t.Fatal(err)
	}
	if created.TenantID != fx.tenants["globex"].ID {
		t.Fatalf("new memo stamped with tenant %d, want globex %d", created.TenantID, fx.tenants["globex"].ID)
	}

	// Removing a user from the tenant keeps the account.
	_, page = bob.get("/auth/users")
	req, _ := http.NewRequest(http.MethodDelete, bob.base+"/auth/users/"+itoa(fx.users["alice"].ID), nil)
	req.Header.Set("X-CSRF-Token", bob.token(page))
	req.Header.Set("Accept", "application/json")
	resp, err := bob.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("removing alice from globex: %d", resp.StatusCode)
	}
	var n int64
	all.Model(&steward.User{}).Where("id = ?", fx.users["alice"].ID).Count(&n)
	if n != 1 {
		t.Fatal("removing a member deleted the account")
	}
	all.Model(&steward.TenantUser{}).Where("user_id = ?", fx.users["alice"].ID).Count(&n)
	if n != 1 {
		t.Fatalf("alice has %d memberships, want 1 (acme)", n)
	}
}

func TestTenantBoundTokens(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&TenantMemo{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin", DB: db, SecretKey: []byte("tenancy-token-secret-key"),
		Tenancy: &steward.TenancyConfig{}, EnableTokenAuth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[TenantMemo](app).Slug("memos")
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	base := serve(t, app).URL + "/admin"

	all := db.WithContext(steward.WithoutTenant(context.Background()))
	var role steward.Role
	all.Where("slug = ?", steward.RoleAdministrator).First(&role)
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	dave := steward.User{Username: "dave", Password: string(hash)}
	all.Create(&dave)
	ids := map[string]uint{}
	for _, slug := range []string{"north", "south"} {
		tn := steward.Tenant{Slug: slug, Name: slug, Active: true}
		all.Create(&tn)
		ids[slug] = tn.ID
		all.Create(&steward.TenantUser{TenantID: tn.ID, UserID: dave.ID})
		all.Create(&steward.TenantUserRole{TenantID: tn.ID, UserID: dave.ID, RoleID: role.ID})
		db.WithContext(steward.WithTenant(context.Background(), tn.ID)).Create(&TenantMemo{Title: slug + "-memo"})
	}

	issue := func(body string) (int, map[string]any) {
		resp, err := http.Post(base+"/auth/token", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	status, out := issue(`{"username":"dave","password":"secret"}`)
	if status != http.StatusConflict {
		t.Fatalf("two memberships without tenant: %d %v", status, out)
	}
	if list, _ := out["tenants"].([]any); len(list) != 2 {
		t.Fatalf("conflict lists %v", out["tenants"])
	}

	status, out = issue(`{"username":"dave","password":"secret","tenant":"south"}`)
	if status != http.StatusCreated {
		t.Fatalf("issuing for south: %d %v", status, out)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/memos", nil)
	req.Header.Set("Authorization", "Bearer "+out["token"].(string))
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var list struct {
		Items []struct{ Title string } `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if len(list.Items) != 1 || list.Items[0].Title != "south-memo" {
		t.Fatalf("south token sees %+v", list.Items)
	}
}
