package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// builtinOrigin 是桌面壳自定义协议的 Origin，总是放行。
const builtinOrigin = "app://gogosling"

type ctxKey int

const infoKey ctxKey = iota

// reqInfo 在中间件之间传递每个请求的元数据：鉴权得到的身份 ID（供请求日志）与解析出的语言。
// 由请求日志中间件创建；只在处理该请求的 goroutine 链上读写。
type reqInfo struct {
	principal string
	locale    string
}

func infoFrom(ctx context.Context) *reqInfo {
	info, _ := ctx.Value(infoKey).(*reqInfo)
	return info
}

// chain 按固定顺序（由外到内）套上中间件：
// panic 恢复 → 请求日志 → Host → Origin → 鉴权 → 语言 → 请求体上限 → 读写期限 → inner。
func (s *Server) chain(inner http.Handler) http.Handler {
	h := s.withDeadlines(inner)
	h = s.withBodyLimit(h)
	h = s.withLocale(h)
	h = s.withAuth(h)
	h = s.withOrigin(h)
	h = s.withHost(h)
	h = s.withRequestLog(h)
	return s.withRecover(h)
}

// statusRecorder 记录状态码与写出字节数；通过 Unwrap 让 http.ResponseController 找到底层连接能力。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

// Flush 让事件流在包装之后仍可刷新。
func (r *statusRecorder) Flush() { _ = r.FlushError() }

// FlushError 供 http.ResponseController 使用：把底层刷新失败（客户端已断开）传回事件流处理器。
func (r *statusRecorder) FlushError() error {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return http.NewResponseController(r.ResponseWriter).Flush()
}

// Unwrap 供 http.ResponseController 访问底层 ResponseWriter。
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// withRecover 是最外层：把处理器 panic 转成 500 信封（响应头尚未写出时），否则中止连接。
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, ok := w.(*statusRecorder)
		if !ok {
			rec = &statusRecorder{ResponseWriter: w}
		}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				// 与标准库约定的哨兵值：原样抛给 net/http 静默断开连接。
				panic(v)
			}
			s.log.Error("处理请求时发生 panic",
				"method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			if rec.wroteHeader {
				// 响应已经开始，无法再改写状态码；让 net/http 静默断开连接。
				panic(http.ErrAbortHandler)
			}
			s.writeError(rec, r, model.NewError(model.CodeInternal, "处理请求时发生 panic"))
		}()
		next.ServeHTTP(rec, r)
	})
}

// withRequestLog 记录方法、路径、状态、耗时与身份 ID；不记录令牌、查询串与请求体。/healthz 不记录。
func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := &reqInfo{}
		r = r.WithContext(context.WithValue(r.Context(), infoKey, info))
		rec, ok := w.(*statusRecorder)
		if !ok {
			rec = &statusRecorder{ResponseWriter: w}
		}
		start := time.Now()
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" {
			return
		}
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
			"bytes", rec.bytes,
		}
		if info.principal != "" {
			attrs = append(attrs, "principal_id", info.principal)
		}
		s.log.Info("http 请求", attrs...)
	})
}

// withHost 只接受 Host 为回环 IP 字面量（127.0.0.0/8、[::1]）或 localhost（任意端口）的请求，挡住 DNS 重绑定。
func (s *Server) withHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHost(r.Host) {
			s.writeError(w, r, &model.Error{
				Code:    model.CodeForbidden,
				Message: "Host 不在允许范围内",
				Details: map[string]any{"reason": "host_not_allowed"},
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowedHost 判断 Host 头是否是回环主机加可选端口。规则与监听地址相同（config.IsLoopbackAddr），
// 因此监听在 127.0.0.2 之类地址上时客户端照样可用；IP 字面量不经 DNS，不存在重绑定问题，
// 主机名只接受 localhost（不接受 localhost. 或任何子域名）。
func allowedHost(hostport string) bool {
	if hostport == "" {
		return false
	}
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if host == "" {
		return false
	}
	return config.IsLoopbackAddr(net.JoinHostPort(host, "0"))
}

// withOrigin：无 Origin（非浏览器客户端）、桌面壳协议与白名单放行，其余 403。
// 不发送任何 CORS 允许头：浏览器的跨源请求即使到达处理器，结果也无法被页面读取。
func (s *Server) withOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || s.originAllowed(origin) {
			next.ServeHTTP(w, r)
			return
		}
		s.writeError(w, r, &model.Error{
			Code:    model.CodeForbidden,
			Message: "Origin 不在允许范围内",
			Details: map[string]any{"reason": "origin_not_allowed"},
		})
	})
}

func (s *Server) originAllowed(origin string) bool {
	if strings.EqualFold(origin, builtinOrigin) {
		return true
	}
	norm, err := normalizeOrigin(origin)
	if err != nil {
		return false
	}
	if _, ok := s.staticOrigins[norm]; ok {
		return true
	}
	return s.settings.originAllowed(norm)
}

// normalizeOrigin 把 http(s) Origin 规范化为 "scheme://host[:port]"（小写）；
// 拒绝其他协议、空主机、路径、查询串、片段与用户信息。
func normalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme != "http" && scheme != "https":
		return "", fmt.Errorf("origin 必须是 http 或 https: %q", raw)
	case u.Host == "" || u.Hostname() == "":
		return "", fmt.Errorf("origin 缺少主机: %q", raw)
	case u.User != nil:
		return "", fmt.Errorf("origin 不能包含用户信息: %q", raw)
	case u.Path != "" && u.Path != "/":
		return "", fmt.Errorf("origin 不能包含路径: %q", raw)
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "", fmt.Errorf("origin 不能包含查询串或片段: %q", raw)
	}
	return scheme + "://" + strings.ToLower(u.Host), nil
}

// withAuth 要求除 GET /healthz 外的所有请求携带有效的 Bearer 令牌，并把 Principal 放入 ctx。
// 校验后从请求中删除 Authorization 头，避免后续任何环节意外记录它。
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHealthz(r) {
			next.ServeHTTP(w, r)
			return
		}
		p, err := s.deps.Auth.Authenticate(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			if !model.IsCode(err, model.CodeUnauthorized) {
				err = model.Wrap(model.CodeUnauthorized, "鉴权失败", err)
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="gogosling"`)
			s.writeError(w, r, err)
			return
		}
		r.Header.Del("Authorization")
		if info := infoFrom(r.Context()); info != nil {
			info.principal = p.ID
		}
		next.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), p)))
	})
}

func isHealthz(r *http.Request) bool {
	return r.URL.Path == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead)
}

// withLocale 解析请求语言并放入 ctx：Accept-Language > 设置项 locale > Options.DefaultLocale > en。
func (s *Server) withLocale(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loc := s.resolveLocale(r)
		if info := infoFrom(r.Context()); info != nil {
			info.locale = loc
		}
		next.ServeHTTP(w, r)
	})
}

// resolveLocale 判定错误文案与事件文案的语言（与 OpenAPI「message 已按 Accept-Language 本地化」一致）：
// 请求的 Accept-Language 给出受支持的语言时以它为准；否则用设置项 locale；
// 再否则用组合根解析的 Options.DefaultLocale；都没有时用 i18n.DefaultLocale。
func (s *Server) resolveLocale(r *http.Request) string {
	if loc, ok := i18n.FromAcceptLanguage(r.Header.Get("Accept-Language")); ok {
		return loc
	}
	if loc, ok := i18n.Normalize(s.settings.locale()); ok {
		return loc
	}
	return s.fallbackLocale()
}

// fallbackLocale 是设置项未保存时内核实际使用的语言：Options.DefaultLocale（规范化后）> i18n.DefaultLocale。
// GET /settings 的 effective_locale 在设置项为空时也取它。
func (s *Server) fallbackLocale() string {
	if loc, ok := i18n.Normalize(s.opts.DefaultLocale); ok {
		return loc
	}
	return i18n.DefaultLocale
}

// localeOf 返回请求的语言；语言中间件之前的拒绝（Host/Origin/鉴权）也能得到本地化文案。
func (s *Server) localeOf(r *http.Request) string {
	if info := infoFrom(r.Context()); info != nil && info.locale != "" {
		return info.locale
	}
	return s.resolveLocale(r)
}

// withBodyLimit 给请求体套上上限：附件上传用 MaxUploadBytes（加 multipart 开销），其余用 MaxBodyBytes。
// 声明的 Content-Length 已超限时直接拒绝，不读取请求体。
func (s *Server) withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			limit := s.opts.MaxBodyBytes
			if isUpload(r) {
				limit = s.opts.MaxUploadBytes + multipartOverhead
			}
			if r.ContentLength > limit {
				s.writeError(w, r, bodyTooLarge(limit, nil))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// withDeadlines 为普通请求设置读写期限（上传更长）；事件流不设期限，由处理器自己管理每次写出。
func (s *Server) withDeadlines(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isEvents(r) {
			d := requestTimeout
			if isUpload(r) {
				d = uploadTimeout
			}
			rc := http.NewResponseController(w)
			deadline := time.Now().Add(d)
			_ = rc.SetReadDeadline(deadline)
			_ = rc.SetWriteDeadline(deadline)
		}
		next.ServeHTTP(w, r)
	})
}

// isUpload 判断是否是 POST /api/v1/sessions/{id}/attachments。
func isUpload(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/sessions/")
	if !ok {
		project, found := strings.CutPrefix(r.URL.Path, "/api/v1/projects/")
		id, tail, split := strings.Cut(project, "/")
		return found && split && id != "" && tail == "materials"
	}
	id, tail, ok := strings.Cut(rest, "/")
	return ok && id != "" && tail == "attachments"
}

func isEvents(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == "/api/v1/events"
}
