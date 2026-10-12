// Package session 管理会话（线程）、消息历史与一次次的运行（run），并编排一次对话。
//
// 无论请求来自桌面界面、命令行、IM 通道还是定时任务，都会落到同一种会话模型上，
// 来源只是会话的一个属性；所有入口都经过 Service.Send，因此"同一会话同一时刻只有一个运行"
// 这条不变量只在这里维护一次。
//
// 职责：
//   - 会话的创建、归档、检索、删除（含删除智能体前的 DeleteByAgent），以及路由键（"<agent_id>:<origin>:<peer>"）到会话的解析
//   - 消息与运行记录的持久化；运行状态机：queued → running ⇄ awaiting_approval → completed | failed | cancelled。
//     running ⇄ awaiting_approval 由 NoteApproval 推进：approval.* 由 permission 包发布到总线（不经过运行时），
//     组合根订阅后调用 NoteApproval，本包只改状态并落库、不重复发布
//   - session.* 事件的 payload 是 View（会话 + active_run_id），与 OpenAPI 的 Session 形状一致
//   - 一次对话的编排：追加用户消息、组装运行请求（系统提示 + 最近 HistoryLimit 条历史 + 工具 + 凭据）、
//     交给 Runtime，并把运行事件转换成状态变化与总线事件
//   - 启动恢复：把上次进程遗留的非终态运行标记为失败
//
// 边界与设计要点：
//   - 本包只依赖 model、event、config；智能体、凭据、工具与运行时都通过本包定义的最小接口
//     （AgentSource、CredentialSource、ToolSource、Runtime、PromptBuilder）注入，由组合根适配到
//     agent、provider、tools、engine 等包。
//   - 流式事件（text.delta、tool.call.*、usage）只转发到事件总线，不写数据库；
//     数据库只在状态迁移与终态时写入，避免逐 token 落盘拖慢流式输出。
//   - 凭据只在组装 RunRequest 时短暂存在于内存，不进日志、数据库与事件。
//   - 导出类型 Service 并发安全。
//
// 待实现（超出本包范围，由其他模块或后续版本提供）：
//   - 审批决定的提交由 permission 包负责；本包只经 NoteApproval 记录 awaiting_approval 状态
//   - 上下文压缩与历史摘要：当前只按条数截窗并保证窗口从用户回合开始
//   - 历史导出与会话分叉
package session
