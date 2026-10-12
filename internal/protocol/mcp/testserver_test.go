package mcp

// 本文件提供测试用的进程内 MCP 服务：既可以通过内存传输直接连接，也可以在 TestMain 发现
// GOGOSLING_TEST_MCP_SERVER=1 时让测试二进制自我重执行、以 stdio 充当真实的子进程服务。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const testServerEnv = "GOGOSLING_TEST_MCP_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(testServerEnv) == "1" {
		server := newTestServer(testServerSpec{})
		if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
			fmt.Fprintln(os.Stderr, "test mcp server:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// testServerSpec 控制测试服务的行为。
type testServerSpec struct {
	// EchoDescription 覆盖 echo 工具的描述，用于模拟服务端改写工具说明。
	EchoDescription string
	// ExtraTools 追加的工具名（返回固定文本）。
	ExtraTools []string
}

type echoArgs struct {
	Text string `json:"text"`
}

type envArgs struct {
	Name string `json:"name"`
}

type sleepArgs struct {
	Millis int `json:"millis"`
}

type bigArgs struct {
	Bytes int `json:"bytes"`
}

func text(s string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}}
}

// newTestServer 构造带固定工具集的服务：echo、env、sleep、fail、multi、big、destructive、structured。
func newTestServer(spec testServerSpec) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "gogosling-test-server", Version: "0.0.1"}, nil)

	desc := "Echo the given text back"
	if spec.EchoDescription != "" {
		desc = spec.EchoDescription
	}
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: desc},
		func(_ context.Context, _ *sdk.CallToolRequest, in echoArgs) (*sdk.CallToolResult, any, error) {
			return text("echo:" + in.Text), nil, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: "env", Description: "Return the value of an environment variable"},
		func(_ context.Context, _ *sdk.CallToolRequest, in envArgs) (*sdk.CallToolResult, any, error) {
			v, ok := os.LookupEnv(in.Name)
			if !ok {
				return text("<unset>"), nil, nil
			}
			return text(v), nil, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: "sleep", Description: "Sleep for the given milliseconds"},
		func(ctx context.Context, _ *sdk.CallToolRequest, in sleepArgs) (*sdk.CallToolResult, any, error) {
			select {
			case <-time.After(time.Duration(in.Millis) * time.Millisecond):
				return text("slept"), nil, nil
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		})
	sdk.AddTool(server, &sdk.Tool{Name: "fail", Description: "Always reports a tool error"},
		func(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "boom"}}}, nil, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: "multi", Description: "Mixed content types"},
		func(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{
				&sdk.TextContent{Text: "first"},
				&sdk.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3, 4}},
				&sdk.ResourceLink{URI: "file:///tmp/a.txt", MIMEType: "text/plain", Name: "a"},
				&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "mem://x", MIMEType: "text/plain", Text: "embedded"}},
				&sdk.TextContent{Text: "last"},
			}}, nil, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: "big", Description: "Return a large payload"},
		func(_ context.Context, _ *sdk.CallToolRequest, in bigArgs) (*sdk.CallToolResult, any, error) {
			return text(strings.Repeat("x", in.Bytes)), nil, nil
		})
	destructive := true
	server.AddTool(&sdk.Tool{
		Name:        "destructive",
		Description: "Deletes things",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"b":{"type":"string"},"a":{"type":"integer"}}}`),
		Annotations: &sdk.ToolAnnotations{DestructiveHint: &destructive},
	}, func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return text("deleted"), nil
	})
	server.AddTool(&sdk.Tool{
		Name:        "structured",
		Description: "Only structured content",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{StructuredContent: map[string]any{"answer": 42}}, nil
	})
	for _, name := range spec.ExtraTools {
		server.AddTool(&sdk.Tool{Name: name, Description: "extra " + name, InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return text("extra"), nil
			})
	}
	return server
}

// connectInMemory 在进程内用内存传输连接一个测试服务，返回客户端；连接随测试结束关闭。
func connectInMemory(t *testing.T, spec testServerSpec, opts Options) *Client {
	t.Helper()
	serverT, clientT := sdk.NewInMemoryTransports()
	server := newTestServer(spec)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("启动测试服务失败: %v", err)
	}
	c, err := connect(context.Background(), "ext-1", clientT, nil, opts, nil)
	if err != nil {
		t.Fatalf("连接测试服务失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
