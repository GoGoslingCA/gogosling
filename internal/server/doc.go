// Package server 实现守护进程的本地 HTTP API 与事件流（SSE），契约见 api/openapi/openapi.yaml。
//
// 它是 Web UI、桌面壳、CLI 访问内核的唯一入口；与组合根之间的编译期契约在 deps.go。
//
// 生命周期（见 deps.go 顶部注释）：
//
//	srv, err := server.New(deps, opts) // 只允许回环监听地址（config.IsLoopbackAddr）；读取一次设置项
//	addr, err := srv.Listen(ctx)       // 绑定监听地址，返回实际地址（端口可为 0）
//	err = srv.Serve(ctx)               // 阻塞；ctx 取消后优雅关闭（在途请求最多等 10s）
//	h := srv.Handler()                 // 测试直接驱动
//
// 经 API 关闭：POST /api/v1/system/shutdown 鉴权后先写出 202，再调用组合根提供的 Options.Shutdown
// （只调用一次；它让 Serve 的 ctx 取消）。没有提供时返回 unavailable。这是三个平台一致的优雅关闭途径
// （Windows 上没有可用的信号），桌面壳与 CLI 优先用它，超时后才强制结束进程。
//
// 中间件链顺序固定（由外到内）：panic 恢复 → 请求日志 → Host 校验 → Origin 校验 → 鉴权 →
// 语言解析 → 请求体上限 → 读写期限 → 路由。要点：
//   - Host 只接受回环 IP 字面量（127.0.0.0/8、[::1]）或 localhost（任意端口），挡住 DNS 重绑定；
//     规则与监听地址相同（config.IsLoopbackAddr），全仓只有一份；
//   - Origin：无 Origin、app://gogosling、Options.AllowedOrigins 与设置项 allowed_origins 放行，其余 403；
//     不发送任何 Access-Control-Allow-* 头（浏览器跨源读取一律失败）；
//   - 除 GET /healthz 外全部要求 Authorization: Bearer，令牌只认请求头、绝不进日志；
//   - 错误统一写成 {"error":{"code","message","details"}}，message 按请求语言本地化，
//     internal 错误只进日志不外泄细节。请求语言（与 OpenAPI 一致）：Accept-Language > 设置项 locale >
//     Options.DefaultLocale > en；Run.Error 与 run.failed 中的机器常量（session.Error*、engine.Error*）
//     经 i18n.RunErrorText 换成同一语言的 run.error.* 文案。
//
// 设置：GET /api/v1/settings 的 timezone / locale 只反映用户显式保存的值（未设置时省略），
// effective_locale / effective_timezone 是实际生效值（设置项 > Options.DefaultLocale / DefaultTimezone）。
//
// 处理器只做"解码 → 调用领域包 → 编码"，校验与状态机都在领域包内。传输层只保留自身约束：
// 分页上限（缺省 100、上限 500，messages 也一样）、请求体上限、PATCH 的缺席 / null 语义、
// 附件路径必须位于工作区 inbound/ 之下、设置项格式、POST /sessions 只接受 desktop / cli 两种 origin。
// 仍留在传输层、应下沉到领域包的规则见 docs/reference-notes/server.md（扩展目录类型识别、
// 会话 → 工作区解析链、任务 agent_id 不可变检查）。
//
// 事件流：GET /api/v1/events 每条连接先写一帧 event: hello（{"started_at","last_seq"}，不带 id），
// 再订阅 event.Bus，按 since / Last-Event-ID 续传；订阅因慢消费被踢时服务端从最后写出的 Seq 重新订阅
// （仍在回放缓冲内则对客户端无感），只有真正丢失事件（回放缓冲已覆盖、或 since 大于本实例当前 Seq）
// 时才发送 gap 并结束响应。新实例的 Seq 追上旧 since 的重启由客户端比较 hello.started_at 发现。
// session.* 事件与 /sessions 响应的载荷都是 session.View（含 active_run_id）。
//
// 边界：本包只导入叶子包与领域包，不导入 internal/app、cmd 与 internal/client；
// servertest 子包提供接近真实的测试装配（真实 SQLite、文件凭据后端、生产的通道服务，只有推理运行时与
// IM 网关是测试替身），供本包与集成测试使用。
//
// 待实现：
//   - 可选的 Unix 域套接字 / Windows 命名管道监听（文件权限即可作为第二道边界）；
//   - 审批规则（permission.Rules）的读写端点，待契约补充。
package server
