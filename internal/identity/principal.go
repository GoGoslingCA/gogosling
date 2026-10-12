package identity

import (
	"context"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// LocalPrincipalID 是单用户桌面模式下本机用户的固定 ID。
const LocalPrincipalID = "local"

// LocalPrincipal 返回持有本机访问令牌的桌面用户 / 命令行身份：ID "local"，角色 admin。
// 每次调用返回独立副本，调用方修改不会影响其他人。
func LocalPrincipal() model.Principal {
	return model.Principal{
		ID:    LocalPrincipalID,
		Kind:  model.PrincipalLocalUser,
		Name:  "local",
		Roles: []string{model.RoleAdmin},
	}
}

// ChannelPeerPrincipal 构造 IM 通道对端的身份：ID 形如 "<kind>:<peerID>"，角色 member。
// 对端的输入一律视为不可信数据，权限规则可按 Kind 收紧。
func ChannelPeerPrincipal(kind model.ChannelKind, peerID, name string) model.Principal {
	return model.Principal{
		ID:    string(kind) + ":" + peerID,
		Kind:  model.PrincipalChannelPeer,
		Name:  name,
		Roles: []string{model.RoleMember},
	}
}

type principalKey struct{}

// WithPrincipal 把调用方身份放进 ctx。
func WithPrincipal(ctx context.Context, p model.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext 取出 ctx 中的调用方身份；没有时返回 false。
func FromContext(ctx context.Context) (model.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(model.Principal)
	return p, ok
}

// RequirePrincipal 要求 ctx 携带身份；没有时返回 CodeUnauthorized 的错误。
func RequirePrincipal(ctx context.Context) (model.Principal, error) {
	p, ok := FromContext(ctx)
	if !ok {
		return model.Principal{}, model.NewError(model.CodeUnauthorized, "请求未携带调用方身份")
	}
	return p, nil
}

// RequireRole 要求 ctx 中的身份拥有 role：
// 没有身份返回 CodeUnauthorized，有身份但缺少角色返回 CodeForbidden（Details 带 role 与 principal）。
func RequireRole(ctx context.Context, role string) error {
	p, err := RequirePrincipal(ctx)
	if err != nil {
		return err
	}
	if !p.HasRole(role) {
		return &model.Error{
			Code:    model.CodeForbidden,
			Message: "需要 " + role + " 角色",
			Details: map[string]any{"role": role, "principal": p.ID},
		}
	}
	return nil
}
