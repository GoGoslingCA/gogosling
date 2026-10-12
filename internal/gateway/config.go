package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 群聊触发模式。
const (
	// GroupModeMention 只在机器人被 @、被回复或消息带触发前缀时响应群消息（默认）。
	GroupModeMention = "mention"
	// GroupModeAll 响应群里的每一条消息。
	GroupModeAll = "all"
)

// 限流默认值：每个通道里每个对端每分钟 20 条、允许 5 条突发。
const (
	DefaultRatePerMinute = 20
	DefaultRateBurst     = 5
)

// ChannelConfig 是 model.Channel.Config 中与平台无关的部分；平台专有键（如 telegram 的 api_base）
// 与这些键并存于同一个 JSON 对象，由各适配器自行解析。
type ChannelConfig struct {
	// AllowedPeers 是允许触发智能体的发送者 ID 白名单；空表示允许所有人。
	AllowedPeers []string `json:"allowed_peers,omitempty"`
	// GroupMode 是群聊触发模式：mention（默认）或 all。
	GroupMode string `json:"group_mode,omitempty"`
	// TriggerPrefix 是触发前缀（如 "!" 或 "/ask"）：带前缀的消息在任何模式下都会触发，并去掉前缀后再送入会话。
	TriggerPrefix string `json:"trigger_prefix,omitempty"`
	// RatePerMinute 是每个对端每分钟允许的消息数；0 表示默认值。
	RatePerMinute float64 `json:"rate_per_minute,omitempty"`
	// RateBurst 是允许的突发条数；0 表示默认值。
	RateBurst int `json:"rate_burst,omitempty"`
	// MaxChunk 是单条出站消息的最大字符数，超过的回复会被分片；0 表示默认值 DefaultMaxChunk。
	MaxChunk int `json:"max_chunk,omitempty"`
}

// ParseChannelConfig 解析并校验通道配置；raw 为空或 null 时返回默认值。未知键被忽略（留给适配器）。
func ParseChannelConfig(raw json.RawMessage) (ChannelConfig, error) {
	var cfg ChannelConfig
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &cfg); err != nil {
			return ChannelConfig{}, model.Wrap(model.CodeInvalid, "通道配置不是合法的 JSON 对象", err)
		}
	}
	return cfg.normalize()
}

// normalize 校验取值并用默认值填补零值。
func (c ChannelConfig) normalize() (ChannelConfig, error) {
	peers := make([]string, 0, len(c.AllowedPeers))
	for _, p := range c.AllowedPeers {
		if p = strings.TrimSpace(p); p != "" {
			peers = append(peers, p)
		}
	}
	c.AllowedPeers = peers

	switch c.GroupMode {
	case "":
		c.GroupMode = GroupModeMention
	case GroupModeMention, GroupModeAll:
	default:
		return ChannelConfig{}, &model.Error{
			Code:    model.CodeInvalid,
			Message: "group_mode 只能是 mention 或 all",
			Details: map[string]any{"group_mode": c.GroupMode},
		}
	}
	c.TriggerPrefix = strings.TrimSpace(c.TriggerPrefix)

	if c.RatePerMinute < 0 || c.RateBurst < 0 || c.MaxChunk < 0 {
		return ChannelConfig{}, model.NewError(model.CodeInvalid, "rate_per_minute、rate_burst、max_chunk 不能为负数")
	}
	if c.RatePerMinute == 0 {
		c.RatePerMinute = DefaultRatePerMinute
	}
	if c.RateBurst == 0 {
		c.RateBurst = DefaultRateBurst
	}
	if c.MaxChunk == 0 {
		c.MaxChunk = DefaultMaxChunk
	}
	return c, nil
}

// PeerAllowed 判断发送者是否在白名单内；白名单为空时允许所有人。
func (c ChannelConfig) PeerAllowed(peerID string) bool {
	if len(c.AllowedPeers) == 0 {
		return true
	}
	for _, p := range c.AllowedPeers {
		if p == peerID {
			return true
		}
	}
	return false
}

// SessionKeySeparator 是会话路由键各段之间的分隔符。
//
// IM 会话的路由键是 "<agent_id>:channel:<kind>:<chat_id>"，即 session 包三段式键
// "<agent_id>:<origin>:<peer>"（session.MakeKey / ParseKey）在 origin=channel、peer="<kind>:<chat_id>" 时的形式。
// 本包按依赖约定不导入 session，因此在这里维护它的镜像：分隔符与 session 包的 keySeparator 相同，
// origin 段取 model.OriginChannel。两边各有一个用例（本包 TestSessionKeyMatchesSessionPackage、
// session 包 TestChannelKeyMatchesGatewayFormat）用同一个样例键锁定格式，修改任一侧都必须同步另一侧。
const SessionKeySeparator = ":"

// SessionKey 组合 IM 会话的路由键："<agent_id>:channel:<kind>:<chat_id>"（格式见 SessionKeySeparator）。
// chat_id 可以包含分隔符：它总在最后一段。
func SessionKey(agentID string, kind model.ChannelKind, chatID string) string {
	return agentID + SessionKeySeparator + string(model.OriginChannel) + SessionKeySeparator +
		string(kind) + SessionKeySeparator + chatID
}

// ParseSessionKey 是 SessionKey 的逆运算：只接受 origin 段为 channel、各段非空的键；
// 其他来源的会话（桌面、命令行、定时任务）返回 ok=false。
func ParseSessionKey(key string) (agentID string, kind model.ChannelKind, chatID string, ok bool) {
	parts := strings.SplitN(key, SessionKeySeparator, 4)
	if len(parts) != 4 || parts[1] != string(model.OriginChannel) {
		return "", "", "", false
	}
	for _, p := range parts {
		if p == "" {
			return "", "", "", false
		}
	}
	return parts[0], model.ChannelKind(parts[2]), parts[3], true
}
