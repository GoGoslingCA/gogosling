package provider

import "github.com/GoGoslingCA/gogosling/internal/model"

// Preset 是内置的供应商预设：界面据此预填表单，用户仍可修改任何字段。
// DefaultModel 只是建议值，连通性测试会刷新实际可用的模型列表。
type Preset struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Kind           model.ProviderKind `json:"kind"`
	BaseURL        string             `json:"base_url"`
	DefaultModel   string             `json:"default_model,omitempty"`
	RequiresAPIKey bool               `json:"requires_api_key"`
	// Docs 是获取密钥或查看模型列表的文档地址，可为空。
	Docs string `json:"docs,omitempty"`
}

// presets 按界面展示顺序排列：国际服务、国内服务、本地运行时。
var presets = []Preset{
	{
		ID: "openai", Name: "OpenAI", Kind: model.ProviderOpenAICompatible,
		BaseURL: "https://api.openai.com/v1", DefaultModel: "gpt-4.1-mini",
		RequiresAPIKey: true, Docs: "https://platform.openai.com/api-keys",
	},
	{
		ID: "anthropic", Name: "Anthropic", Kind: model.ProviderAnthropic,
		BaseURL: "https://api.anthropic.com", DefaultModel: "claude-sonnet-4-5",
		RequiresAPIKey: true, Docs: "https://console.anthropic.com/settings/keys",
	},
	{
		ID: "deepseek", Name: "DeepSeek", Kind: model.ProviderOpenAICompatible,
		BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat",
		RequiresAPIKey: true, Docs: "https://platform.deepseek.com/api_keys",
	},
	{
		ID: "dashscope", Name: "通义千问（DashScope）", Kind: model.ProviderOpenAICompatible,
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", DefaultModel: "qwen-plus",
		RequiresAPIKey: true, Docs: "https://help.aliyun.com/zh/model-studio/get-api-key",
	},
	{
		ID: "moonshot", Name: "Moonshot（Kimi）", Kind: model.ProviderOpenAICompatible,
		BaseURL: "https://api.moonshot.cn/v1", DefaultModel: "kimi-k2-0905-preview",
		RequiresAPIKey: true, Docs: "https://platform.moonshot.cn/console/api-keys",
	},
	{
		ID: "zhipu", Name: "智谱 AI", Kind: model.ProviderOpenAICompatible,
		BaseURL: "https://open.bigmodel.cn/api/paas/v4", DefaultModel: "glm-4.5",
		RequiresAPIKey: true, Docs: "https://open.bigmodel.cn/usercenter/apikeys",
	},
	{
		ID: "minimax", Name: "MiniMax", Kind: model.ProviderOpenAICompatible,
		BaseURL: "https://api.minimaxi.com/v1", DefaultModel: "MiniMax-M1",
		RequiresAPIKey: true, Docs: "https://platform.minimaxi.com/user-center/basic-information/interface-key",
	},
	{
		ID: "ollama", Name: "Ollama（本机）", Kind: model.ProviderOllama,
		BaseURL: DefaultOllamaURL, RequiresAPIKey: false, Docs: "https://ollama.com/download",
	},
}

// Presets 返回内置预设的副本，调用方可以自由修改。
func Presets() []Preset {
	out := make([]Preset, len(presets))
	copy(out, presets)
	return out
}

// PresetByID 按标识查找预设。
func PresetByID(id string) (Preset, bool) {
	for _, p := range presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}
