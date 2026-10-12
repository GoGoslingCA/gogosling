package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const (
	// DefaultCallTimeout 是单次 ListTools / CallTool 的默认超时。
	DefaultCallTimeout = 60 * time.Second
	// DefaultConnectTimeout 是建立连接（含 initialize 握手）的默认超时。
	DefaultConnectTimeout = 30 * time.Second
	// DefaultMaxResultBytes 是工具结果文本的默认上限，超出部分截断并注明。
	DefaultMaxResultBytes = 1 << 20

	// SourcePrefix 是 MCP 工具在 model.ToolSpec.Source 中的前缀。
	SourcePrefix = "mcp:"

	clientName = "gogosling"
	// maxStderrLine 是子进程 stderr 单行进入日志的最大字节数。
	maxStderrLine = 4096
)

// Options 是连接选项；零值字段取默认值。
type Options struct {
	// CallTimeout 是每次请求的超时；0 表示 DefaultCallTimeout。
	CallTimeout time.Duration
	// ConnectTimeout 是建立连接与握手的超时；0 表示 DefaultConnectTimeout。
	ConnectTimeout time.Duration
	// MaxResultBytes 是工具结果文本的上限；0 表示 DefaultMaxResultBytes。
	MaxResultBytes int
	// WorkDir 是 stdio 子进程的工作目录；为空时继承守护进程的当前目录。
	WorkDir string
	// HTTPClient 用于 http 传输；nil 时使用 http.DefaultClient。配置了 Headers 时本包会在它的
	// 副本上包一层注入请求头的 RoundTripper，不修改调用方传入的 Client。
	HTTPClient *http.Client
	// Logger 为 nil 时丢弃日志。
	Logger *slog.Logger
	// Version 是向服务端通告的客户端版本。
	Version string
	// LookupEnv 用于读取白名单环境变量，nil 时使用 os.LookupEnv（测试用）。
	LookupEnv func(string) (string, bool)
}

func (o Options) withDefaults() Options {
	if o.CallTimeout <= 0 {
		o.CallTimeout = DefaultCallTimeout
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = DefaultConnectTimeout
	}
	if o.MaxResultBytes <= 0 {
		o.MaxResultBytes = DefaultMaxResultBytes
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	return o
}

// Client 是与一个 MCP 服务的活动连接。并发安全：方法可以被多个 goroutine 同时调用。
type Client struct {
	extID   string
	opts    Options
	log     *slog.Logger
	session *sdk.ClientSession
	cmd     *exec.Cmd

	done     chan struct{}
	doneOnce sync.Once
	closeMu  sync.Mutex
	closed   bool
	closeErr error
}

// Connect 按配置建立连接并完成握手。
//
// extensionID 用于标记工具来源（Source 为 "mcp:<extensionID>"）。resolve 用于解析 Env 与 Headers 中的
// "secret:<ref>"，没有密钥引用时可为 nil。http 传输的 Headers 由自定义 RoundTripper 注入每个发往
// 该端点（同 scheme 与 host）的请求，解析结果不进日志。ctx 只约束连接过程；连接建立后的生命周期
// 由 Close 或服务端断开决定。
func Connect(ctx context.Context, extensionID string, cfg model.MCPServerConfig, resolve EnvResolver, opts Options) (*Client, error) {
	opts = opts.withDefaults()
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	log := opts.Logger.With("extension_id", extensionID, "transport", string(cfg.Transport))

	switch cfg.Transport {
	case model.MCPStdio:
		env, err := buildEnv(ctx, cfg.Env, resolve, opts.LookupEnv)
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = env
		cmd.Dir = opts.WorkDir
		cmd.Stderr = &lineLogger{log: log}
		configureCmd(cmd)
		return connect(ctx, extensionID, &sdk.CommandTransport{Command: cmd}, cmd, opts, log)
	case model.MCPHTTP:
		endpoint := strings.TrimSpace(cfg.URL)
		headers, err := resolveHeaders(ctx, cfg.Headers, resolve)
		if err != nil {
			return nil, err
		}
		httpClient, err := withHeaders(opts.HTTPClient, endpoint, headers)
		if err != nil {
			return nil, err
		}
		if len(headers) > 0 {
			// 只记录键名；值可能是令牌。
			log.Debug("mcp http 附加请求头", "headers", headerNames(headers))
		}
		t := &sdk.StreamableClientTransport{
			Endpoint:   endpoint,
			HTTPClient: httpClient,
			MaxRetries: 2,
			// 本包不处理服务端主动通知（工具列表变化靠摘要比对发现），不维持常驻 SSE 流。
			DisableStandaloneSSE: true,
		}
		return connect(ctx, extensionID, t, nil, opts, log)
	default:
		return nil, model.Errorf(model.CodeInvalid, "不支持的 MCP 传输方式 %q", cfg.Transport)
	}
}

// connect 用任意传输建立会话；测试用它接入进程内的内存传输。
func connect(ctx context.Context, extensionID string, t sdk.Transport, cmd *exec.Cmd, opts Options, log *slog.Logger) (*Client, error) {
	opts = opts.withDefaults()
	if log == nil {
		log = opts.Logger.With("extension_id", extensionID)
	}
	cctx, cancel := context.WithTimeout(ctx, opts.ConnectTimeout)
	defer cancel()

	client := sdk.NewClient(
		&sdk.Implementation{Name: clientName, Version: opts.Version},
		// 不通告 roots / sampling / elicitation：本宿主不向服务端暴露文件系统，也不代为调用模型。
		&sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}},
	)
	session, err := client.Connect(cctx, t, nil)
	if err != nil {
		if cmd != nil {
			killTree(cmd.Process)
		}
		code := model.CodeUnavailable
		if cctx.Err() != nil && ctx.Err() == nil {
			code = model.CodeTimeout
		} else if ctx.Err() != nil {
			code = model.CodeCancelled
		}
		return nil, model.Wrap(code, "连接 MCP 服务失败", err)
	}

	c := &Client{
		extID:   extensionID,
		opts:    opts,
		log:     log,
		session: session,
		cmd:     cmd,
		done:    make(chan struct{}),
	}
	go func() {
		_ = session.Wait()
		c.doneOnce.Do(func() { close(c.done) })
	}()
	name, version := c.ServerInfo()
	log.Info("mcp 已连接", "server_name", name, "server_version", version)
	return c, nil
}

// ExtensionID 返回连接所属的扩展 ID。
func (c *Client) ExtensionID() string { return c.extID }

// Source 返回该连接提供的工具的来源标记。
func (c *Client) Source() model.ToolSource { return model.ToolSource(SourcePrefix + c.extID) }

// ServerInfo 返回服务端在握手时通告的名称与版本。
func (c *Client) ServerInfo() (name, version string) {
	if res := c.session.InitializeResult(); res != nil && res.ServerInfo != nil {
		return res.ServerInfo.Name, res.ServerInfo.Version
	}
	return "", ""
}

// Done 在连接结束（主动关闭或服务端断开）后关闭；调用方用它判断是否需要重连。
func (c *Client) Done() <-chan struct{} { return c.done }

// ListTools 列出服务端当前的全部工具（自动翻页），转换为 model.ToolSpec。
// 描述与参数 schema 原样保留；Source 为 "mcp:<extensionID>"；Risk 默认 network。
func (c *Client) ListTools(ctx context.Context) ([]model.ToolSpec, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.opts.CallTimeout)
	defer cancel()

	var specs []model.ToolSpec
	for tool, err := range c.session.Tools(callCtx, nil) {
		if err != nil {
			return nil, c.mapErr(ctx, callCtx, "tools/list", err)
		}
		spec, ok := c.toSpec(tool)
		if !ok {
			continue
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// ListToolsPinned 列出工具并与 pinned（首次启用时记录的摘要）比对。
// pinned 为空表示尚未固定；changed 为 true 表示工具的名称、描述或 schema 与固定时不同，
// 调用方应当拒绝使用这些工具直到用户重新确认（防止服务端在用户不知情时改写工具说明）。
func (c *Client) ListToolsPinned(ctx context.Context, pinned string) (specs []model.ToolSpec, digest string, changed bool, err error) {
	specs, err = c.ListTools(ctx)
	if err != nil {
		return nil, "", false, err
	}
	digest = ToolsDigest(specs)
	changed = pinned != "" && pinned != digest
	if changed {
		c.log.Warn("mcp 工具声明与固定时不同，已拒绝使用，需要重新确认",
			"pinned_digest", pinned, "current_digest", digest, "tool_count", len(specs))
	}
	return specs, digest, changed, nil
}

// toSpec 把 MCP 工具声明转换为 model.ToolSpec；名称为空的工具被跳过。
func (c *Client) toSpec(t *sdk.Tool) (model.ToolSpec, bool) {
	if t == nil || strings.TrimSpace(t.Name) == "" {
		c.log.Warn("mcp 服务返回了没有名称的工具，已跳过")
		return model.ToolSpec{}, false
	}
	schema := json.RawMessage(`{"type":"object"}`)
	if t.InputSchema != nil {
		if raw, err := json.Marshal(t.InputSchema); err == nil && !bytes.Equal(raw, []byte("null")) {
			schema = raw
		}
	}
	risk := model.RiskNetwork
	// 注解来自不可信的服务端，只允许据此收紧风险等级，不允许放宽。
	if a := t.Annotations; a != nil && !a.ReadOnlyHint && a.DestructiveHint != nil && *a.DestructiveHint {
		risk = model.RiskDestructive
	}
	return model.ToolSpec{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: schema,
		Source:      c.Source(),
		Risk:        risk,
	}, true
}

// CallTool 调用工具。args 必须是 JSON 对象（空表示无参数）。
//
// 文本内容按顺序拼接；图片、音频、资源等非文本内容以占位说明代替；结果超过 MaxResultBytes 时截断。
// 服务端以 isError 报告的工具错误体现在 ToolResult.IsError 上而不是 error；协议层失败才返回 error。
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (model.ToolResult, error) {
	if strings.TrimSpace(name) == "" {
		return model.ToolResult{}, model.NewError(model.CodeInvalid, "工具名不能为空")
	}
	var arguments any = map[string]any{}
	if trimmed := bytes.TrimSpace(args); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if !json.Valid(trimmed) || trimmed[0] != '{' {
			return model.ToolResult{}, model.NewError(model.CodeInvalid, "工具参数必须是 JSON 对象")
		}
		arguments = json.RawMessage(trimmed)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.opts.CallTimeout)
	defer cancel()
	res, err := c.session.CallTool(callCtx, &sdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return model.ToolResult{}, c.mapErr(ctx, callCtx, "tools/call "+name, err)
	}
	content, truncated := renderResult(res, c.opts.MaxResultBytes)
	if truncated {
		c.log.Warn("mcp 工具结果超过上限，已截断", "tool", name, "limit_bytes", c.opts.MaxResultBytes)
	}
	return model.ToolResult{Content: content, IsError: res.IsError}, nil
}

// Close 关闭连接；stdio 传输会先关闭子进程的 stdin 等待其退出，必要时发送 SIGTERM / SIGKILL，
// 最后结束整个进程组。可重复调用。
func (c *Client) Close() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closed {
		return c.closeErr
	}
	c.closed = true
	err := c.session.Close()
	if c.cmd != nil {
		killTree(c.cmd.Process)
	}
	c.doneOnce.Do(func() { close(c.done) })
	if err != nil && !errors.Is(err, sdk.ErrConnectionClosed) {
		c.closeErr = model.Wrap(model.CodeInternal, "关闭 MCP 连接失败", err)
	}
	return c.closeErr
}

// mapErr 把 go-sdk 的错误归一化为带 model.Code 的错误。
func (c *Client) mapErr(ctx, callCtx context.Context, op string, err error) error {
	switch {
	case ctx.Err() != nil:
		return model.Wrap(model.CodeCancelled, op+" 已取消", err)
	case callCtx.Err() != nil:
		return model.Wrap(model.CodeTimeout, fmt.Sprintf("%s 超过 %s 未返回", op, c.opts.CallTimeout), err)
	case errors.Is(err, sdk.ErrConnectionClosed):
		return model.Wrap(model.CodeUnavailable, op+" 失败：连接已关闭", err)
	}
	var werr *jsonrpc.Error
	if errors.As(err, &werr) {
		switch werr.Code {
		case jsonrpc.CodeInvalidParams, jsonrpc.CodeMethodNotFound:
			return model.Wrap(model.CodeInvalid, op+" 被服务端拒绝", err)
		default:
			return model.Wrap(model.CodeInternal, op+" 服务端返回错误", err)
		}
	}
	return model.Wrap(model.CodeUnavailable, op+" 失败", err)
}

// renderResult 把调用结果渲染为单段文本；返回是否发生截断。
func renderResult(res *sdk.CallToolResult, maxBytes int) (string, bool) {
	if res == nil {
		return "", false
	}
	var parts []string
	for _, item := range res.Content {
		if s := renderContent(item); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(raw))
		}
	}
	text := strings.Join(parts, "\n")
	if len(text) <= maxBytes {
		return text, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	omitted := len(text) - cut
	return text[:cut] + fmt.Sprintf("\n[... output truncated: %d bytes omitted]", omitted), true
}

// renderContent 渲染单个内容块；非文本内容给出面向模型的占位说明（不是用户界面文案）。
func renderContent(item sdk.Content) string {
	switch v := item.(type) {
	case *sdk.TextContent:
		return v.Text
	case *sdk.ImageContent:
		return fmt.Sprintf("[%s image, %d bytes omitted]", orUnknown(v.MIMEType), len(v.Data))
	case *sdk.AudioContent:
		return fmt.Sprintf("[%s audio, %d bytes omitted]", orUnknown(v.MIMEType), len(v.Data))
	case *sdk.ResourceLink:
		return fmt.Sprintf("[resource link: %s (%s)]", v.URI, orUnknown(v.MIMEType))
	case *sdk.EmbeddedResource:
		if v.Resource == nil {
			return "[embedded resource omitted]"
		}
		if v.Resource.Text != "" {
			return v.Resource.Text
		}
		return fmt.Sprintf("[resource %s (%s), %d bytes omitted]", v.Resource.URI, orUnknown(v.Resource.MIMEType), len(v.Resource.Blob))
	default:
		return "[unsupported content omitted]"
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// lineLogger 把子进程的 stderr 按行写入日志（debug 级别），超长行截断。
type lineLogger struct {
	log *slog.Logger
	mu  sync.Mutex
	buf []byte
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.emit(l.buf[:i])
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > maxStderrLine {
		l.emit(l.buf)
		l.buf = nil
	}
	return len(p), nil
}

func (l *lineLogger) emit(line []byte) {
	line = bytes.TrimRight(line, "\r")
	if len(line) == 0 {
		return
	}
	if len(line) > maxStderrLine {
		line = line[:maxStderrLine]
	}
	l.log.Debug("mcp stderr", "line", string(line))
}
