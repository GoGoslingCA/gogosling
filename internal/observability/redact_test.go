package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestIsSensitiveKey(t *testing.T) {
	cases := map[string]bool{
		"api_key":                   true,
		"apiKey":                    true,
		"gen_ai.api.key":            true,
		"private_key":               true,
		"token":                     true,
		"access_token":              true,
		"refresh_token":             true,
		"Authorization":             true,
		"password":                  true,
		"db_password":               true,
		"client_secret":             true,
		"secrets":                   true,
		"credentials":               true,
		"cookie":                    true,
		"gen_ai.usage.input_tokens": false,
		"max_tokens":                false,
		"token_count":               false,
		"token_usage":               false,
		"cache_key":                 false,
		"session_id":                false,
		"agent_id":                  false,
		"msg":                       false,
		"":                          false,
	}
	for key, want := range cases {
		if got := IsSensitiveKey(key); got != want {
			t.Errorf("IsSensitiveKey(%q) = %v，期望 %v", key, got, want)
		}
	}
}

func TestSnakeKey(t *testing.T) {
	cases := map[string]string{
		"agent_id":    "agent_id",
		"agentID":     "agent_id",
		"agentId":     "agent_id",
		"SessionID":   "session_id",
		"HTTPStatus":  "http_status",
		"x-request":   "x_request",
		"gen_ai.name": "gen_ai.name",
		"runID2":      "run_id2",
		"a b":         "a_b",
	}
	for in, want := range cases {
		if got := SnakeKey(in); got != want {
			t.Errorf("SnakeKey(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// logJSON 用脱敏 JSON Handler 记录一次并解析输出。
func logJSON(t *testing.T, fn func(l *slog.Logger)) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	fn(New(&buf, slog.LevelDebug))
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("日志不是合法 JSON: %v\n%s", err, buf.String())
	}
	return out
}

func TestRedactStringValues(t *testing.T) {
	out := logJSON(t, func(l *slog.Logger) {
		l.Info("x", "api_key", "sk-123", "input_tokens", 42, "agentID", "A1", "token", "t")
	})
	if out["api_key"] != RedactedValue {
		t.Errorf("api_key 应被脱敏，得到 %v", out["api_key"])
	}
	if out["token"] != RedactedValue {
		t.Errorf("token 应被脱敏，得到 %v", out["token"])
	}
	if out["input_tokens"] != float64(42) {
		t.Errorf("input_tokens 是计数，不应被脱敏，得到 %v", out["input_tokens"])
	}
	if out["agent_id"] != "A1" {
		t.Errorf("agentID 应被规范化为 agent_id，得到 %v", out)
	}
	if _, ok := out["agentID"]; ok {
		t.Error("原始的 camelCase 键名不应出现在输出里")
	}
}

func TestRedactNumericNotRedacted(t *testing.T) {
	out := logJSON(t, func(l *slog.Logger) {
		l.Info("x", "token_count", 7, "password", 12345)
	})
	if out["token_count"] != float64(7) {
		t.Errorf("token_count 不应被脱敏，得到 %v", out["token_count"])
	}
	if out["password"] != float64(12345) {
		t.Errorf("数值类型不会是凭据，不应被脱敏，得到 %v", out["password"])
	}
}

func TestRedactGroupsAndWithAttrs(t *testing.T) {
	out := logJSON(t, func(l *slog.Logger) {
		l = l.With("authorization", "Bearer abc", "sessionID", "S")
		l.Info("x",
			slog.Group("provider", "name", "openai", "api_key", "sk"),
			slog.Group("credentials", "user", "u", "pass", "p"),
		)
	})
	if out["authorization"] != RedactedValue {
		t.Errorf("With 预置的敏感属性应被脱敏，得到 %v", out["authorization"])
	}
	if out["session_id"] != "S" {
		t.Errorf("With 预置的键名应规范化，得到 %v", out)
	}
	provider, _ := out["provider"].(map[string]any)
	if provider["name"] != "openai" || provider["api_key"] != RedactedValue {
		t.Errorf("分组内的敏感属性应被脱敏，得到 %v", out["provider"])
	}
	if out["credentials"] != RedactedValue {
		t.Errorf("敏感键名的整个分组应被替换，得到 %v", out["credentials"])
	}
}

func TestRedactLogValuer(t *testing.T) {
	out := logJSON(t, func(l *slog.Logger) {
		l.Info("x", "secret", lazyValue("plain"), "note", lazyValue("ok"))
	})
	if out["secret"] != RedactedValue {
		t.Errorf("LogValuer 解析后的敏感值应被脱敏，得到 %v", out["secret"])
	}
	if out["note"] != "ok" {
		t.Errorf("非敏感的 LogValuer 应正常输出，得到 %v", out["note"])
	}
}

type lazyValue string

func (v lazyValue) LogValue() slog.Value { return slog.StringValue(string(v)) }

func TestRedactWithGroupHandler(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(NewRedactingHandler(slog.NewJSONHandler(&buf, nil))).WithGroup("req")
	l.Info("x", "password", "p", "path", "/a")
	s := buf.String()
	if !strings.Contains(s, `"password":"[redacted]"`) || !strings.Contains(s, `"path":"/a"`) {
		t.Errorf("WithGroup 之后仍应脱敏，得到 %s", s)
	}
}

func TestNewRedactingHandlerIdempotent(t *testing.T) {
	h := NewRedactingHandler(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if NewRedactingHandler(h) != h {
		t.Error("重复包装脱敏 Handler 应返回原对象")
	}
}
