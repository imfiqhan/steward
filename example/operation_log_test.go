package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type logRow struct {
	ID     uint `gorm:"primaryKey"`
	Title  string
	Secret string
}

func newOpLogServer(t *testing.T) (*httptest.Server, *gorm.DB) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&logRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin", DB: db, SecretKey: []byte("oplog-test-secret-key-0000"),
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[logRow](app).Form(func(f *steward.Form[logRow]) {
		f.Text("Title")
		f.Text("Secret")
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv, db
}

// The entry is there as soon as the request that caused it has answered.
//
// A fast insert cannot tell the two arrangements apart — a goroutine usually
// wins that race too — so the insert is made slow. Detached, the request then
// answers while the write is still in flight and this finds nothing.
func TestOperationLogIsWrittenBeforeTheRequestAnswers(t *testing.T) {
	srv, db := newOpLogServer(t)
	slowOperationLogInsert(t, db, 300*time.Millisecond)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)
	postLogRow(t, client, srv, map[string]string{"Title": "a memo", "Secret": "hunter2"})

	var entries []steward.OperationLog
	if err := db.Order("id desc").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("the mutating request left no operation log entry")
	}
	last := entries[0]
	if last.Method != http.MethodPost || !strings.Contains(last.Path, "log_rows") {
		t.Errorf("the entry does not describe the request: %+v", last)
	}
	if last.UserID == 0 {
		t.Error("the entry names no user")
	}
	// Masking is the reason the log is safe to keep at all.
	if strings.Contains(last.Input, "hunter2") {
		t.Error("a masked field reached the log in clear text")
	}
}

// Reads are not recorded; a log of every page view is a different feature and
// a much larger table.
func TestOperationLogIgnoresReads(t *testing.T) {
	srv, db := newOpLogServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)

	var before int64
	db.Model(&steward.OperationLog{}).Count(&before)
	resp, err := client.Get(srv.URL + "/admin/log_rows")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	var after int64
	db.Model(&steward.OperationLog{}).Count(&after)
	if after != before {
		t.Errorf("a read was logged: %d entries became %d", before, after)
	}
}

// postLogRow creates one row through the form, token and all.
func postLogRow(t *testing.T, client *http.Client, srv *httptest.Server, values map[string]string) {
	t.Helper()
	resp, err := client.Get(srv.URL + "/admin/log_rows/create")
	if err != nil {
		t.Fatal(err)
	}
	m := notifyTokenRe.FindStringSubmatch(readBody(t, resp))
	if m == nil {
		t.Fatal("no CSRF token on the create form")
	}
	form := url.Values{"_token": {m[1]}}
	for k, v := range values {
		form.Set(k, v)
	}
	resp, err = client.PostForm(srv.URL+"/admin/log_rows", form)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode >= 400 {
		t.Fatalf("POST = %d %s", resp.StatusCode, body)
	}
}

// slowOperationLogInsert makes writing one operation log entry take long
// enough that a write left to a goroutine has not finished when the request
// it belongs to has answered.
func slowOperationLogInsert(t *testing.T, db *gorm.DB, d time.Duration) {
	t.Helper()
	err := db.Callback().Create().Before("gorm:create").
		Register("test:slow_operation_log", func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == "admin_operation_log" {
				time.Sleep(d)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
}
