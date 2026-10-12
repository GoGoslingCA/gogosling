package session

import (
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// DefaultPeer 是桌面与命令行会话的默认对端标识。
const DefaultPeer = "local"

// keySeparator 是路由键各段之间的分隔符。
const keySeparator = ":"

// Key 是解析后的会话路由键。
//
// 路由键把"谁（智能体）通过哪个入口（来源）和谁（对端）在对话"映射到一个 active 会话，
// 是 IM 消息与定时任务找到目标会话的唯一依据。生成与解析集中在本文件，调用方不要自行拼接字符串。
//
// 唯一的例外是 gateway 包：它按依赖规则不导入本包，用 gateway.SessionKey / ParseSessionKey 维护
// IM 会话键 "<agent_id>:channel:<kind>:<chat_id>"（即 origin=channel、peer="<kind>:<chat_id>"）的镜像实现。
// 两边各有一个用例（本包 TestChannelKeyMatchesGatewayFormat、gateway 包 TestSessionKeyMatchesSessionPackage）
// 用同一个样例键锁定格式；修改分隔符或分段规则时必须同步两边。
type Key struct {
	AgentID string
	Origin  model.Origin
	// Peer 标识对端：桌面与命令行为 "local"，IM 通道为平台会话标识（可以包含冒号）。
	Peer string
}

// MakeKey 按 "<agent_id>:<origin>:<peer>" 组合路由键；peer 为空时使用 DefaultPeer。
func MakeKey(agentID string, origin model.Origin, peer string) string {
	if peer == "" {
		peer = DefaultPeer
	}
	return agentID + keySeparator + string(origin) + keySeparator + peer
}

// String 返回组合后的路由键。
func (k Key) String() string { return MakeKey(k.AgentID, k.Origin, k.Peer) }

// ParseKey 解析并校验路由键：agent 与 origin 段不得为空，origin 必须是已知来源，peer 不得为空。
// peer 段允许包含冒号，因此只在前两个分隔符处切分。
func ParseKey(key string) (Key, error) {
	parts := strings.SplitN(key, keySeparator, 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Key{}, model.Errorf(model.CodeInvalid, "路由键格式应为 <agent_id>:<origin>:<peer>: %q", key)
	}
	origin := model.Origin(parts[1])
	if !validOrigin(origin) {
		return Key{}, model.Errorf(model.CodeInvalid, "未知的会话来源 %q", parts[1])
	}
	return Key{AgentID: parts[0], Origin: origin, Peer: parts[2]}, nil
}

// validOrigin 判断来源是否是 model 中定义的取值。
func validOrigin(o model.Origin) bool {
	switch o {
	case model.OriginDesktop, model.OriginCLI, model.OriginChannel, model.OriginJob, model.OriginACP:
		return true
	}
	return false
}

// validAgentID 判断智能体 ID 能否安全地作为路由键的首段。
func validAgentID(id string) bool {
	return id != "" && !strings.Contains(id, keySeparator)
}
