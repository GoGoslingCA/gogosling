package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// TokenBytes 是访问令牌的随机字节数；编码后为 43 个 base64url 字符。
const TokenBytes = 32

// NewToken 生成新的访问令牌（32 字节加密随机数，base64url 无填充）。
func NewToken() (string, error) {
	buf := make([]byte, TokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", model.Wrap(model.CodeInternal, "系统随机源不可用", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ParseBearer 从 Authorization 头中取出 Bearer 令牌；scheme 不区分大小写。
// 头为空、scheme 不是 Bearer 或令牌为空时返回 false。
func ParseBearer(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

// Authenticator 校验本机访问令牌。可并发使用；Rotate 之后新令牌立即生效、旧令牌失效。
//
// 内部只保存令牌的 SHA-256 摘要并用常量时间比较，既不泄露长度信息，
// 也避免在内存中长期持有明文令牌的多个副本。
type Authenticator struct {
	mu        sync.RWMutex
	digest    [sha256.Size]byte
	principal model.Principal
}

// NewAuthenticator 创建只接受 token 的校验器；通过校验的调用方被视为本机用户（LocalPrincipal）。
func NewAuthenticator(token string) (*Authenticator, error) {
	a := &Authenticator{principal: LocalPrincipal()}
	if err := a.Rotate(token); err != nil {
		return nil, err
	}
	return a, nil
}

// Rotate 替换接受的令牌；空令牌被拒绝。
func (a *Authenticator) Rotate(token string) error {
	if strings.TrimSpace(token) == "" {
		return model.NewError(model.CodeInvalid, "访问令牌不能为空")
	}
	d := sha256.Sum256([]byte(token))
	a.mu.Lock()
	a.digest = d
	a.mu.Unlock()
	return nil
}

// Verify 用常量时间比较判断 token 是否有效。
func (a *Authenticator) Verify(token string) bool {
	if token == "" {
		return false
	}
	d := sha256.Sum256([]byte(token))
	a.mu.RLock()
	defer a.mu.RUnlock()
	return subtle.ConstantTimeCompare(d[:], a.digest[:]) == 1
}

// Authenticate 校验 Authorization 头并返回调用方身份；失败返回 CodeUnauthorized 的错误。
// 令牌只接受请求头，不接受查询参数或 Cookie。
func (a *Authenticator) Authenticate(ctx context.Context, authorization string) (model.Principal, error) {
	if err := ctx.Err(); err != nil {
		return model.Principal{}, err
	}
	token, ok := ParseBearer(authorization)
	if !ok {
		if strings.TrimSpace(authorization) == "" {
			return model.Principal{}, model.NewError(model.CodeUnauthorized, "缺少访问令牌")
		}
		return model.Principal{}, model.NewError(model.CodeUnauthorized, "Authorization 头格式不正确，应为 Bearer <token>")
	}
	if !a.Verify(token) {
		return model.Principal{}, model.NewError(model.CodeUnauthorized, "访问令牌无效")
	}
	a.mu.RLock()
	p := a.principal
	a.mu.RUnlock()
	return p, nil
}

// String 实现 fmt.Stringer，避免把摘要或令牌意外打印进日志。
func (a *Authenticator) String() string {
	return fmt.Sprintf("identity.Authenticator(%s)", a.principal.ID)
}
