package model

// PrincipalKind 是调用方身份的类型。
type PrincipalKind string

const (
	// PrincipalLocalUser 是持有本机访问令牌的桌面用户或命令行。
	PrincipalLocalUser PrincipalKind = "local_user"
	// PrincipalChannelPeer 是通过 IM 通道发来消息的对端。
	PrincipalChannelPeer PrincipalKind = "channel_peer"
	// PrincipalSystem 是内核自身（定时任务、维护作业）。
	PrincipalSystem PrincipalKind = "system"
)

// Role 的取值见 RoleAdmin 与 RoleMember；单用户桌面模式下本机用户总是 admin。
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Principal 回答"是谁在调用"。它由 identity 包解析得到，随 context 传递。
type Principal struct {
	ID    string        `json:"id"`
	Kind  PrincipalKind `json:"kind"`
	Name  string        `json:"name,omitempty"`
	Roles []string      `json:"roles,omitempty"`
}

// HasRole 判断是否拥有某个角色。
func (p Principal) HasRole(role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// SystemPrincipal 是内核自身的身份。
func SystemPrincipal() Principal {
	return Principal{ID: "system", Kind: PrincipalSystem, Name: "system", Roles: []string{RoleAdmin}}
}
