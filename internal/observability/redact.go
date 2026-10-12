package observability

import (
	"context"
	"log/slog"
	"strings"
	"unicode"
)

// RedactedValue 是脱敏后替换敏感属性值的占位文本。
const RedactedValue = "[redacted]"

// alwaysSensitive 是出现在键名任意位置都视为凭据的片段。
var alwaysSensitive = map[string]struct{}{
	"password":      {},
	"passwd":        {},
	"secret":        {},
	"secrets":       {},
	"authorization": {},
	"credential":    {},
	"credentials":   {},
	"apikey":        {},
	"cookie":        {},
}

// keyQualifiers 是紧接在 "key" 之前时表示凭据的片段（api_key、private_key、secret_key 等）。
var keyQualifiers = map[string]struct{}{
	"api":     {},
	"private": {},
	"secret":  {},
	"access":  {},
	"signing": {},
}

// IsSensitiveKey 判断属性键名是否表示凭据。
//
// 规则（键名先转为小写并按 "_"、"."、"-" 切分为片段）：
//   - 任一片段是 password / passwd / secret(s) / authorization / credential(s) / apikey / cookie；
//   - "key" 紧跟在 api / private / secret / access / signing 之后（api_key、gen_ai.api.key）；
//   - 最后一个片段是 token（access_token、refresh_token、token）。
//     "token" 出现在中间（token_count、token_usage）不算，
//     "tokens"（input_tokens、max_tokens）是计数而不是凭据。
func IsSensitiveKey(key string) bool {
	segs := splitKey(strings.ToLower(key))
	for i, s := range segs {
		if _, ok := alwaysSensitive[s]; ok {
			return true
		}
		if s == "key" && i > 0 {
			if _, ok := keyQualifiers[segs[i-1]]; ok {
				return true
			}
		}
		if s == "token" && i == len(segs)-1 {
			return true
		}
	}
	return false
}

func splitKey(key string) []string {
	return strings.FieldsFunc(key, func(r rune) bool {
		return r == '_' || r == '.' || r == '-' || r == ' ' || r == '/'
	})
}

// SnakeKey 把 camelCase / PascalCase 键名转为 snake_case，并把空格与连字符替换为下划线。
// 已经是 snake_case 的键名原样返回（不分配内存）。
func SnakeKey(key string) string {
	needs := false
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c >= 'A' && c <= 'Z' || c == '-' || c == ' ' {
			needs = true
			break
		}
	}
	if !needs {
		return key
	}
	runes := []rune(key)
	var b strings.Builder
	b.Grow(len(key) + 4)
	for i, r := range runes {
		switch {
		case r == '-' || r == ' ':
			b.WriteByte('_')
		case unicode.IsUpper(r):
			if i > 0 {
				prev := runes[i-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					b.WriteByte('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RedactAttr 返回脱敏并规范化键名后的属性：
//   - 键名转为 snake_case；
//   - 键名敏感且值是字符串或任意对象（KindString / KindAny）时，值替换为 RedactedValue；
//     数值、布尔、时间、时长不会是凭据，保持原样（因此 gen_ai.usage.input_tokens 这类计数不受影响）；
//   - 分组（KindGroup）递归处理；分组本身的键名敏感时整组替换为一个字符串。
func RedactAttr(a slog.Attr) slog.Attr {
	a.Key = SnakeKey(a.Key)
	v := a.Value.Resolve()
	sensitive := IsSensitiveKey(a.Key)
	if v.Kind() == slog.KindGroup {
		if sensitive {
			return slog.String(a.Key, RedactedValue)
		}
		members := v.Group()
		out := make([]slog.Attr, len(members))
		for i, m := range members {
			out[i] = RedactAttr(m)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	if sensitive && (v.Kind() == slog.KindString || v.Kind() == slog.KindAny) {
		return slog.String(a.Key, RedactedValue)
	}
	return slog.Attr{Key: a.Key, Value: v}
}

// redactHandler 在把记录交给内层 Handler 之前做脱敏与键名规范化。
type redactHandler struct {
	inner slog.Handler
}

// NewRedactingHandler 包装 inner：每条记录的属性（包括 WithAttrs 预置的属性）都经过 RedactAttr。
// 返回的 Handler 与 inner 一样可并发使用。
func NewRedactingHandler(inner slog.Handler) slog.Handler {
	if _, ok := inner.(*redactHandler); ok {
		return inner
	}
	return &redactHandler{inner: inner}
}

func (h *redactHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(RedactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = RedactAttr(a)
	}
	return &redactHandler{inner: h.inner.WithAttrs(out)}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &redactHandler{inner: h.inner.WithGroup(SnakeKey(name))}
}
