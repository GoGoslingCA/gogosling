// Package channel 管理 IM 通道的配置记录与凭据，是本地 API 通道端点背后的服务。
//
// 职责：
//   - 通道的创建、读取、列表、部分更新、删除；校验类型（KindChecker）、名称与非敏感配置
//     （Options.ValidateConfig，由组合根接到网关的解析规则）。
//   - 凭据只写入凭据存储（Secrets），名称固定为 "channel:<id>:secret"；记录里只保存这个引用名，
//     返回给调用方的记录永远不含凭据。删除通道时一并删除凭据。
//   - 每次写操作之后让网关重载该通道（Gateway.Reload；Options.Gateway 为 nil 表示通道功能未启用）；
//     重载失败不回滚写入，失败原因由网关写入 Channel.LastError，并通过 Status 报告。
//   - Status 把网关的运行时状态（Gateway.ChannelStatus）映射为契约中的 stopped / starting / running / failed。
//
// 错误：Secrets 的实现（组合根的 secretAdapter）负责把 secret 包的哨兵错误映射为 model.Code；
// 本包只处理带 model.Code 的错误（不存在 = CodeNotFound），不再导入 secret 包。
//
// 边界：本包只导入叶子包 model；不导入 gateway 与 server——网关能力通过本包定义的 Gateway、
// KindChecker 接口注入，server.ChannelService 由组合根 internal/app 用一个薄适配器转接到 Service。
//
// 并发：Service 并发安全；所有写操作（创建、更新、删除、重载）由一个全局互斥锁 writeMu 串行化，
// 避免凭据轮换与记录更新交错；读操作不加这把锁。
package channel
