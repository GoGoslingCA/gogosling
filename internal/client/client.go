package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/version"
)

const (
	// DefaultTimeout 是普通请求的缺省超时（调用方 ctx 更早到期时以 ctx 为准）。
	DefaultTimeout = 30 * time.Second
	// uploadTimeout 是附件上传的超时。
	uploadTimeout = 10 * time.Minute
	// DefaultBackoffMin / DefaultBackoffMax 是事件流重连的退避区间（上限 5 秒）。
	DefaultBackoffMin = 250 * time.Millisecond
	DefaultBackoffMax = 5 * time.Second
	// DefaultIdleTimeout 是事件流的空闲看门狗：服务端每 15 秒发送保活，超过此时长没有任何字节即重连。
	DefaultIdleTimeout = 45 * time.Second
	// maxErrorBody 是读取错误响应体的上限。
	maxErrorBody = 1 << 20
)

// Client 是本地 API 客户端。创建后不可变，可并发使用。
type Client struct {
	endpoint    string
	token       string
	hc          *http.Client
	timeout     time.Duration
	locale      string
	userAgent   string
	backoffMin  time.Duration
	backoffMax  time.Duration
	idleTimeout time.Duration
}

// Option 配置 Client。
type Option func(*Client)

// WithHTTPClient 指定底层 HTTP 客户端（其 Timeout 应为 0，超时由本包按请求控制）。
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.hc = hc
		}
	}
}

// WithLocale 设置 Accept-Language（例如 "zh" 或 "en"），服务端据此本地化错误文案。
func WithLocale(locale string) Option {
	return func(c *Client) { c.locale = strings.TrimSpace(locale) }
}

// WithTimeout 设置普通请求的超时；0 表示只受调用方 ctx 约束。
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d >= 0 {
			c.timeout = d
		}
	}
}

// WithUserAgent 设置 User-Agent。
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// WithReconnectBackoff 设置事件流重连的退避区间；max 超过 5 秒时按 5 秒。
func WithReconnectBackoff(min, max time.Duration) Option {
	return func(c *Client) {
		if min > 0 {
			c.backoffMin = min
		}
		if max > 0 {
			c.backoffMax = max
		}
		if c.backoffMax > DefaultBackoffMax {
			c.backoffMax = DefaultBackoffMax
		}
		if c.backoffMin > c.backoffMax {
			c.backoffMin = c.backoffMax
		}
	}
}

// WithIdleTimeout 设置事件流空闲看门狗；0 表示关闭。
func WithIdleTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d >= 0 {
			c.idleTimeout = d
		}
	}
}

// New 创建客户端。endpoint 形如 "http://127.0.0.1:46750"（末尾斜杠会被去掉）。
func New(endpoint, token string, opts ...Option) *Client {
	c := &Client{
		endpoint:    strings.TrimRight(strings.TrimSpace(endpoint), "/"),
		token:       token,
		timeout:     DefaultTimeout,
		userAgent:   "gogosling-client/" + version.Version,
		backoffMin:  DefaultBackoffMin,
		backoffMax:  DefaultBackoffMax,
		idleTimeout: DefaultIdleTimeout,
	}
	for _, o := range opts {
		o(c)
	}
	if c.hc == nil {
		c.hc = &http.Client{Transport: newTransport()}
	}
	return c
}

// newTransport 返回不经过任何代理的传输：本地 API 只在回环地址上，令牌不应离开本机。
func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return t
}

// Endpoint 返回服务端地址。
func (c *Client) Endpoint() string { return c.endpoint }

// Discover 读取 <dataDir>/daemon.json 得到端点与令牌，并以 GET /healthz 确认守护进程存活。
// 句柄文件缺失、损坏或守护进程无响应都返回 CodeUnavailable。
func Discover(ctx context.Context, dataDir string, opts ...Option) (*Client, error) {
	h, err := identity.NewHandleFile(dataDir).Read(ctx)
	if err != nil {
		return nil, &model.Error{
			Code:    model.CodeUnavailable,
			Message: "守护进程未运行（找不到可用的 daemon.json）",
			Details: map[string]any{"reason": "daemon_not_running"},
			Err:     err,
		}
	}
	c := New(h.Endpoint, h.Token, opts...)
	if err := c.Health(ctx); err != nil {
		return nil, &model.Error{
			Code:    model.CodeUnavailable,
			Message: "守护进程没有响应",
			Details: map[string]any{"reason": "daemon_not_responding", "endpoint": c.endpoint},
			Err:     err,
		}
	}
	return c, nil
}

// request 描述一次 API 调用。
type request struct {
	method string
	path   string
	query  url.Values
	// body 为 nil 时不发送请求体；raw 非 nil 时原样发送（contentType 必填）。
	body        any
	raw         io.Reader
	contentType string
	timeout     time.Duration
	// noAuth 用于 /healthz。
	noAuth bool
}

// call 执行请求并把 2xx 响应体解码到 out（out 为 nil 时丢弃）；非 2xx 解析为 *model.Error。
func (c *Client) call(ctx context.Context, req request, out any) error {
	resp, cancel, err := c.send(ctx, req)
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr, _ := decodeErrorResponse(resp)
		return apiErr
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return nil
	}
	return decodeJSONBody(ctx, resp.Body, out)
}

// decodeJSONBody 解码 2xx 响应体；读取中途 ctx 结束时返回 cancelled / timeout。
func decodeJSONBody(ctx context.Context, body io.Reader, out any) error {
	if err := json.NewDecoder(body).Decode(out); err != nil {
		if ctxErr := ctxError(ctx, err); ctxErr != nil {
			return ctxErr
		}
		return model.Wrap(model.CodeInternal, "无法解析服务端响应", err)
	}
	return nil
}

// send 发出请求并返回响应；调用方负责关闭响应体并调用 cancel。
func (c *Client) send(ctx context.Context, req request) (*http.Response, context.CancelFunc, error) {
	timeout := c.timeout
	if req.timeout > 0 {
		timeout = req.timeout
	}
	cancel := context.CancelFunc(func() {})
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	u := c.endpoint + req.path
	if len(req.query) > 0 {
		u += "?" + req.query.Encode()
	}
	var body io.Reader
	contentType := req.contentType
	switch {
	case req.raw != nil:
		body = req.raw
	case req.body != nil:
		data, err := json.Marshal(req.body)
		if err != nil {
			cancel()
			return nil, nil, model.Wrap(model.CodeInvalid, "无法编码请求体", err)
		}
		body = bytes.NewReader(data)
		contentType = "application/json"
	}
	hreq, err := http.NewRequestWithContext(ctx, req.method, u, body)
	if err != nil {
		cancel()
		return nil, nil, model.Wrap(model.CodeInvalid, "无法构造请求", err)
	}
	c.decorate(hreq, req.noAuth)
	if contentType != "" {
		hreq.Header.Set("Content-Type", contentType)
	}
	hreq.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(hreq)
	if err != nil {
		// 先按 ctx 状态分类再释放：cancel 之后 ctx.Err() 恒为 Canceled，会把连接失败误判为取消。
		terr := transportError(ctx, err)
		cancel()
		return nil, nil, terr
	}
	return resp, cancel, nil
}

// decorate 设置鉴权、语言与 User-Agent 头。
func (c *Client) decorate(req *http.Request, noAuth bool) {
	if !noAuth && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.locale != "" {
		req.Header.Set("Accept-Language", c.locale)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
}

// transportError 把无法得到响应的错误分类：调用方取消 / 超时，或连接失败（unavailable）。
func transportError(ctx context.Context, err error) error {
	if ce := ctxError(ctx, err); ce != nil {
		return ce
	}
	return &model.Error{
		Code:    model.CodeUnavailable,
		Message: "无法连接守护进程",
		Details: map[string]any{"reason": "network"},
		Err:     err,
	}
}

// ctxError 在 ctx 已结束时返回对应的 cancelled / timeout 错误，否则返回 nil。
func ctxError(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return model.Wrap(model.CodeCancelled, "请求被取消", errors.Join(ctx.Err(), err))
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return model.Wrap(model.CodeTimeout, "请求超时", errors.Join(ctx.Err(), err))
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return model.Wrap(model.CodeTimeout, "请求超时", err)
	}
	return nil
}

// wireError 是错误信封的解码目标。
type wireError struct {
	Error *struct {
		Code    model.Code     `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
	Run *model.Run `json:"run"`
}

// decodeErrorResponse 把非 2xx 响应解析为 *model.Error；信封里带 run 时一并返回。
// 不是信封的响应（代理页面、截断的响应）按 HTTP 状态码推断错误码。
func decodeErrorResponse(resp *http.Response) (*model.Error, *model.Run) {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var w wireError
	if json.Unmarshal(data, &w) == nil && w.Error != nil && w.Error.Code != "" {
		return &model.Error{Code: w.Error.Code, Message: w.Error.Message, Details: w.Error.Details}, w.Run
	}
	return &model.Error{
		Code:    codeFromStatus(resp.StatusCode),
		Message: http.StatusText(resp.StatusCode),
		Details: map[string]any{"status": resp.StatusCode},
	}, nil
}

// codeFromStatus 是 HTTP 状态码到错误码的反向映射（只用于非信封响应）。
func codeFromStatus(status int) model.Code {
	switch {
	case status == http.StatusBadRequest, status == http.StatusMethodNotAllowed,
		status == http.StatusRequestEntityTooLarge, status == http.StatusUnprocessableEntity:
		return model.CodeInvalid
	case status == http.StatusUnauthorized:
		return model.CodeUnauthorized
	case status == http.StatusForbidden:
		return model.CodeForbidden
	case status == http.StatusNotFound:
		return model.CodeNotFound
	case status == http.StatusConflict:
		return model.CodeConflict
	case status == http.StatusGatewayTimeout, status == http.StatusRequestTimeout:
		return model.CodeTimeout
	case status == http.StatusBadGateway, status == http.StatusServiceUnavailable, status == http.StatusTooManyRequests:
		return model.CodeUnavailable
	default:
		return model.CodeInternal
	}
}
