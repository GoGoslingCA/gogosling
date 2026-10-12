// Package engine 抽象"智能体运行时"，并监管实现它的 Python 工作进程。
//
// 内核只依赖本包定义的 Runtime 接口；当前实现 Supervisor 通过标准输入输出与
// worker 目录下的 Python 工作进程交换行分隔的 JSON-RPC 2.0 消息（契约见
// api/worker-rpc/protocol.md）。运行时引擎因此可以替换，而不影响其余模块。
//
// 职责：
//   - Runtime 接口：发起运行（StartRun）、以回调流式接收事件（EventSink）、取消（CancelRun）、状态（Status）
//   - 工作进程监管：定位命令（Locate）、启动、worker.hello 握手与协议版本校验、worker.ping 健康检查、
//     崩溃或 EOF 后把在途运行以 run.failed 收尾并指数退避重启、worker.shutdown 优雅退出
//   - JSON-RPC 客户端：请求编号、待答表、通知分发（run.event → 对应运行的 sink，log → 日志）、
//     反向请求服务（host.tool.call → ToolExecutor，并发处理且不阻塞读循环）、单行 16 MiB 上限
//
// 设计要点：
//   - 运行状态只存在于 Supervisor.runs 一处；每个运行拥有自己的 context，取消运行即取消该运行上
//     所有在途的工具调用，等待审批的反向请求会立刻以 -32003 结束。
//   - StartRun 返回 nil 之后，sink 必然恰好收到一个终态事件（run.completed / run.failed / run.cancelled）；
//     工作进程崩溃由守护进程补发 run.failed，取消后工作进程迟迟不收尾则由守护进程补发 run.cancelled。
//     StartRun 返回错误时不会再有终态事件；运行在被工作进程接受之前就被取消的兜底移除时，
//     StartRun 返回 CodeCancelled，由调用方收尾。
//   - 失败原因只用导出的 Error* 机器常量（run.failed 的 Error、StartRun 错误的 Message），
//     由传输层按 i18n 的 run.error.* 本地化；退出码、协议错误等细节只进日志。
//   - openai_compatible 的 base_url 为空时 StartRun 直接返回 CodeInvalid（协议 §2），不替用户回退到任何默认服务商。
//   - reasoning.delta 等事件原样透传，engine 不解析其业务含义。
//   - sink 在读循环中同步调用，同一运行的事件严格有序；sink 必须快速返回（例如发布到 event.Bus），
//     耗时工作请自行异步化。
//   - 工作进程只拿到环境变量白名单（config.ChildEnvAllowlist，全仓唯一定义，含代理与证书变量）与协议要求的
//     变量；启动日志里代理 URL 的 userinfo 已脱敏。凭据只出现在 run.start 请求体里，不进日志、不进事件。
//   - 所有 goroutine 都随 Start 传入的 ctx 或 Stop 结束；Stop 返回前会有界地（5 秒）等待在途的反向请求
//     （工具调用）收尾，之后组合根才能安全关闭扩展与数据库。进程退出时在 POSIX 上按进程组整体结束，不留孤儿；
//     Windows 上以 CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP 启动，无控制台的守护进程不会为它弹出窗口。
//   - GOGOSLING_WORKER_CMD 按 POSIX shell 的引号与转义规则拆分（不做变量展开）；Windows 上反斜杠不作转义。
//
// 待实现：
//   - Windows 上用 Job Object 约束子进程树（当前只结束直接子进程，工作进程再派生的进程可能残留）
//   - 进程池策略：每个智能体一个进程还是共享进程（当前为单进程共享，运行按 run_id 复用）
//   - 内置 Python 运行环境的完整性校验（随安装包分发，不在首次启动时联网安装）
//   - host.secret.get 反向请求（协议保留，当前返回 -32601）
//
// 边界：不解析模型输出的业务含义；只做传输、监管与事件转发。工具的权限判定、沙箱与审计由
// ToolExecutor 的实现（集成阶段由 tools 包提供）负责。
package engine
