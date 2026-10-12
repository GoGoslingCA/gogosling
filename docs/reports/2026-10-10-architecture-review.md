# 架构审查结论与阶段报告（2026-10-10）

> 范围：gogosling 的首轮实现（契约 → 两波并行模块实现 → 集成 → 审查 → 修复）。
> 验证基线：`go build ./... && go vet ./... && go test -race ./...`、`GOOS=windows/linux go vet ./...`、worker `ruff / mypy / pytest`、`npm run typecheck && npm test && npm run build`、`make e2e`（真实 Python 工作进程 + 假模型服务）均通过；GitHub Actions 三平台矩阵在 `8903381` 起转绿。

## 1. 审查结论

架构按 [docs/architecture.md](../architecture.md) 落地，三条不变量在代码与测试中可验证：

| 不变量 | 落地方式 | 验证 |
|---|---|---|
| 守护进程持有全部状态，前端只是客户端 | `internal/app` 组合根；桌面壳、Web、CLI 全部经本地 HTTP + SSE；CLI 不打开数据库，只读 `daemon.json` | `internal/app` 集成测试（假工作进程走通对话、审批、取消、冲突、锁、关闭）；`tests/e2e` |
| 密钥不入库、不进日志/事件 | `secret`（OS keychain / 加密文件，后端选择持久化）；provider / channel 记录只存引用；observability 脱敏 Handler；server 只返回 `has_secret` | provider / channel / server 测试断言响应与数据库不含密钥；e2e 断言数据库文件无明文密钥 |
| 副作用必经权限与沙箱，第三方代码进程外 | 工作进程只能经 `host.tool.call` 申请；`tools.Executor` 固定顺序 permission → 执行；MCP / 插件为子进程；`archcheck` 强制依赖方向 | permission 的硬拒绝/规则/授权/档位测试；tools 的拒绝转 `is_error`；archcheck 测试 |

依赖方向由 `internal/archcheck` 测试强制执行（越层导入即失败），已覆盖传输层不得导入 `store` 的规则。

**整体判断**：分层清晰、边界可执行、关键安全路径有测试覆盖；可以进入下一阶段（TeamAI 团队工作流、Linux/Windows 沙箱、IM 适配器扩展）。尚未在真实 Windows/Linux 机器上做人工冒烟，是当前最大的验证缺口（CI 的 Windows 矩阵只能覆盖自动化测试）。

## 3. 审查发现的问题与修正

审查由 6 个视角（安全不变量、并发与生命周期、契约一致性、分层与重复、跨平台、文档准确性）完成，共 82 条发现（7 high、26 medium、49 low）。按用户要求，发现的筛选与验证由架构负责人完成，未做多代理复核。全部 high 与 medium 已修复，low 修复了 29 条，其余 20 条列入待办。

### 3.1 high（7/7 已修）

| ID | 问题 | 修正 |
|---|---|---|
| DOC-01 | `auto_in_sandbox` 在无隔离的 Linux/Windows 上直接放行命令执行；不可信输入的下限不含 execute | 权限请求增加 `Isolated` 事实（只有 Seatbelt 下的 run_command 为真）；无隔离时 execute/network 退回 ask_risky；不可信输入下 execute 至少询问 |
| XP-03 | 路径规则区分大小写，macOS/Windows 上 `.SSH/id_rsa` 绕过 deny 规则 | darwin/windows 不区分大小写匹配；deny 规则全平台 fail-closed；Windows 长名规范化 |
| XP-05 | 桌面壳 `app://` 页面跨源访问回环 API，浏览器拒绝读取响应 | 主进程 `protocol.handle` 把 `app://gogosling/api/*` 代理到守护进程并注入令牌；令牌不再进入渲染进程 |
| XP-06 | `daemon stop` 只凭残留 `daemon.json` 的 pid 杀进程，PID 复用时误杀 | 先用令牌访问 `/version` 校验身份与 `started_at`，失败则只删残留文件 |
| XP-02 | Windows 上 `cmd /C` 的引号被 Go 转义，带引号的命令全部失败 | `sandbox.Spec.RawCommandLine` + `SysProcAttr.CmdLine` 原样传递 |
| XP-01 | Windows 发行版没有时区数据库 | `import _ "time/tzdata"` |
| C1 | 工作进程把空名工具调用写进历史，之后整个会话被解析器拒绝 | 缺名调用不产生 `tool_call` 片段；解析历史时宽松；增加"产出必须能被自己解析"的往返测试 |

### 3.2 medium（26/26 已修，节选）

- 运行状态机：`approval.*` 事件现在经 `session.NoteApproval` 进入状态机，`Run.status` 会显示 `awaiting_approval`（C2/CONC-1）。
- 删除智能体：先检查在途运行，级联删除会话并发布 `session.deleted`，调度器与网关内存状态随 `agent.deleted` 清理（C5）。
- 网关：按会话键推导路由（重启后仍能回复），投递 `push_text` 推送到 IM（LAY-05）；失败原因本地化而不是机器常量（C13）。
- 并发：引擎有界等待反向请求收尾（CONC-4）；未受理运行的取消收敛（CONC-3）；`ScopeAlways` 先决定后写规则并审计（CONC-2）；关闭分步预算（CONC-5）。
- 契约：Windows 优雅关闭端点 `POST /api/v1/system/shutdown`（XP-07）；设置返回 `effective_locale / effective_timezone`、未设置时不伪装成用户偏好（C3/LAY-01）；`Agent.tools` 三态语义（C4/DOC-02/DOC-04）；消息分页上限统一 500（LAY-02）；MCP `Headers` 真正生效（LAY-04）；凭据后端选择持久化、不静默切换（XP-11）。
- 跨平台：子进程不再弹控制台窗口（XP-08）；内核数据目录防护覆盖大小写与 `\\?\` 前缀（XP-04）；代理与证书环境变量透传给工作进程（XP-13）；桌面壳深链冷启动、Windows 自启动状态、内置扩展打包（XP-09/10/12）。

### 3.3 集成阶段由架构负责人直接修复的问题

- 系统提示重复注入：session 把 system 消息放进 `Messages`，工作进程又按 `agent.system_prompt` 注入一次（两边修复并加回归测试，协议写明只经 `agent.system_prompt` 传递）。
- `go.sum` 缺 MCP SDK 传递依赖；Windows 上 sandbox/engine 测试的平台假设与时序竞争；macOS runner 的 `LC_ALL` 干扰 locale 测试；Windows 8.3 短路径导致的规则路径比较失败。
- provider 对 `openai_compatible` 静默回填 `api.openai.com`（协议禁止）；engine 取消码在 session 侧按失败收尾。

## 4. 各模块完成情况

| 模块 | 状态 | 说明 |
|---|---|---|
| `model` / `event` / `config` / `version` | 完成 | 契约层；事件总线带回放与 `gap` |
| `store` | 完成 | SQLite 双池、嵌入式迁移、全部仓储；待：升级快照、保留期清理 |
| `observability` / `i18n` / `secret` / `identity` | 完成 | 脱敏日志、zh/en 语言包、keychain + 加密文件（后端持久化）、本机令牌与句柄文件 |
| `permission` / `sandbox` / `tools` / `workspace` | 完成（沙箱仅 macOS） | 规则引擎、审批代理、审计；Seatbelt；6 个内置工具 + `load_skill`；Linux bwrap / Windows Job Object 待实现 |
| `engine` + `worker` | 完成 | 工作进程监管、stdio JSON-RPC、推理循环、openai_compatible / anthropic / ollama、reasoning.delta；worker 的 memory / knowledge 仍是骨架 |
| `session` / `agent` / `provider` | 完成 | 运行状态机（含等待审批）、推送消息、模板库、供应商预设与连通性测试 |
| `scheduler` / `gateway` / `channel` | 完成（通道仅 console、telegram） | cron 解析与错过补偿；网关路由与回复分片；飞书、钉钉、企微、Discord 适配器待实现 |
| `extension` / `protocol/mcp` | 完成 | 技能 / 插件 / MCP 服务，工具摘要固定防 rug pull，内置资产播种；市场与签名待实现 |
| `server` / `client` / `app` / `cmd` | 完成 | 51+2 条路由、SSE、组合根、完整 CLI；`protocol/acp` 与 `update` 仍为包注释 |
| `apps/web` / `apps/desktop` | 完成（未在真机打包验证） | React UI；Electron 壳（代理、托盘、深链、守护进程生命周期） |
| 文档 | 完成 | 架构、AGENTS、OpenAPI、worker-rpc、19 份参考记录、本报告 |

## 5. 待办

**P0（下一迭代）**
1. Windows / Linux 真机冒烟：子进程无窗口、命令引号、时区、`daemon stop`、桌面壳打包件中的 `app://` 代理与 SSE。
2. Linux bwrap 沙箱与 Windows Job Object；`extension.ProcessRunner` 接入沙箱（插件子进程目前无 OS 级隔离）。
3. `protocol/acp`（双向）与 `update`（内核与运行环境升级、升级前快照）。
4. 随安装包分发 Python 运行环境（`electron-builder` 的 `resources/worker` 目前为空占位）。

**P1**
5. IM 适配器：飞书、钉钉、企业微信、Discord；入站附件下载；斜杠命令。
6. worker 的 memory / knowledge；多模态输入（`model.Part` 内容片段）；429/5xx 退避重试。
7. 调度器时区设置热更新；执行历史表；`job.updated` 事件。
8. 剩余 low 发现：LAY-06（组合根中少量领域规则下沉）、LAY-10（导出面收敛）、LAY-11（同形类型拷贝）、LAY-17（server 处理器中的校验下沉）、C7 文档、DOC-05（已改）、CLI 的附件上传 / 消息查看 / update 子命令。
9. 桌面壳：`SIGTERM` 回退等待与守护进程最坏关闭时长对齐；`launch()` 与 `stop()` 的竞态；托盘重启非本壳启动的守护进程的产品决策。

**P2**
10. 可观测性：OTel SDK 适配、按日期轮转；用量与费用统计。
11. 扩展市场：来源签名、撤销列表、压缩包 / URL 安装、插件常驻进程。
12. 多用户模式与目录服务接入点。

## 6. 规模

截至本报告：Go 约 35.7k 行实现 + 28.9k 行测试（707 个测试函数）；Python 工作进程约 3.5k 行 + 2.6k 行测试；Web 约 6.0k 行、桌面壳约 2.8k 行 TypeScript；16 次提交。

## 7. 工作流记录

- 第一波 12 个模块、第二波 2 个集成层任务由并行编码代理完成；每个任务按契约实现与自测。
- 架构负责人负责契约、集成对齐、全仓验证、审查发现的筛选与跨组收尾；审查发现的验证不再使用多代理复核。
- 关联文档：TeamAI 团队工作流的用户故事与 PRD 见 `docs/prd/team-workflow.md`，架构与流程设计见 `docs/design/team-workflow-architecture.md`（撰写中）。
