package main

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// signIn drives the real login form, so the endpoints under test are reached
// the way a browser reaches them: session cookie, CSRF and all.
func signIn(t *testing.T, client *http.Client, base string) {
	t.Helper()
	resp, err := client.Get(base + "/admin/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	page := readBody(t, resp)
	m := notifyTokenRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no CSRF token on the login page")
	}
	form := url.Values{"username": {"admin"}, "password": {"admin"}, "_token": {m[1]}}
	resp, err = client.PostForm(base+"/admin/auth/login", form)
	if err != nil {
		t.Fatal(err)
	}
	_ = readBody(t, resp)
	if resp.StatusCode >= 400 {
		t.Fatalf("login = %d", resp.StatusCode)
	}
}

var notifyTokenRe = regexp.MustCompile(`name="_token" value="([^"]+)"`)

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func getPath(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, readBody(t, resp)
}

func newNotifyApp(t *testing.T) (*steward.Admin, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:notify?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB:        db,
		SecretKey: []byte("notification-test-secret-key"),
		Prefix:    "/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM admin_notifications")
	})
	return app, db
}

// The seeded administrator is user 1; a second account gives the scoping
// checks someone to be confused with.
func secondUser(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	u := steward.AdminUser{Username: "editor", Name: "Editor"}
	if err := db.Where("username = ?", "editor").FirstOrCreate(&u).Error; err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestNotifyStoresAndCounts(t *testing.T) {
	app, db := newNotifyApp(t)
	ctx := context.Background()
	other := secondUser(t, db)

	if err := app.Notify(ctx, 1, steward.Notification{Title: "First", Body: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Notify(ctx, 1, steward.Notification{Title: "Second", URL: "/admin/posts/2"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Notify(ctx, other, steward.Notification{Title: "Not yours"}); err != nil {
		t.Fatal(err)
	}

	n, err := app.UnreadNotifications(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("unread for user 1 = %d, want 2", n)
	}

	items, err := app.Notifications(ctx, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("listed %d notifications, want 2", len(items))
	}
	for _, it := range items {
		if it.Title == "Not yours" {
			t.Fatal("one account's list contains another's notification")
		}
		if it.Read() {
			t.Fatalf("%q arrived already read", it.Title)
		}
	}

	// A title is the one thing the panel cannot render without.
	if err := app.Notify(ctx, 1, steward.Notification{Body: "no title"}); err == nil {
		t.Error("a notification without a title was accepted")
	}
	if err := app.Notify(ctx, 0, steward.Notification{Title: "nobody"}); err == nil {
		t.Error("a notification addressed to nobody was accepted")
	}
}

func TestMarkReadIsScopedToItsOwner(t *testing.T) {
	app, db := newNotifyApp(t)
	ctx := context.Background()
	other := secondUser(t, db)

	if err := app.Notify(ctx, other, steward.Notification{Title: "Theirs"}); err != nil {
		t.Fatal(err)
	}
	theirs, err := app.Notifications(ctx, other, 0)
	if err != nil || len(theirs) != 1 {
		t.Fatalf("setup: %v, %d rows", err, len(theirs))
	}

	// User 1 marking another account's notification must not touch it.
	if err := app.MarkNotificationRead(ctx, 1, theirs[0].ID); err != nil {
		t.Fatal(err)
	}
	n, err := app.UnreadNotifications(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("after another account marked it read, unread = %d, want 1", n)
	}

	if err := app.MarkNotificationRead(ctx, other, theirs[0].ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := app.UnreadNotifications(ctx, other); n != 0 {
		t.Fatalf("the owner's mark-read left unread = %d, want 0", n)
	}
}

func TestNotifyRoleReachesEachHolderOnce(t *testing.T) {
	app, db := newNotifyApp(t)
	ctx := context.Background()

	// Two roles, one account holding both: the account must be notified once.
	var admin steward.Role
	if err := db.Where("slug = ?", "administrator").First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	extra := steward.Role{Name: "Editor", Slug: "editor"}
	if err := db.Where("slug = ?", "editor").FirstOrCreate(&extra).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO admin_role_users (role_id, user_id) VALUES (?, ?)", extra.ID, 1).Error; err != nil {
		t.Fatal(err)
	}

	if err := app.NotifyRole(ctx, steward.Notification{Title: "Both roles"}, "administrator", "editor"); err != nil {
		t.Fatal(err)
	}
	n, err := app.UnreadNotifications(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("an account holding both roles got %d notifications, want 1", n)
	}

	if err := app.NotifyRole(ctx, steward.Notification{Title: "No role named"}); err == nil {
		t.Error("NotifyRole with no roles was accepted")
	}
}

func TestPayloadRoundTrips(t *testing.T) {
	app, _ := newNotifyApp(t)
	ctx := context.Background()

	type ref struct {
		Resource string `json:"resource"`
		ID       uint   `json:"id"`
	}
	in := ref{Resource: "posts", ID: 42}
	n := steward.Notification{Title: "Payload"}.WithPayload(in)
	if err := app.Notify(ctx, 1, n); err != nil {
		t.Fatal(err)
	}

	items, err := app.Notifications(ctx, 1, 0)
	if err != nil || len(items) == 0 {
		t.Fatalf("listing: %v, %d rows", err, len(items))
	}
	var out ref
	if err := items[0].Payload(&out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("payload came back as %+v, want %+v", out, in)
	}

	// A notification stored without one must not be a special case.
	plain := steward.Notification{Title: "No payload"}
	if err := app.Notify(ctx, 1, plain); err != nil {
		t.Fatal(err)
	}
	if err := (&plain).Payload(&out); err != nil {
		t.Fatalf("reading an absent payload failed: %v", err)
	}
}

func TestPruneKeepsUnread(t *testing.T) {
	app, db := newNotifyApp(t)
	ctx := context.Background()

	if err := app.Notify(ctx, 1, steward.Notification{Title: "old read"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Notify(ctx, 1, steward.Notification{Title: "old unread"}); err != nil {
		t.Fatal(err)
	}
	items, _ := app.Notifications(ctx, 1, 0)
	var readID uint
	for _, it := range items {
		if it.Title == "old read" {
			readID = it.ID
		}
	}
	if err := app.MarkNotificationRead(ctx, 1, readID); err != nil {
		t.Fatal(err)
	}
	// Age both past the cutoff.
	long := time.Now().Add(-90 * 24 * time.Hour)
	if err := db.Exec("UPDATE admin_notifications SET created_at = ?", long).Error; err != nil {
		t.Fatal(err)
	}

	gone, err := app.PruneNotifications(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if gone != 1 {
		t.Fatalf("pruned %d rows, want 1", gone)
	}
	left, _ := app.Notifications(ctx, 1, 0)
	if len(left) != 1 || left[0].Title != "old unread" {
		t.Fatalf("prune left %+v, want only the unread one", left)
	}
}

// The bell's endpoints must answer for a signed-in account and be scoped to
// it, and the badge must reach the header markup.
func TestBellEndpoints(t *testing.T) {
	app, _ := newNotifyApp(t)
	ctx := context.Background()
	if err := app.Notify(ctx, 1, steward.Notification{Title: "Ping", Body: "a body", Icon: "bell"}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)

	code, body := getPath(t, client, srv.URL+"/admin/_notifications/badge")
	if code != http.StatusOK {
		t.Fatalf("badge = %d, want 200", code)
	}
	if !strings.Contains(body, "steward-notification-count") || !strings.Contains(body, ">1<") {
		t.Fatalf("badge did not report one unread: %q", body)
	}

	code, body = getPath(t, client, srv.URL+"/admin/_notifications")
	if code != http.StatusOK {
		t.Fatalf("list = %d, want 200", code)
	}
	// A fragment, not a page: a template error used to answer 200 with the
	// error page appended, whose own header carries the badge markup and so
	// satisfied a bare "contains" check.
	if strings.Contains(body, "<html") || strings.Contains(body, "went wrong") {
		t.Fatalf("the fragment came back as a whole page: %d bytes", len(body))
	}
	for _, want := range []string{
		"Ping", "a body", "is-unread",
		`hx-swap-oob`,      // the badge, swapped out of band
		`/_notifications/`, // the mark-read control
	} {
		if !strings.Contains(body, want) {
			t.Errorf("list is missing %q", want)
		}
	}

	// An unread row without a URL must offer a way to mark it read.
	if !strings.Contains(body, "steward-notification-check") {
		t.Error("an unread notification has no mark-read control")
	}

	// The page itself carries the bell.
	code, body = getPath(t, client, srv.URL+"/admin/")
	if code != http.StatusOK {
		t.Fatalf("dashboard = %d, want 200", code)
	}
	if !strings.Contains(body, `id="notification-trigger"`) {
		t.Fatal("the header has no bell")
	}
}

// A URL is followed only when it is a path on this origin, so a stored
// "//evil.example" cannot turn the bell into an open redirect.
func TestFollowingANotificationStaysOnThisOrigin(t *testing.T) {
	app, _ := newNotifyApp(t)
	ctx := context.Background()
	for _, url := range []string{"//evil.example/x", "https://evil.example/x", "/admin/auth/profile"} {
		if err := app.Notify(ctx, 1, steward.Notification{Title: "go " + url, URL: url}); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	signIn(t, client, srv.URL)

	items, err := app.Notifications(ctx, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		resp, err := client.Get(srv.URL + "/admin/_notifications/" + itoa(it.ID) + "/go")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		loc := resp.Header.Get("Location")
		switch it.URL {
		case "/admin/auth/profile":
			if loc != "/admin/auth/profile" {
				t.Errorf("a local URL redirected to %q", loc)
			}
		default:
			if loc != "/admin" && loc != "/admin/" {
				t.Errorf("URL %q redirected off-origin to %q", it.URL, loc)
			}
		}
	}
}
