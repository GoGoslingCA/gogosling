package model

import (
	"encoding/json"
	"time"
)

// ExtensionKind 是扩展的类型。
type ExtensionKind string

const (
	// ExtensionSkill 是 Agent Skills 规范的技能目录（SKILL.md + 资源文件），按需注入上下文。
	ExtensionSkill ExtensionKind = "skill"
	// ExtensionMCPServer 是一个 MCP 服务，提供工具、资源与提示词。
	ExtensionMCPServer ExtensionKind = "mcp_server"
	// ExtensionPlugin 是进程外插件，通过 extension.json 清单声明能力与权限。
	ExtensionPlugin ExtensionKind = "plugin"
)

// MCPTransport 是 MCP 服务的传输方式。
type MCPTransport string

const (
	MCPStdio MCPTransport = "stdio"
	MCPHTTP  MCPTransport = "http"
)

// MCPServerConfig 描述如何连接一个 MCP 服务。
type MCPServerConfig struct {
	Transport MCPTransport `json:"transport"`
	// Command / Args / Env 用于 stdio；Env 的值可以是 "secret:<ref>" 形式的密钥引用。
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// URL 用于 http。
	URL string `json:"url,omitempty"`
	// Headers 用于 http 传输的附加请求头（如 Authorization）；值同样允许 "secret:<ref>" 引用，
	// 由 EnvResolver 在连接时解析，解析结果只进请求头。
	Headers map[string]string `json:"headers,omitempty"`
}

// Extension 是一个已安装的扩展。
type Extension struct {
	ID      string        `json:"id"`
	Kind    ExtensionKind `json:"kind"`
	Name    string        `json:"name"`
	Version string        `json:"version,omitempty"`
	// Source 是安装来源：本地目录路径、仓库地址或内置标识。
	Source string `json:"source"`
	// InstallDir 是扩展文件在数据目录下的位置（绝对路径）。
	InstallDir string `json:"install_dir"`
	// Manifest 是原始清单（SKILL.md 的 frontmatter、extension.json 或 MCP 配置），结构随 Kind 而异。
	Manifest json.RawMessage `json:"manifest,omitempty"`
	// MCP 仅在 Kind 为 mcp_server 时存在。
	MCP     *MCPServerConfig `json:"mcp,omitempty"`
	Enabled bool             `json:"enabled"`
	// Digest 是安装时计算的内容哈希，用于检测被篡改或升级。
	Digest      string    `json:"digest,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
