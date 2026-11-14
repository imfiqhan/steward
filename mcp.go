package steward

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
)

// The Model Context Protocol server: the command catalog as MCP tools, over
// stdio (`./app mcp`) or over HTTP at {prefix}/_mcp. It speaks the subset of
// the protocol a tools-only server needs — initialize, ping, tools/list,
// tools/call — as JSON-RPC 2.0, and runs every call through runCommand, so an
// agent is held to the same permissions and policies as the account it acts
// for.

// mcpProtocolVersions are the protocol revisions this server answers to,
// newest first. A client asking for one of them gets it; any other gets the
// newest.
var mcpProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC error codes.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// mcpServer answers protocol messages for one principal.
type mcpServer struct {
	a *Panel
	p principal
}

// handle answers one message. It returns nil for a notification, which gets no
// response.
func (s *mcpServer) handle(ctx context.Context, raw []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: rpcParseError, Message: "parse error: " + err.Error()}}
	}
	notification := len(req.ID) == 0
	if req.JSONRPC != "2.0" || req.Method == "" {
		if notification {
			return nil
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: rpcInvalidRequest, Message: "not a JSON-RPC 2.0 request"}}
	}
	result, rerr := s.dispatch(ctx, req)
	if notification {
		return nil
	}
	resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	return resp
}

func (s *mcpServer) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := mcpProtocolVersions[0]
		if slices.Contains(mcpProtocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo": map[string]any{
				"name": "steward", "title": s.a.cfg.Brand, "version": stewardVersion(),
			},
			"instructions": "Tools operate " + s.a.cfg.Brand + "'s records as the account the " +
				"connection authenticated as. A call that account may not make is refused " +
				"with the panel's reason; fix the arguments or ask for access rather than retrying.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "notifications/initialized", "notifications/cancelled":
		return map[string]any{}, nil
	case "tools/list":
		tools := make([]map[string]any, 0, len(s.a.commands))
		for _, c := range s.a.commands {
			tools = append(tools, c.toolJSON())
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: rpcInvalidParams, Message: "invalid params: " + err.Error()}
		}
		c, ok := s.a.lookupCommand(p.Name)
		if !ok {
			return nil, &rpcError{Code: rpcInvalidParams, Message: "unknown tool: " + p.Name}
		}
		res, err := s.a.runCommand(ctx, s.p, c, p.Arguments)
		if err != nil {
			s.a.log.Error("steward: mcp tool", "tool", p.Name, "err", err)
			return nil, &rpcError{Code: rpcInternalError, Message: "the tool failed; the panel's log says why"}
		}
		return toolResult(res), nil
	}
	return nil, &rpcError{Code: rpcMethodNotFound, Message: "method not found: " + req.Method}
}

// toolResult is a command's answer as an MCP tool result: the handler's JSON
// as structured content and as text, flagged as an error the agent can read
// when the panel refused it.
func toolResult(res *commandResult) map[string]any {
	if res.IsError {
		return map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": res.errorText()}},
		}
	}
	text, _ := json.MarshalIndent(res.Body, "", "  ")
	out := map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}
	// structuredContent has to be an object; a list answer is one already.
	if obj, ok := res.Body.(map[string]any); ok {
		out["structuredContent"] = obj
	}
	return out
}

// serveMCP runs the stdio transport: one JSON-RPC message per line in, one per
// line out, until in closes or ctx ends.
func (a *Panel) serveMCP(ctx context.Context, p principal, in io.Reader, out io.Writer) error {
	s := &mcpServer{a: a, p: p}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	enc := json.NewEncoder(out)
	var mu sync.Mutex
	for sc.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if resp := s.handle(ctx, []byte(line)); resp != nil {
			mu.Lock()
			err := enc.Encode(resp)
			mu.Unlock()
			if err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// mcpHTTP answers {prefix}/_mcp, the Streamable HTTP transport without a server
// stream: each POST carries one message and gets its answer as JSON. It is
// bearer-only — the caller acts as the token's account and workspace.
func (a *Panel) mcpHTTP(c *Context) error {
	if c.R.Method != http.MethodPost {
		c.W.Header().Set("Allow", http.MethodPost)
		c.W.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	// A browser page on another origin must not drive a local or intranet
	// server: the protocol's DNS-rebinding rule.
	if origin := c.R.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, c.R.Host) {
			return c.JSON(http.StatusForbidden, Error("cross-origin requests are refused"))
		}
	}
	tok := tokenOf(c.R)
	if tok == nil || c.User == nil {
		c.W.Header().Set("WWW-Authenticate", "Bearer")
		return c.JSON(http.StatusUnauthorized, Error("MCP needs a bearer token"))
	}
	raw, err := io.ReadAll(io.LimitReader(c.R.Body, 16<<20))
	if err != nil {
		return err
	}
	s := &mcpServer{a: a, p: principal{User: c.User, Token: tok}}
	resp := s.handle(c.Ctx(), raw)
	if resp == nil {
		c.W.WriteHeader(http.StatusAccepted)
		return nil
	}
	return c.JSON(http.StatusOK, resp)
}

// stewardVersion is the framework's module version as built into the binary.
func stewardVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/imfiqhan/steward" {
				if dep.Replace != nil && dep.Replace.Version != "" {
					return dep.Replace.Version
				}
				return dep.Version
			}
		}
		if info.Main.Path == "github.com/imfiqhan/steward" && info.Main.Version != "" {
			return info.Main.Version
		}
	}
	return "devel"
}

// mcpPrincipal resolves who the CLI acts as: -user, else STEWARD_MCP_USER,
// and -tenant, else STEWARD_MCP_TENANT. The token it returns marks the calls
// as a bearer caller's and binds the workspace; it is never stored.
func (a *Panel) mcpPrincipal(ctx context.Context, username, tenantSlug string) (principal, error) {
	if username == "" {
		return principal{}, errors.New("no account to act as: pass -user or set STEWARD_MCP_USER")
	}
	db := a.db.WithContext(WithoutTenant(ctx))
	var u User
	if err := db.Preload("Roles").Where("username = ?", username).First(&u).Error; err != nil {
		return principal{}, fmt.Errorf("no account named %q", username)
	}
	tok := &Token{UserID: u.ID, Name: "cli"}
	if tenantSlug != "" {
		if !a.TenancyEnabled() {
			return principal{}, errors.New("-tenant needs Config.Tenancy")
		}
		var t Tenant
		if err := db.Where("slug = ?", tenantSlug).First(&t).Error; err != nil {
			return principal{}, fmt.Errorf("no workspace with the slug %q", tenantSlug)
		}
		tok.TenantID = t.ID
	}
	return principal{User: &u, Token: tok}, nil
}
