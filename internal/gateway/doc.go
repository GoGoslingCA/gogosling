// Package gateway 是 IM 通道网关：把各即时通讯平台的消息归一化后送入会话，并把回复送回平台。
//
// 结构：
//   - Adapter 是平台适配器的统一接口（Start / Send / Stop），Factory 按 model.ChannelKind 创建实例；
//     本包自带 console（内存实现，供测试与 CLI）与 telegram（Bot API 长轮询）两个适配器。
//   - Gateway 是路由器：加载启用的通道并启动适配器；入站消息经净化、发送者白名单、群聊触发规则、
//     每通道每对端限流后，按会话键 "<agent_id>:channel:<kind>:<chat_id>"（SessionKey / ParseSessionKey，
//     与 session 包的键格式一致）交给 Sender.Send；订阅事件总线上的运行终态、审批请求与主动推送
//     （带 Meta.pushed=true 的 message.created），把文本分片后经对应通道的适配器送回。
//   - 回复路由：入站时记下"会话 → 通道与 chat"；内存中没有时（守护进程重启后、定时任务写入的通道会话），
//     若 Sender 实现了 SessionGetter，则读会话、从路由键解析出 kind 与 chat_id，找该智能体同类型的已启动通道。
//   - 删除智能体后由组合根调用 ForgetAgent 停止它的全部通道并清除路由。
//   - 本包对其他领域包的依赖只通过自己定义的最小接口（Sender、SessionGetter、SecretReader）表达，
//     由 internal/app 组合根用 session、secret 等包的实现适配。
//
// 安全约定：
//   - 入站内容一律视为不可信数据：SendRequest.Untrusted = true，Principal.Kind = channel_peer。
//   - 通道凭据只经 SecretReader 读取后直接交给 Adapter.Start，不进日志、数据库、事件与错误信息。
//   - 审批只能在桌面端完成；网关只把 approval.requested 通知到通道，不接受来自 IM 的审批指令。
//   - 运行失败原因只发送 Texts.RunErrorMap 中的本地化文案；未登记的原因（可能带内部细节）不外发，只发通用失败文案。
//   - 一个通道启动失败只写入该通道的 LastError，不影响其他通道。
//
// 待实现：
//   - 入站附件（图片、文件）的下载与落盘：需要 workspace 包的 inbound/ 写入能力，当前 telegram 适配器只处理文本与说明文字。
//   - 斜杠命令（/new、/stop 等）的分发。
//   - 飞书、钉钉、企业微信、Discord 适配器。
//   - 面向用户的提示文案（含 RunErrorMap）通过 Options.Texts 注入；集成阶段由组合根从 i18n 取值。
//
// 边界：适配器之间互不依赖；网关不决定智能体如何回答，也不执行任何工具。
package gateway
