package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// The command catalog: CLI data commands and MCP tools over the registered
// resources, run through the panel's own handlers.

type mcPost struct {
	ID     uint `gorm:"primaryKey"`
	Title  string
	Status string
	Live   bool
	Body   string
}

type mcSecret struct {
	ID   uint `gorm:"primaryKey"`
	Note string
}

type mcpFixture struct {
	app *steward.Panel
	db  *gorm.DB
	srv *httptest.Server
}

func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&mcPost{}, &mcSecret{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&mcPost{Title: "Seeded", Status: "draft"})
	app, err := steward.New(steward.Config{
		Prefix: "/admin", DB: db, SecretKey: []byte("mcp-test-secret-key-0000000"),
		EnableTokenAuth: true, EnableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	publish := func(c *steward.Context, ids []string) (*steward.Envelope, error) {
		err := c.Panel.DB().Model(&mcPost{}).Where("id IN ?", ids).Update("status", "published").Error
		return steward.Success("Published."), err
	}
	steward.Register[mcPost](app).Slug("posts").Describe("News posts on the public site.").History().
		Grid(func(g *steward.Grid[mcPost]) {
			g.Column("Title").Sortable()
			g.Column("Status")
			g.QuickSearch("Title")
			g.Filter(func(f *steward.Filters[mcPost]) {
				f.Equal("Status").Select(steward.Options{"draft": "Draft", "published": "Published"})
			})
			g.BatchAction(steward.NewAction("publish", "Publish", publish))
		}).
		Form(func(f *steward.Form[mcPost]) {
			f.Text("Title").Rules("required|max:50")
			f.Select("Status").Options(steward.Options{"draft": "Draft", "published": "Published"})
			f.Switch("Live")
			f.Textarea("Body")
		})
	steward.Register[mcSecret](app).DisableMCP()
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("op-password"), bcrypt.MinCost)
	db.Model(&steward.User{}).Where("username = ?", "admin").Update("password", string(hash))
	list := steward.Permission{Name: "List", Slug: "mc-list", HTTPMethod: "GET", HTTPPath: "/posts\n/posts/*"}
	db.Create(&list)
	addUser(t, app, "reader", []steward.Permission{list})
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	return &mcpFixture{app: app, db: db, srv: srv}
}

// cli runs the app CLI against the fixture's panel, returning what it printed.
func (fx *mcpFixture) cli(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	return runCLIWith(t, fx.app, stdin, args...)
}

func runCLIWith(t *testing.T, app *steward.Panel, stdin string, args ...string) (string, error) {
	t.Helper()
	outR, outW, _ := os.Pipe()
	inR, inW, _ := os.Pipe()
	oldOut, oldIn := os.Stdout, os.Stdin
	os.Stdout, os.Stdin = outW, inR
	go func() {
		_, _ = io.WriteString(inW, stdin)
		_ = inW.Close()
	}()
	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(outR)
		done <- b
	}()
	err := steward.RunCLI(steward.App{Build: func() (*steward.Panel, error) { return app, nil }}, args)
	os.Stdout, os.Stdin = oldOut, oldIn
	_ = outW.Close()
	_ = inR.Close()
	return string(<-done), err
}

// token signs in through the token endpoint.
func (fx *mcpFixture) token(t *testing.T, username string) string {
	t.Helper()
	body := `{"username":"` + username + `","password":"op-password","name":"mcp"}`
	resp, err := http.Post(fx.srv.URL+"/admin/auth/token", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
		Token string `json:"token"`
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	tok := out.Token
	if tok == "" {
		tok = out.Data.Token
	}
	if tok == "" {
		t.Fatalf("no token for %s: %s", username, raw)
	}
	return tok
}

type rpcReply struct {
	ID     json.RawMessage `json:"id"`
	Result struct {
		ProtocolVersion   string           `json:"protocolVersion"`
		Tools             []map[string]any `json:"tools"`
		IsError           bool             `json:"isError"`
		StructuredContent map[string]any   `json:"structuredContent"`
		Content           []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (r rpcReply) text() string {
	if len(r.Result.Content) == 0 {
		return ""
	}
	return r.Result.Content[0].Text
}

func (fx *mcpFixture) rpc(t *testing.T, token, body string) (int, rpcReply) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, fx.srv.URL+"/admin/_mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out rpcReply
	raw, _ := io.ReadAll(resp.Body)
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func call(name, args string) string {
	return `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

func toolNames(r rpcReply) []string {
	var out []string
	for _, tl := range r.Result.Tools {
		out = append(out, tl["name"].(string))
	}
	return out
}

func TestMCPOverHTTP(t *testing.T) {
	fx := newMCPFixture(t)
	admin := fx.token(t, "admin")

	code, init := fx.rpc(t, admin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	if code != http.StatusOK || init.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("initialize = %d %+v", code, init)
	}
	if code, _ := fx.rpc(t, admin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Errorf("a notification should be accepted without a body, got %d", code)
	}

	_, list := fx.rpc(t, admin, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	names := toolNames(list)
	for _, want := range []string{"posts_list", "posts_get", "posts_create", "posts_update", "posts_delete", "posts_publish", "auth_users_list"} {
		if !strings.Contains(strings.Join(names, " "), want) {
			t.Errorf("tools/list lacks %s", want)
		}
	}
	for _, n := range names {
		if strings.HasPrefix(n, "mc_secrets") {
			t.Error("a resource with DisableMCP should offer no tools")
		}
	}
	var create map[string]any
	for _, tl := range list.Result.Tools {
		if tl["name"] == "posts_create" {
			create = tl
		}
	}
	schema := create["inputSchema"].(map[string]any)
	props := schema["properties"].(map[string]any)
	if props["Live"].(map[string]any)["type"] != "boolean" || !strings.Contains(create["description"].(string), "News posts") {
		t.Errorf("posts_create's schema should come from the form: %v", create)
	}
	if req, _ := schema["required"].([]any); len(req) != 1 || req[0] != "Title" {
		t.Errorf("Title is required by its rule: %v", schema["required"])
	}

	_, made := fx.rpc(t, admin, call("posts_create", `{"Title":"Via MCP","Status":"published","Live":true}`))
	if made.Result.IsError {
		t.Fatalf("create failed: %s", made.text())
	}
	var row mcPost
	fx.db.Where("title = ?", "Via MCP").First(&row)
	if row.ID == 0 || !row.Live || row.Status != "published" {
		t.Fatalf("the created row = %+v", row)
	}

	_, bad := fx.rpc(t, admin, call("posts_create", `{"Title":""}`))
	if !bad.Result.IsError || !strings.Contains(bad.text(), "Title") {
		t.Errorf("a failed rule should come back as an error naming the field: %+v", bad)
	}
	_, wrong := fx.rpc(t, admin, call("posts_create", `{"Title":"x","Nope":1}`))
	if !wrong.Result.IsError || !strings.Contains(wrong.text(), "Nope is not an argument") {
		t.Errorf("an unknown argument should be refused before running: %s", wrong.text())
	}

	id := jsonNumber(row.ID)
	if _, upd := fx.rpc(t, admin, call("posts_update", `{"id":"`+id+`","Live":false}`)); upd.Result.IsError {
		t.Fatalf("update failed: %s", upd.text())
	}
	fx.db.First(&row, row.ID)
	if row.Live || row.Title != "Via MCP" {
		t.Errorf("an update should change only what it names: %+v", row)
	}

	_, found := fx.rpc(t, admin, call("posts_list", `{"f_Status":"published","sort":"-ID"}`))
	if found.Result.IsError || found.Result.StructuredContent["total"] != float64(1) {
		t.Errorf("a filtered list = %s", found.text())
	}
	if _, act := fx.rpc(t, admin, call("posts_publish", `{"ids":["1"]}`)); act.Result.IsError {
		t.Errorf("the batch action failed: %s", act.text())
	}

	// The catalog is the same for every caller; the call is not.
	reader := fx.token(t, "reader")
	_, rlist := fx.rpc(t, reader, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if strings.Join(toolNames(rlist), ",") != strings.Join(names, ",") {
		t.Error("tools/list should not depend on who asks")
	}
	_, denied := fx.rpc(t, reader, call("posts_create", `{"Title":"Not allowed"}`))
	if !denied.Result.IsError || !strings.Contains(strings.ToLower(denied.text()), "permission") {
		t.Errorf("a reader's create should be refused with the reason: %s", denied.text())
	}
	if _, ok := fx.rpc(t, reader, call("posts_get", `{"id":"1"}`)); ok.Result.IsError {
		t.Errorf("a reader may read: %s", ok.text())
	}

	if entries, _ := fx.app.History(t.Context(), "posts", id, 10); len(entries) != 2 || entries[1].UserID == 0 {
		t.Errorf("history should record the agent's account, got %+v", entries)
	}

	if code, _ := fx.rpc(t, "", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", code)
	}
	req, _ := http.NewRequest(http.MethodPost, fx.srv.URL+"/admin/_mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("a cross-origin request should be refused: %v %v", resp.StatusCode, err)
	}
	_, unknown := fx.rpc(t, admin, `{"jsonrpc":"2.0","id":9,"method":"resources/list"}`)
	if unknown.Error == nil || unknown.Error.Code != -32601 {
		t.Errorf("an unknown method = %+v", unknown.Error)
	}
	_, garbage := fx.rpc(t, admin, `{not json`)
	if garbage.Error == nil || garbage.Error.Code != -32700 {
		t.Errorf("malformed JSON = %+v", garbage.Error)
	}
}

func TestMCPOverStdio(t *testing.T) {
	fx := newMCPFixture(t)
	session := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		call("posts_create", `{"Title":"From stdio"}`),
		call("posts_list", `{"q":"stdio"}`),
	}, "\n") + "\n"
	out, err := fx.cli(t, session, "mcp", "-user", "admin")
	if err != nil {
		t.Fatal(err)
	}
	var replies []rpcReply
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r rpcReply
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("stdout carried a line that is not JSON-RPC: %q", sc.Text())
		}
		replies = append(replies, r)
	}
	if len(replies) != 4 {
		t.Fatalf("4 requests should get 4 replies (the notification none), got %d:\n%s", len(replies), out)
	}
	if replies[0].Result.ProtocolVersion == "1999-01-01" || replies[0].Result.ProtocolVersion == "" {
		t.Errorf("an unknown version should be answered with the server's own: %q", replies[0].Result.ProtocolVersion)
	}
	if replies[2].Result.IsError || replies[3].Result.StructuredContent["total"] != float64(1) {
		t.Errorf("create then list = %s / %s", replies[2].text(), replies[3].text())
	}
}

func TestDataCommands(t *testing.T) {
	fx := newMCPFixture(t)

	out, err := fx.cli(t, "", "posts:create", "-user", "admin", "--Title", "From CLI", "--Live", "true")
	if err != nil {
		t.Fatalf("posts:create: %v", err)
	}
	if !strings.Contains(out, "posts created") && !strings.Contains(out, "created") {
		t.Errorf("posts:create printed %q", out)
	}
	var row mcPost
	fx.db.Where("title = ?", "From CLI").First(&row)
	if !row.Live {
		t.Errorf("--Live true should set the switch: %+v", row)
	}

	// Refused before anything runs: no account needed to say so.
	if _, err := fx.cli(t, "", "posts:create", "--Status", "draft"); err == nil || !strings.Contains(err.Error(), "Title is required") {
		t.Errorf("a missing required flag = %v", err)
	}
	if _, err := fx.cli(t, "", "posts:create", "-user", "admin", "--Title", strings.Repeat("x", 60)); err == nil || !strings.Contains(err.Error(), "50") {
		t.Errorf("a failed rule should be the command's error: %v", err)
	}
	if _, err := fx.cli(t, "", "posts:lst"); err == nil || !strings.Contains(err.Error(), "posts:list") {
		t.Errorf("a mistyped command should suggest the right one: %v", err)
	}

	t.Setenv("STEWARD_MCP_USER", "reader")
	if _, err := fx.cli(t, "", "posts:delete", "--ids", "1"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "permission") {
		t.Errorf("the env account (reader) should be refused a delete: %v", err)
	}
	if _, err := fx.cli(t, "", "posts:delete", "-user", "admin", "--ids", "1"); err != nil {
		t.Errorf("-user should beat the env: %v", err)
	}

	manifest, err := fx.cli(t, "", "tools", "-json")
	if err != nil {
		t.Fatal(err)
	}
	_, list := fx.rpc(t, fx.token(t, "admin"), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var m struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal([]byte(manifest), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Tools) != len(list.Result.Tools) || len(m.Tools) == 0 {
		t.Errorf("tools -json (%d) and tools/list (%d) should be the same catalog", len(m.Tools), len(list.Result.Tools))
	}
	help, _ := fx.cli(t, "", "tools", "posts:create")
	if !strings.Contains(help, "--Title string (required)") {
		t.Errorf("tools posts:create should describe its flags:\n%s", help)
	}
}

// A command resolves its workspace from -tenant, never from the CLI's own
// tenant-bypassing context.
func TestDataCommandsStayInTheirWorkspace(t *testing.T) {
	fx := newTenancyFixture(t)
	titles := func(user, tenant string) string {
		t.Helper()
		out, err := runCLIWith(t, fx.app, "", "memos:list", "-user", user, "-tenant", tenant)
		if err != nil {
			t.Fatalf("%s in %s: %v", user, tenant, err)
		}
		return out
	}
	acme := titles("alice", "acme")
	if !strings.Contains(acme, "acme-memo") || strings.Contains(acme, "globex-memo") {
		t.Errorf("alice in acme sees:\n%s", acme)
	}
	if out := titles("bob", "acme"); strings.Contains(out, "acme-memo") {
		t.Errorf("bob is not a member of acme and saw its memo:\n%s", out)
	}
}

func TestMCPNeedsTokenAuth(t *testing.T) {
	db := testDB(t)
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("mcp-verify-secret-key-00000"), EnableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err == nil || !strings.Contains(err.Error(), "EnableTokenAuth") {
		t.Errorf("EnableMCP without token auth should fail Verify: %v", err)
	}
}

func jsonNumber(id uint) string {
	raw, _ := json.Marshal(id)
	return string(raw)
}

// The stdio server runs under the CLI's tenant-bypassing context; a tool call
// must still be confined to the workspace its account was given.
func TestMCPStdioStaysInItsWorkspace(t *testing.T) {
	fx := newTenancyFixture(t)
	session := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		call("memos_list", `{}`) + "\n"
	out, err := runCLIWith(t, fx.app, session, "mcp", "-user", "alice", "-tenant", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "acme-memo") || strings.Contains(out, "globex-memo") {
		t.Errorf("alice's MCP session in acme listed:\n%s", out)
	}
}

// A command's -h is the catalog's description of it, not the flag package's
// list of untyped strings.
func TestDataCommandHelp(t *testing.T) {
	fx := newMCPFixture(t)
	want, err := fx.cli(t, "", "tools", "posts:create")
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-h", "--help"} {
		got, err := fx.cli(t, "", "posts:create", flag)
		if err != nil {
			t.Fatalf("posts:create %s: %v", flag, err)
		}
		if got != want {
			t.Errorf("posts:create %s printed:\n%s\nwant what tools posts:create prints:\n%s", flag, got, want)
		}
	}
	for _, line := range []string{"--Title string (required)", "--Status draft|published", "--Live boolean", "-user string"} {
		if !strings.Contains(want, line) {
			t.Errorf("the help lacks %q", line)
		}
	}
}
