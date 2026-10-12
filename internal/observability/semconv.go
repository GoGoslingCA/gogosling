package observability

// 本文件定义追踪属性的键名常量，对齐 OpenTelemetry 语义约定
// （Generative AI 部分见 https://opentelemetry.io/docs/specs/semconv/gen-ai/ ）。
// 领域包只引用这些常量，日后接入 OTel SDK 时属性无需改名。

// GenAI 语义约定属性键名。
const (
	// AttrGenAIOperationName 是操作类型，取值见 Op* 常量。
	AttrGenAIOperationName = "gen_ai.operation.name"
	// AttrGenAIProviderName 是供应商标识（openai、anthropic、ollama 等）。
	AttrGenAIProviderName = "gen_ai.provider.name"
	// AttrGenAIRequestModel 是请求时指定的模型名。
	AttrGenAIRequestModel = "gen_ai.request.model"
	// AttrGenAIRequestTemperature 是请求的采样温度。
	AttrGenAIRequestTemperature = "gen_ai.request.temperature"
	// AttrGenAIRequestMaxTokens 是请求允许的最大输出 token 数。
	AttrGenAIRequestMaxTokens = "gen_ai.request.max_tokens"
	// AttrGenAIResponseModel 是供应商实际返回的模型名。
	AttrGenAIResponseModel = "gen_ai.response.model"
	// AttrGenAIResponseID 是供应商返回的响应标识。
	AttrGenAIResponseID = "gen_ai.response.id"
	// AttrGenAIResponseFinishReasons 是结束原因列表（stop、tool_calls、length 等）。
	AttrGenAIResponseFinishReasons = "gen_ai.response.finish_reasons"
	// AttrGenAIUsageInputTokens 是输入 token 数。
	AttrGenAIUsageInputTokens = "gen_ai.usage.input_tokens"
	// AttrGenAIUsageOutputTokens 是输出 token 数。
	AttrGenAIUsageOutputTokens = "gen_ai.usage.output_tokens"
	// AttrGenAIUsageCacheReadTokens 是命中提示缓存的输入 token 数（约定扩展，非标准字段）。
	AttrGenAIUsageCacheReadTokens = "gen_ai.usage.cache_read_tokens"
	// AttrGenAIConversationID 是会话标识，对应 model.Session.ID。
	AttrGenAIConversationID = "gen_ai.conversation.id"
	// AttrGenAIAgentID 是智能体标识。
	AttrGenAIAgentID = "gen_ai.agent.id"
	// AttrGenAIAgentName 是智能体名称。
	AttrGenAIAgentName = "gen_ai.agent.name"
	// AttrGenAIToolName 是被调用的工具名。
	AttrGenAIToolName = "gen_ai.tool.name"
	// AttrGenAIToolCallID 是工具调用标识。
	AttrGenAIToolCallID = "gen_ai.tool.call.id"
	// AttrGenAIToolType 是工具类型（function、extension、datastore）。
	AttrGenAIToolType = "gen_ai.tool.type"
)

// 通用语义约定属性键名。
const (
	// AttrErrorType 是错误类别；本项目填 model.Code 的字符串值。
	AttrErrorType = "error.type"
	// AttrServerAddress 是上游服务地址（供应商的主机名）。
	AttrServerAddress = "server.address"
	// AttrServerPort 是上游服务端口。
	AttrServerPort = "server.port"
)

// gogosling 自有属性键名（OTel 没有对应约定的部分）。
const (
	// AttrRunID 是一次运行的标识，对应 model.Run.ID。
	AttrRunID = "gogosling.run.id"
	// AttrRunTrigger 是运行的触发来源，对应 model.RunTrigger。
	AttrRunTrigger = "gogosling.run.trigger"
	// AttrApprovalID 是审批请求标识。
	AttrApprovalID = "gogosling.approval.id"
	// AttrChannelKind 是 IM 通道类型。
	AttrChannelKind = "gogosling.channel.kind"
)

// gen_ai.operation.name 的取值。
const (
	// OpChat 是一次对话补全（模型调用）。
	OpChat = "chat"
	// OpExecuteTool 是一次工具执行。
	OpExecuteTool = "execute_tool"
	// OpInvokeAgent 是一次完整的智能体运行（从输入到最终回复）。
	OpInvokeAgent = "invoke_agent"
	// OpEmbeddings 是一次向量化调用。
	OpEmbeddings = "embeddings"
)

// gen_ai.tool.type 的取值。
const (
	ToolTypeFunction  = "function"
	ToolTypeExtension = "extension"
	ToolTypeDatastore = "datastore"
)
