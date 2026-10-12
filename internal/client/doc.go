// Package client 是本地 API（api/openapi/openapi.yaml）的 Go 客户端，供命令行与集成测试复用。
//
// 用法：
//
//	c, err := client.Discover(ctx, dataDir)         // 读 <dataDir>/daemon.json 并 GET /healthz 校验存活
//	c := client.New("http://127.0.0.1:46750", token, client.WithLocale("en"))
//	agents, err := c.ListAgents(ctx)
//	events, errs := c.Events(ctx, 0, "")            // SSE：断线按最后的 Seq 自动续传
//	err = c.Shutdown(ctx)                           // POST /system/shutdown：请求优雅关闭（202 即返回）
//
// 约定：
//   - 每个端点一个方法，参数与返回值使用 internal/model 的类型；服务端在 model 之外附加的字段
//     用视图类型表达（ChannelView、ExtensionStatus、VersionInfo、Settings；SessionView 与 ProviderView
//     是 session.View、provider.View 的别名，与服务端、事件载荷同形）。
//   - 事件流：每条连接开头的 hello 帧由本包消费、不投递；重连后 hello.started_at 变化（守护进程重启）时
//     丢弃本地续传位置，投递一条 gap 并从新实例的当前位置重新订阅。
//   - 所有错误都是 *model.Error：服务端错误信封解析为 Code / Message（已按 Accept-Language 本地化）/ Details；
//     无法到达守护进程为 CodeUnavailable；调用方取消为 CodeCancelled，超时为 CodeTimeout（底层错误可经 errors.Is 判断）。
//   - 部分更新（Patch 类型）用指针字段表达"缺席即不修改"；AgentPatch.Tools 指向 nil 切片时发送 null（重置为默认工具集）。
//   - 令牌只放在 Authorization 头里；传输层显式不使用代理，令牌不会离开本机。
//
// 并发：Client 不可变、可被多个 goroutine 同时使用。
//
// 边界：本包只导入叶子包与领域包（model、event、identity、provider、session），不导入 internal/server
// （传输层的两个包互不依赖）；端到端测试通过 servertest/testdaemon 子进程驱动真实的服务端。
package client
