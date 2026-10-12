// Package extension 是统一的扩展宿主：技能（skill）、插件（plugin）与 MCP 服务（mcp_server）
// 的安装、启用、停用、卸载，以及把它们的能力（工具、技能）汇总给智能体。
//
// 三种扩展共用一条记录（model.Extension）、一个目录布局（<dataDir>/extensions/<id>/）、
// 一套启用开关（全局 Enabled + Agent.Extensions 按智能体选用）与一种来源标记（Source），
// 而不是技能、插件、连接器各自一套机制。
//
// 职责：
//   - InstallSkill：校验 SKILL.md（YAML frontmatter，必填 name / description）、复制、计算摘要、写记录
//   - InstallPlugin：校验 extension.json（name / version / kind / entry / permissions / tools），同上
//   - RegisterMCP：登记一个 MCP 服务的连接配置（Env 与 http 传输的 Headers 里只保存 "secret:<ref>" 引用，
//     不保存密钥；配置被深拷贝后落库）
//   - Enable / Disable / Uninstall / List / Get：生命周期；Enable 时核对磁盘摘要（插件）或固定工具摘要（MCP）
//   - ToolsFor / CallTool：汇总智能体启用的 MCP 与插件工具，并按名称把调用分发到对应扩展
//   - SkillsFor / LoadSkill：渐进式披露——系统提示只放名称与描述，正文按需加载
//   - ReadSkillResource：只读技能的引用文件与脚本文本，限制为 1 MiB 内的普通 UTF-8 文件；
//     os.Root 约束安装目录边界，拒绝所有符号链接，绝不执行脚本。
//   - SeedBundled：首次启动时安装随发行包分发的内置扩展，之后按摘要判断是否升级
//
// 安全要点：
//   - 第三方代码一律在内核进程之外运行：插件工具是"每次调用启动一次 entry 子进程、stdin 一行请求、
//     stdout 一行响应"的 JSON 协议；MCP 服务是子进程或远程 HTTP。内核进程内不加载任何第三方代码。
//   - 子进程只拿到环境变量白名单（config.ChildEnvAllowlist，全仓唯一定义）；Windows 上插件子进程以
//     CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP 启动，不会每次调用闪出控制台窗口；
//     插件的 permissions 声明随 ProcessSpec 交给 ProcessRunner，
//     集成阶段由 sandbox 包的实现强制执行；工具的风险等级由声明推导，只会据此收紧审批、不会放宽。
//   - MCP 工具的名称、描述与 schema 在首次启用时固定（Digest）；之后连接若发现变化，该服务的工具
//     整体不再暴露给模型（事件 extension.tools_changed），直到用户调用 AcceptTools 重新确认。
//   - 安装目录由内核生成（ULID），不使用清单里的名称做路径；卸载只删除位于 <dataDir>/extensions/ 之下、
//     且与记录 ID 一致的目录；安装时拒绝符号链接、限制文件数与总大小、先写临时目录再原子改名。
//   - 密钥不进记录、不进日志、不进事件；MCPServerConfig.Env 与 Headers 的值只允许是字面量或
//     "secret:<ref>"，由 protocol/mcp 在连接时经 EnvResolver 解析（Headers 由 RoundTripper 注入请求）。
//
// 并发：Manager 并发安全。MCP 连接按扩展缓存并按需重连；插件工具每次调用独立进程，天然隔离。
//
// 待实现：
//   - 来源校验：签名、哈希固定、撤销列表；扩展目录（市场）的索引协议
//   - 从压缩包 / URL 安装（当前只支持本地目录与内置资产，避免无上限下载）
//   - 连接器的 OAuth 授权流程与令牌刷新（静态鉴权头已由 MCPServerConfig.Headers 支持）
//   - 插件的常驻进程模式（当前每次调用启动一次，简单且隔离，但对冷启动慢的运行时不友好）
//
// 边界：不做权限判定与审计（permission / tools 包的职责）；协议细节在 protocol/mcp。
package extension
