// Package provider 管理模型供应商与模型配置。
//
// 职责：
//   - Service 提供供应商的增删改查；API key 只写入 SecretStore（引用名 "provider:<id>"），
//     model.Provider 记录与事件里只有引用名，View 用 HasSecret 布尔值对外展示。
//   - Credentials 把 model.ModelRef 解析为交给工作进程的一次性凭据（内存中、不落盘、不进日志）。
//   - Test / Probe 用各类接口的"模型列表"端点做连通性测试（不消耗推理额度），成功后刷新 Models；
//     错误信息会把密钥替换为 ***。
//   - DiscoverLocal 探测本机 Ollama；Presets 提供内置预设（OpenAI、Anthropic、DeepSeek、
//     通义千问、Moonshot、智谱、MiniMax、Ollama）。
//   - Delete 通过 AgentLister 检查引用，被引用时返回 CodeConflict 并在 Details 里列出引用者。
//
// 待实现：
//   - 用量与费用统计的记录点。
//   - 供应商级别的自定义请求头与代理设置。
//   - 按模型的能力元数据（上下文窗口、是否支持视觉 / 工具调用）。
//
// 边界：本包只依赖标准库、model 与 event；不保存明文密钥，不直接发起推理请求。
// SecretStore 与 AgentLister 是本包定义的最小接口，由组合根用 secret 包与 store 实现适配。
package provider
