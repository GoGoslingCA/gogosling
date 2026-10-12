package extension

// 本文件覆盖审查修复的回归：MCP Headers 的深拷贝与实际发送（LAY-04）、插件子进程环境白名单（LAY-13）。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/protocol/mcp"
)

func TestRegisterMCPDeepCopiesHeaders(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	cfg := model.MCPServerConfig{
		Transport: model.MCPHTTP,
		URL:       "https://mcp.example.com/mcp",
		Headers:   map[string]string{"Authorization": "secret:mcp/token"},
	}
	ext, err := env.m.RegisterMCP(ctx, "带鉴权头的服务", cfg)
	if err != nil {
		t.Fatalf("登记应成功: %v", err)
	}
	if ext.MCP == nil || ext.MCP.Headers["Authorization"] != "secret:mcp/token" {
		t.Fatalf("记录应保存 Headers: %+v", ext.MCP)
	}
	cfg.Headers["Authorization"] = "secret:changed"
	got, err := env.m.Get(ctx, ext.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MCP.Headers["Authorization"] != "secret:mcp/token" {
		t.Errorf("调用方修改原配置的 Headers 不应影响已登记的记录，得到 %q", got.MCP.Headers["Authorization"])
	}
	if !strings.Contains(string(got.Manifest), `"headers"`) {
		t.Errorf("清单应包含 headers: %s", got.Manifest)
	}

	stdio := model.MCPServerConfig{Transport: model.MCPStdio, Command: "srv", Headers: map[string]string{"Authorization": "x"}}
	if _, err := env.m.RegisterMCP(ctx, "stdio 带头", stdio); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("stdio 传输配置 headers 应返回 CodeInvalid（而不是静默忽略），得到 %v", err)
	}
}

func TestCloneMCPConfigKeepsNilMaps(t *testing.T) {
	c := cloneMCPConfig(model.MCPServerConfig{Transport: model.MCPStdio, Command: "x"})
	if c.Env != nil || c.Headers != nil {
		t.Errorf("nil 映射应保持为 nil（JSON 中省略），得到 %+v", c)
	}
}

func TestRealMCPDialerSendsResolvedHeaders(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "auth-test", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "ping", Description: "ping"}, func(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong"}}}, nil, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var mu sync.Mutex
	var auths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer ts.Close()

	resolve := func(_ context.Context, ref string) (string, error) {
		if ref == "mcp/token" {
			return "Bearer tok-123", nil
		}
		return "", errors.New("unknown ref")
	}
	repo := newFakeRepo()
	m := New(repo, t.TempDir(), nil, NewMCPDialer(resolve, mcp.Options{HTTPClient: ts.Client()}))
	defer m.Close()
	ctx := context.Background()

	ext, err := m.RegisterMCP(ctx, "auth-svc", model.MCPServerConfig{
		Transport: model.MCPHTTP,
		URL:       ts.URL,
		Headers:   map[string]string{"Authorization": "secret:mcp/token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enable(ctx, ext.ID); err != nil {
		t.Fatalf("Headers 应随连接发送，服务端才会放行: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) == 0 || slices.Contains(auths, "") {
		t.Errorf("每个请求都应带解析后的 Authorization，得到 %q", auths)
	}
	rec, _ := repo.GetExtension(ctx, ext.ID)
	if rec != nil && strings.Contains(string(rec.Manifest), "tok-123") {
		t.Error("解析后的密钥不得写进记录")
	}
}

func TestPluginEnvUsesSharedAllowlist(t *testing.T) {
	parent := map[string]string{"OPENAI_API_KEY": "sk-leak"}
	for _, k := range config.ChildEnvAllowlist() {
		parent[k] = "v-" + k
	}
	lookup := func(k string) (string, bool) {
		v, ok := parent[k]
		return v, ok
	}
	ext := &model.Extension{ID: "ext1", InstallDir: "/data/extensions/ext1"}
	env := pluginEnv(ext, lookup)
	for _, k := range config.ChildEnvAllowlist() {
		if !slices.Contains(env, k+"=v-"+k) {
			t.Errorf("config.ChildEnvAllowlist 中的 %s 应透传给插件（含代理与证书变量）", k)
		}
	}
	for _, want := range []string{envExtensionID + "=ext1", envExtensionDir + "=/data/extensions/ext1", envPluginProtocol + "=" + PluginProtocolVersion} {
		if !slices.Contains(env, want) {
			t.Errorf("插件环境应包含 %q", want)
		}
	}
	if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "OPENAI_API_KEY=") }) {
		t.Errorf("白名单之外的变量不应透传: %v", env)
	}
}
