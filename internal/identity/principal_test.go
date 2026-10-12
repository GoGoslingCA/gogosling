package identity

import (
	"context"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestLocalPrincipalIsCopy(t *testing.T) {
	a := LocalPrincipal()
	a.Roles[0] = "mutated"
	if b := LocalPrincipal(); b.Roles[0] != model.RoleAdmin {
		t.Error("LocalPrincipal 应返回独立副本")
	}
}

func TestChannelPeerPrincipal(t *testing.T) {
	p := ChannelPeerPrincipal(model.ChannelTelegram, "12345", "Alice")
	if p.ID != "telegram:12345" || p.Kind != model.PrincipalChannelPeer || p.Name != "Alice" {
		t.Errorf("通道对端身份不符: %+v", p)
	}
	if p.HasRole(model.RoleAdmin) || !p.HasRole(model.RoleMember) {
		t.Errorf("通道对端应只有 member 角色: %v", p.Roles)
	}
}

func TestContextRoundTripAndRequireRole(t *testing.T) {
	ctx := context.Background()
	if _, ok := FromContext(ctx); ok {
		t.Error("空 ctx 不应有身份")
	}
	if err := RequireRole(ctx, model.RoleAdmin); !model.IsCode(err, model.CodeUnauthorized) {
		t.Errorf("没有身份时 RequireRole 应返回 unauthorized，得到 %v", err)
	}
	if _, err := RequirePrincipal(ctx); !model.IsCode(err, model.CodeUnauthorized) {
		t.Errorf("没有身份时 RequirePrincipal 应返回 unauthorized，得到 %v", err)
	}

	ctx = WithPrincipal(ctx, LocalPrincipal())
	p, ok := FromContext(ctx)
	if !ok || p.ID != LocalPrincipalID {
		t.Fatalf("应取回放入的身份，得到 %+v %v", p, ok)
	}
	if err := RequireRole(ctx, model.RoleAdmin); err != nil {
		t.Errorf("admin 应通过: %v", err)
	}

	peer := WithPrincipal(context.Background(), ChannelPeerPrincipal(model.ChannelFeishu, "u1", ""))
	err := RequireRole(peer, model.RoleAdmin)
	if !model.IsCode(err, model.CodeForbidden) {
		t.Fatalf("member 要求 admin 应返回 forbidden，得到 %v", err)
	}
	var e *model.Error
	if !errorsAs(err, &e) || e.Details["role"] != model.RoleAdmin || e.Details["principal"] != "feishu:u1" {
		t.Errorf("forbidden 错误应带 role 与 principal 细节: %+v", e)
	}
	if err := RequireRole(peer, model.RoleMember); err != nil {
		t.Errorf("member 要求 member 应通过: %v", err)
	}
}

func errorsAs(err error, target **model.Error) bool {
	e, ok := err.(*model.Error)
	if ok {
		*target = e
	}
	return ok
}
