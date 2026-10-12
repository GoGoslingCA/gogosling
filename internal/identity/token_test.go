package identity

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestNewToken(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken()
	if a == b {
		t.Error("两次生成的令牌不应相同")
	}
	raw, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil || len(raw) != TokenBytes {
		t.Errorf("令牌应是 32 字节的 base64url 编码: %q (%v)", a, err)
	}
	if strings.ContainsAny(a, "+/=") {
		t.Errorf("令牌应为 URL 安全字符: %q", a)
	}
}

func TestParseBearer(t *testing.T) {
	cases := map[string]struct {
		token string
		ok    bool
	}{
		"Bearer abc":      {"abc", true},
		"bearer abc":      {"abc", true},
		"BEARER abc ":     {"abc", true},
		"Bearer":          {"", false},
		"Bearer ":         {"", false},
		"Basic abc":       {"", false},
		"abc":             {"", false},
		"":                {"", false},
		"Bearer a b":      {"", false},
		"Bearerabc":       {"", false},
		"Bearer  spaced ": {"spaced", true},
	}
	for in, want := range cases {
		token, ok := ParseBearer(in)
		if token != want.token || ok != want.ok {
			t.Errorf("ParseBearer(%q) = (%q, %v)，期望 (%q, %v)", in, token, ok, want.token, want.ok)
		}
	}
}

func TestAuthenticator(t *testing.T) {
	ctx := context.Background()
	token, _ := NewToken()
	a, err := NewAuthenticator(token)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Authenticate(ctx, "Bearer "+token)
	if err != nil {
		t.Fatalf("正确令牌应通过: %v", err)
	}
	if p.ID != LocalPrincipalID || !p.HasRole(model.RoleAdmin) || p.Kind != model.PrincipalLocalUser {
		t.Errorf("通过校验应得到本机 admin 身份，得到 %+v", p)
	}

	for name, header := range map[string]string{
		"缺少头":   "",
		"错误方案":  "Basic " + token,
		"错误令牌":  "Bearer " + token[:len(token)-1] + "x",
		"前缀匹配":  "Bearer " + token[:10],
		"更长的令牌": "Bearer " + token + "x",
	} {
		if _, err := a.Authenticate(ctx, header); !model.IsCode(err, model.CodeUnauthorized) {
			t.Errorf("%s 应返回 unauthorized，得到 %v", name, err)
		}
	}
	if a.Verify("") {
		t.Error("空令牌不应通过")
	}

	rotated, _ := NewToken()
	if err := a.Rotate(rotated); err != nil {
		t.Fatal(err)
	}
	if a.Verify(token) || !a.Verify(rotated) {
		t.Error("轮换后旧令牌应失效、新令牌应生效")
	}
	if err := a.Rotate("  "); err == nil {
		t.Error("空令牌不能作为新令牌")
	}
	if _, err := NewAuthenticator(""); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("空令牌创建校验器应返回 invalid，得到 %v", err)
	}
	if s := a.String(); strings.Contains(s, rotated) {
		t.Error("String 不应暴露令牌")
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.Authenticate(cancelled, "Bearer "+rotated); err != context.Canceled {
		t.Errorf("已取消的 ctx 应返回取消错误，得到 %v", err)
	}
}
