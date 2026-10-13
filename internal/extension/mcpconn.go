package extension

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/protocol/mcp"
)

// MCPClient 是本包对 MCP 连接的最小依赖；*mcp.Client 直接满足，测试用假实现替代。
type MCPClient interface {
	// ListToolsPinned 列出工具并与固定摘要比对；见 mcp.Client.ListToolsPinned。
	ListToolsPinned(ctx context.Context, pinned string) (specs []model.ToolSpec, digest string, changed bool, err error)
	CallTool(ctx context.Context, name string, args json.RawMessage) (model.ToolResult, error)
	// Done 在连接结束后关闭。
	Done() <-chan struct{}
	Close() error
}

// MCPDialer 为一个 mcp_server 类型的扩展建立连接。
type MCPDialer func(ctx context.Context, ext *model.Extension) (MCPClient, error)

// NewMCPDialer 用 protocol/mcp 构造默认拨号器：resolve 解析 Env 里的 "secret:<ref>"，
// opts 为连接选项（WorkDir 会被设为扩展的安装目录）。
func NewMCPDialer(resolve mcp.EnvResolver, opts mcp.Options) MCPDialer {
	return func(ctx context.Context, ext *model.Extension) (MCPClient, error) {
		if ext == nil || ext.MCP == nil {
			return nil, model.NewError(model.CodeInvalid, "扩展没有 MCP 连接配置")
		}
		o := opts
		o.WorkDir = ext.InstallDir
		return mcp.Connect(ctx, ext.ID, *ext.MCP, resolve, o)
	}
}

// mcpConn 是一个 MCP 扩展的连接槽：缓存连接与最近一次列出的工具；按需重连。
type mcpConn struct {
	mu      sync.Mutex
	client  MCPClient
	specs   []model.ToolSpec
	digest  string
	changed bool
	lastErr string
	active  int
	retired bool
	idle    time.Duration
	timer   *time.Timer
	epoch   uint64
}

func mcpUnavailable() error {
	return model.NewError(model.CodeUnavailable, i18n.T("en", "errors.unavailable", nil))
}

// pinLocked 持有发现或调用的租约，阻止闲置回收；调用方必须恰好 release 一次。
func (c *mcpConn) pinLocked() {
	c.stopTimerLocked()
	c.active++
}

func (c *mcpConn) stopTimerLocked() {
	c.epoch++
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}

// release 在最后一次活动结束后才安排一次回收，不为未连接或正在执行的扩展启动轮询。
func (c *mcpConn) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
	if c.active != 0 || c.retired || !c.alive() {
		return
	}
	c.stopTimerLocked()
	epoch := c.epoch
	c.timer = time.AfterFunc(c.idle, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		// 已进入回调但尚未取锁的旧计时器也必须失效，不能关闭刚续租的连接。
		if c.epoch != epoch || c.active != 0 || c.retired {
			return
		}
		c.closeLocked()
	})
}

// alive 判断缓存的连接是否仍然可用。
func (c *mcpConn) alive() bool {
	if c.client == nil {
		return false
	}
	select {
	case <-c.client.Done():
		return false
	default:
		return true
	}
}

// closeLocked 关闭并丢弃缓存的连接；调用方持有 c.mu。
func (c *mcpConn) closeLocked() {
	c.stopTimerLocked()
	if c.client != nil {
		_ = c.client.Close()
		c.client = nil
	}
	c.specs = nil
}

// slot 返回（必要时创建）某个扩展的连接槽。
func (m *Manager) slot(id string) *mcpConn {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	c, ok := m.conns[id]
	if !ok {
		c = &mcpConn{idle: m.mcpIdleTimeout}
		m.conns[id] = c
	}
	return c
}

// dropSlot 关闭并移除某个扩展的连接槽。
func (m *Manager) dropSlot(id string) {
	m.mu.Lock()
	c, ok := m.conns[id]
	if ok {
		delete(m.conns, id)
	}
	m.mu.Unlock()
	if ok {
		c.mu.Lock()
		c.retired = true
		c.closeLocked()
		c.mu.Unlock()
	}
}

// connectMCP 确保扩展有一条活动连接，并返回最近一次列出的工具与变化标记。
//
// 连接建立（或重建）时都会重新列出工具并与记录里的 Digest 比对：
//   - Digest 为空（从未固定）：固定当前摘要并写回记录；
//   - 摘要不同：标记 changed，工具不再暴露，发布 extension.tools_changed 事件；
//   - 相同：正常使用。
//
// 连接仍然存活时直接复用缓存，不重复列出；成功返回持有租约，调用方须 release。
func (m *Manager) connectMCP(ctx context.Context, ext *model.Extension) (*mcpConn, error) {
	c := m.slot(ext.ID)
	if c == nil {
		return nil, mcpUnavailable()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retired {
		return nil, mcpUnavailable()
	}

	if c.alive() {
		c.pinLocked()
		return c, nil
	}
	c.closeLocked()
	client, err := m.dial(ctx, ext)
	if err != nil {
		c.lastErr = err.Error()
		m.log.Warn("连接 MCP 服务失败", "extension_id", ext.ID, "name", ext.Name, "error", err)
		return nil, err
	}
	specs, digest, changed, err := client.ListToolsPinned(ctx, ext.Digest)
	if err != nil {
		_ = client.Close()
		c.lastErr = err.Error()
		m.log.Warn("列出 MCP 工具失败", "extension_id", ext.ID, "name", ext.Name, "error", err)
		return nil, err
	}
	c.client = client
	c.specs = specs
	c.digest = digest
	c.changed = changed
	c.lastErr = ""

	if changed {
		c.pinLocked()
		m.log.Warn("MCP 工具声明与首次启用时不同，已停止向模型暴露，等待用户确认",
			"extension_id", ext.ID, "name", ext.Name, "pinned", ext.Digest, "current", digest)
		m.publish(EventToolsChanged, ext)
		return c, nil
	}
	if ext.Digest == "" {
		ext.Digest = digest
		ext.UpdatedAt = m.now()
		if err := m.repo.UpdateExtension(ctx, ext); err != nil {
			m.log.Warn("保存 MCP 工具摘要失败", "extension_id", ext.ID, "error", err)
			c.closeLocked()
			return nil, err
		}
	}
	c.pinLocked()
	m.log.Info("MCP 服务已连接", "extension_id", ext.ID, "name", ext.Name, "tool_count", len(specs))
	return c, nil
}

// Status 是一个扩展的运行时状态（当前只有 MCP 服务有连接状态）。
type Status struct {
	// Connected 表示有活动的 MCP 连接。
	Connected bool `json:"connected"`
	// ToolsChanged 表示工具声明与固定时不同，正在等待 AcceptTools。
	ToolsChanged bool `json:"tools_changed"`
	// ToolCount 是最近一次列出的工具数。
	ToolCount int `json:"tool_count"`
	// CurrentDigest 是最近一次列出的工具摘要。
	CurrentDigest string `json:"current_digest,omitempty"`
	// LastError 是最近一次连接或列出失败的原因。
	LastError string `json:"last_error,omitempty"`
}

// Status 返回扩展的运行时状态；没有连接槽的扩展返回零值。
func (m *Manager) Status(id string) Status {
	m.mu.Lock()
	c, ok := m.conns[id]
	m.mu.Unlock()
	if !ok {
		return Status{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{
		Connected:     c.alive(),
		ToolsChanged:  c.changed,
		ToolCount:     len(c.specs),
		CurrentDigest: c.digest,
		LastError:     c.lastErr,
	}
}
