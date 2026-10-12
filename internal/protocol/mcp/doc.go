// Package mcp 实现 Model Context Protocol 的宿主侧能力（基于官方 go-sdk）。
//
// 职责：
//   - Client：以 stdio（子进程）或 streamable HTTP 连接一个 MCP 服务，发现工具并调用
//   - 把 MCP 的工具声明与调用结果转换成 model.ToolSpec / model.ToolResult，
//     让 extension 与 tools 包不必理解 MCP 的线格式
//   - ToolsDigest：对工具的名称、描述与参数 schema 计算稳定的 sha256，
//     供 extension.Manager 在首次启用时固定、之后每次连接时比对（防止服务端悄悄改写工具说明）
//
// 设计要点：
//   - 密钥不落盘：MCPServerConfig.Env 与 Headers 里的 "secret:<ref>" 由调用方提供的 EnvResolver 在
//     连接那一刻解析，解析结果只进子进程环境或 HTTP 请求头，不进日志、不进返回值。
//   - 子进程环境按 config.ChildEnvAllowlist（全仓唯一定义：PATH、HOME、代理与证书变量等）构造，
//     不透传守护进程的其他环境变量。
//   - HTTP 传输只允许 https；明文 http 仅限回环/局域网地址，避免把令牌发到公网。Headers（例如
//     Authorization）由自定义 RoundTripper 注入，只发往与配置端点同 scheme、同 host 的请求，
//     重定向到别的主机时不会带上；ValidateConfig 校验键名合法、拒绝传输自管的头与含换行的值，
//     stdio 传输配置 Headers 直接拒绝而不是静默忽略。
//   - Windows 上 stdio 子进程以 CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP 启动，守护进程没有控制台时
//     也不会为它弹出窗口。
//   - 每次调用都有超时（默认 60s），结果有大小上限（默认 1 MiB），超限截断并注明。
//   - 工具风险等级默认 network；服务端的注解只用于"收紧"（显式 destructiveHint → destructive），
//     永不用于放宽，因为注解来自不可信的第三方。
//   - 关闭 stdio 连接时先关 stdin 等待退出，再 SIGTERM / SIGKILL；POSIX 上子进程在独立进程组，
//     收尾时整组结束，不留孤儿。
//
// 待实现：
//   - 资源与提示词的发现与按需加载
//   - 用户确认类请求（elicitation）与界面的对接
//   - 远程服务的 OAuth 授权流程
//   - 可选：把 gogosling 自身的能力以 MCP 服务形式对外提供
//
// 边界：只处理协议；哪些服务被启用、属于哪个智能体、调用是否需要审批，由 extension 与 permission 决定。
package mcp
