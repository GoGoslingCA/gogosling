package model

import "time"

// ProviderKind 决定工作进程用哪种客户端与模型服务通信。
type ProviderKind string

const (
	// ProviderOpenAICompatible 覆盖 OpenAI 及所有兼容其接口的服务（含大多数国内模型服务与本地运行时）。
	ProviderOpenAICompatible ProviderKind = "openai_compatible"
	// ProviderAnthropic 使用 Anthropic Messages 接口。
	ProviderAnthropic ProviderKind = "anthropic"
	// ProviderOllama 使用 Ollama 原生接口，支持自动发现本机实例。
	ProviderOllama ProviderKind = "ollama"
)

// Provider 是模型供应商配置。密钥不存放在这里，只保存指向 secret 存储的引用。
type Provider struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Kind    ProviderKind `json:"kind"`
	BaseURL string       `json:"base_url,omitempty"`
	// SecretRef 是密钥在 secret 存储中的名称；为空表示该供应商不需要密钥。
	SecretRef    string `json:"secret_ref,omitempty"`
	DefaultModel string `json:"default_model,omitempty"`
	// Models 是已知可用的模型列表，可由连通性测试刷新；为空表示未知。
	Models    []string  `json:"models,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProviderCredentials 是解析后、交给工作进程的一次性凭据；只在内存中存在，不得持久化或写日志。
type ProviderCredentials struct {
	Kind    ProviderKind `json:"kind"`
	BaseURL string       `json:"base_url,omitempty"`
	APIKey  string       `json:"api_key,omitempty"`
	Model   string       `json:"model"`
}
