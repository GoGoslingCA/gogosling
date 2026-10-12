# gogosling 架构

> 本文是架构的权威描述。实现与本文冲突时，以本文为准并修正实现；若本文需要改动，先改文档再改代码。

## 1. 一句话

**薄壳 + 独立守护进程 + 版本化协议。** 业务状态全部在 Go 守护进程 `gogoslingd` 里；桌面壳、Web UI、CLI 都只是它的客户端；智能体推理循环运行在守护进程监管的 Python 工作进程中。

## 2. 进程模型

```
┌───────────────────────────┐   ┌───────────────────────────┐   ┌──────────────┐
│ apps/desktop (Electron)   │   │ apps/web (React, 同一份)  │   │ cmd/gogosling│
│ 托盘·窗口·自启动·更新    │   │ 浏览器或壳内渲染          │   │ CLI          │
└─────────────┬─────────────┘   └─────────────┬─────────────┘   └──────┬───────┘
              │ 本地 HTTP + SSE（Bearer token，仅回环地址）             │
┌─────────────▼─────────────────────────────────────────────────────────▼───────┐
│ cmd/gogoslingd  ——  internal/app 组合根                                      │
│  server ─► identity ─► { agent · session · provider · workspace · scheduler } │
│                        permission ─► tools ─► sandbox                        │
│                        extension ─► protocol/mcp                             │
│                        gateway（IM 通道）      engine（工作进程监管）        │
│  基础层： store(SQLite) · secret(keychain) · event · observability · i18n     │
└───────────────────────────────────────────┬──────────────────────────────────┘
                                            │ stdio · 行分隔 JSON-RPC 2.0（api/worker-rpc）
                                ┌───────────▼───────────┐
                                │ worker (Python)        │
                                │ runtime · providers    │
                                │ tools 声明 · memory    │
                                │ knowledge              │
                                └───────────┬───────────┘
                                            │ HTTPS
                                       模型服务 / 本地模型
```

- **守护进程脱离窗口存活**：定时任务与 IM 通道要求如此。桌面壳负责拉起、发现与重连，不拥有状态。
- **工作进程是可替换的引擎**：内核只依赖 `internal/engine` 定义的接口和 `api/worker-rpc` 协议；今后可以换成 Go 实现或另一种 Python 框架。
- **副作用不在工作进程里发生**：工作进程只"声明"工具调用；真正的文件、命令、网络操作由守护进程在权限判定与沙箱之后执行（反向请求 `host.tool.call`）。

## 3. 代码布局

```
cmd/gogoslingd        守护进程入口（参数、日志、信号）
cmd/gogosling         CLI 入口（HTTP 客户端，不碰数据目录）
internal/app          组合根：构造模块、生命周期、运行时句柄文件
internal/server       本地 HTTP API + SSE（契约 api/openapi）；servertest/ 子包是测试装配（唯一允许直接打开 store 的传输层代码）
internal/client       Go 版 API 客户端（CLI 与集成测试复用）
internal/channel      IM 通道的管理服务（凭据只进 secret；经组合根适配为 server.ChannelService）
internal/archcheck    依赖方向的自动化测试（本文 §4 的可执行版本）
tests/e2e             端到端测试（构建标签 e2e：真实 Python 工作进程 + 假模型服务）
internal/identity     访问令牌、Principal
internal/permission   规则引擎、审批代理、审计
internal/tools        内置工具的声明与执行（经 permission、workspace、sandbox）
internal/sandbox      OS 级隔离执行
internal/agent        智能体定义、模板库
internal/session      会话、消息、运行状态机
internal/provider     供应商与模型配置、连通性测试、本地模型发现
internal/workspace    工作区文件访问与路径约束
internal/workbench    项目与材料索引、可恢复草稿、文本成果与不可变版本；复用会话与工作区能力
internal/engine       Runtime 接口 + Python 工作进程监管 + JSON-RPC 客户端
internal/scheduler    定时任务与错过补偿
internal/gateway      IM 通道适配器接口与路由
internal/extension    技能、MCP 服务、插件的安装与启用
internal/protocol/mcp MCP 宿主侧实现（官方 go-sdk）
internal/protocol/acp ACP 双向接入
internal/store        SQLite 持久化、迁移、仓储实现
internal/secret       凭据存储（OS keychain，回退加密文件）
internal/event        进程内事件总线（Seq、回放）
internal/observability 日志、追踪、指标
internal/i18n         内核侧文案（zh / en / fr）
internal/update       内核与运行环境升级
internal/model        共享领域类型、错误码、仓储接口（叶子）
internal/config       数据目录与进程配置（叶子）
internal/version      构建信息（叶子）
worker/               Python 工作进程（uv 项目）
apps/web              React + TypeScript + Vite
apps/desktop          Electron 壳
api/openapi           本地 HTTP API 契约（OpenAPI 3.1）
api/worker-rpc        守护进程 ↔ 工作进程协议
extensions/bundled    随发行包分发的内置扩展（内容资产，不是代码）
docs/                 文档
```

## 4. 依赖规则

依赖只能**向内**流动。下表中每一行只能导入它右侧列出的包（以及标准库与批准的第三方库）。

| 层 | 包 | 可导入 |
|---|---|---|
| 叶子 | `model` `config` `version` `event` `i18n` `observability` `secret` | 标准库与第三方库；**不导入任何 `internal/` 包**（`secret`、`i18n` 因此用哨兵错误 / 镜像列表，由调用方映射到 `model.Code`） |
| 基础 | `store` | `model` |
| 领域 | `agent` `session` `provider` `workspace` `workbench` `permission` `scheduler` `extension` `protocol/*` `tools` `sandbox` `engine` `gateway` `identity` `channel` `update` | 叶子层 + 同层中**下表允许的**包 |
| 传输 | `server` `client` | 叶子层 + 领域层；**不得导入 `store`**（`server/servertest` 测试装配例外） |
| 组合根 | `app` `cmd/*` | 任何包 |

领域层内部允许的依赖（箭头为"导入"）：

```
tools      → permission, workspace, sandbox, extension(只用 ToolProvider 接口)
engine     → (无领域依赖；通过接口回调 tools 与 session)
session    → agent, provider, engine(接口), permission(接口)
scheduler  → session(接口)
extension  → protocol/mcp
gateway    → session(接口)
permission → secret? 否。permission 不依赖其他领域包。
workbench  → (无领域依赖；通过组合根注入的接口复用会话、工作区等能力)
```

**硬性禁止**

- `model`、`config`、`event`、`secret`、`i18n`、`observability` 不得导入任何 `internal/` 下的非叶子包。
- `store` 只能导入 `model`；领域包不得导入 `store`（通过 `model.Store` 等接口使用）。
- 领域包不得导入 `server`、`client`、`app`。
- `worker/` 不读写数据目录、不监听端口、不持久化密钥。
- `apps/*` 只通过本地 HTTP API 访问内核，不直接打开数据库或工作区。
- 任何包都不得把凭据明文写入 `store`、日志或事件。

依赖方向由 `internal/archcheck` 的测试强制执行：它解析 `cmd/` 与 `internal/` 下所有文件的 import，对照上表，越层导入会让 `go test ./...` 失败。

## 5. 核心数据流

### 5.1 一次对话

1. 客户端 `POST /api/v1/sessions/{id}/messages`，`server` 校验令牌与 Origin，调用 `session.Service.Send`。
2. `session` 追加用户消息，创建 `Run(queued)`，发布 `message.created`，把运行交给 `engine.Runtime.StartRun`（携带 Agent、消息历史、`ProviderCredentials`、可用 `ToolSpec`）。
3. `engine` 通过 JSON-RPC `run.start` 交给工作进程；工作进程流式回传 `run.event` 通知，`engine` 转成 `model.RunEvent` 并发布到 `event.Bus`；`session` 订阅并持久化状态变化。
4. 工作进程需要调用工具时发出反向请求 `host.tool.call`；`engine` 调用 `tools.Executor`，后者先经 `permission.Check`（可能挂起等待审批，发布 `approval.requested`），再在 `sandbox` 内执行，把 `ToolResult` 返回工作进程。
5. 运行结束：`session` 写入助手消息、更新 `Run` 终态与用量，发布 `run.completed`。
6. 客户端全程通过 `GET /api/v1/events?since=<seq>` 的 SSE 接收事件；断线后带上最后的 Seq 重连。

### 5.2 定时任务

`scheduler` 到点后解析目标会话键，调用 `session.Service.Send(trigger=job)`；设备休眠导致错过时按 `MissedPolicy` 处理。

### 5.3 IM 消息

`gateway` 适配器把平台消息归一化为 `(session key, text, attachments, principal)`，经 `session.Service.Send(trigger=channel)` 处理，回复通过同一适配器送回。入站内容一律视为不可信数据。

### 5.4 工作台持久状态

- Project 只组织同一 Agent 的会话与材料，不创建独立文件权限边界。Session 的可选 `project_id` 不改变 ID、Key、Origin 或运行状态；旧会话保持未归入项目。项目归档不停止已有会话。
- 项目材料复用 Agent 工作区 `inbound/` 上传，数据库保存名称、路径、摘要与归属。每次消息明确选择引用，发送前由内核校验归属和当前内容；勾选材料不等于授予或收紧工具文件权限。
- Draft 由内核保存，按鉴权主体与作用域唯一，更新和删除必须检查乐观版本。正文不进入浏览器或 Electron 的 UI 偏好文件。跨窗口冲突返回给客户端，不能静默覆盖。
- Artifact 引用真实助手消息，每次保存追加不可变 Revision。成果与草稿正文存 SQLite；版本列表不携带正文，读取与下载走鉴权 HTTP API。保存版本不等于覆盖工作区文件。
- `model.WorkbenchStore` 是独立仓储集合，提供 `WorkbenchTx`；原 `model.Store` 的接口集合不因此扩张。关联与版本的条件更新由 store 在事务内保证。
- P3B 使用独立持久 Operation 作为人工文件操作主体；审批只能绑定真实 Run 或真实 Operation 之一，不伪造模型运行。应用和撤销都经 tools.Executor 与现有 permission guard/deny/单次审批，操作不向模型提供。ChangeSet 绑定主体、成果版本、目标、工作区身份、前像及过期摘要；应用不接受新正文。
- 单文件 UTF-8 变更的前像、提议和执行意图存受保护数据库。支持创建、替换，以及独立预览的撤销；撤销新建文件按 destructive 审批。工作区用描述符解析、同目录保留原件和 no-replace 安装，发现外部写入不覆盖新目标。Linux/macOS 提供受控实现，未支持的平台或文件系统拒绝原位修改并保留导出入口。
- 文件与数据库不能跨介质原子提交。真实文件结果先持久记录，原件清理完成后才终结操作；清理失败也进入可见的 recovery_required。重启核对完整目录、inode、权限及内容身份；只有保留日志或已持久核验的安装后身份完全匹配才补成功账。日志与安装后身份均缺失时，即使正文相同也不能认定应用成功。目标仍完整匹配前像则认未应用；必要时仅以 no-replace 将保留原件恢复至空缺目标，其余保留恢复状态。未确认恢复的操作禁止继续写入。等待审批不持有数据库事务或文件锁；应用期间阻止同 Agent 的模型工具并发，存在活跃模型运行时拒绝开始文件副作用。
- 此实现不宣称对任意同 UID 外部进程提供原子 CAS 或安全隔离；同 UID 进程持有旧文件描述符或主动移动父目录仍需检测和人工恢复。不能把 hash+rename、目录0700或前端确认当成该保证。

### 5.5 本地资源调度

桌面组合根默认最多两个并行运行，待执行队列最多32项；用户可在1–8之间调整并行数。已有同会话互斥继续生效，同一实际工作区的任务串行派发。用户发起的任务优先于定时任务，等待超过30秒的任务获得提升，避免后台任务长期无法执行。排队不创建Python任务，不预读整段历史，不保留凭据副本；开始执行时重新解析配置。取消排队不启动模型，降低上限不杀死正在执行的任务。

正文及执行结果仍由内核持久化，客户端按会话订阅并合并展示增量；审批及终态不等待展示定时器。已落库且未被页面订阅的实时终态缓存才可释放。任何性能优化不得省略审批、丢弃未保存内容或用百分比模拟执行进展。节能和速度比较需要同负载实测，队列上限本身不等于节能百分比。

### 5.6 发送前的提示词优化

输入区的优化模块按点击加载，通过可选能力 `prompt.enhance` 发现内核接口。优化只携带本次文字和内部指导词，复用当前模型及 worker RPC；临时推理身份不创建业务 Session/Run，不发布聊天事件，也没有工具权限。它共享执行槽预算，全局最多一个，取消跟随请求，终态后释放资源。结果直接回填既有草稿并提供还原；用户选择调整时才打开修改意见弹窗。内部指导词不进前端资源、日志或业务持久状态。

### 5.7 内容预览

工作台预览由 `internal/workbench/preview` 注册并调用独立格式模块，接口及目录见 [内容预览架构](design/content-preview-architecture.md)。组合根注入受控只读工作区句柄，模块不依赖 server、store 或推理 worker。内容解析单并发、有界读取；原文件只经鉴权 API 输出。前端只挂载当前展示器，关闭时取消请求并回收 Blob URL。HTML 在清理活跃内容后的无权限 iframe 中展示，CSP 拒绝外部资源。

旧 Office 和版式预览依赖本地 LibreOffice 及受限读取、禁止联网的转换沙箱，不降级为在线转换或无隔离进程。当前转换适配器面向 macOS；其他平台保留内置内容阅读与下载。预览只读，不改变 P3B 应用修改、撤销、冲突检查和审批。

## 6. 安全不变量

1. **回环地址不是安全边界。** 本地 API 只绑定 `127.0.0.1`；除 `/healthz` 外全部需要 Bearer 令牌；校验 `Host` 与 `Origin`（仅允许无 Origin、桌面壳自定义协议与开发服务器白名单）。
2. **凭据永不落库。** 模型密钥、通道密钥、OAuth 令牌只存 `secret`（OS keychain）；数据库与配置里只有引用名；工作进程通过 RPC 在内存中接收一次性凭据。
3. **副作用必经审批与沙箱。** 工作进程无权直接碰文件系统、Shell 与网络；所有工具执行走 `permission → tools → sandbox` 链路并写审计。
4. **不可信输入标记来源。** IM 消息、网页内容、文件内容进入上下文时附带来源，权限规则可按来源收紧（同一会话内"不可信输入 + 敏感数据 + 对外通信"三者不同时自动放行）。
5. **第三方代码进程外运行。** 插件与 MCP 服务是子进程；内核进程内不加载第三方代码。
6. **升级保留恢复点。** 现有数据库有待执行迁移时，store 在业务开放前用 SQLite 一致快照备份旧模式，核对完整性、版本和摘要；备份失败则拒绝迁移。新空库无需备份。禁止降级到低于数据目录版本的内核；恢复旧内核须先停写、保留当前数据，再恢复匹配的已核验快照，不能自动丢弃升级后的工作。

## 7. 数据目录（默认 `~/.gogosling`，环境变量 `GOGOSLING_HOME`）

```
config.json              进程配置（可选）
gogosling.db(-wal,-shm)  SQLite 控制面（WAL）
daemon.json              运行时句柄：{pid, endpoint, token, version, started_at}，权限 0600；退出时删除
daemon.lock              单实例文件锁（内含 pid）
secret.backend           首次选定的凭据后端（keyring | file），之后不静默切换
secrets.enc / secret.key 无 keychain 时的加密回退存储与其密钥（均 0600）
security/rules.json      权限规则（首次使用时从内置默认落盘；ScopeAlways 的决定写入此处）
agents/<id>/             智能体工作区（inbound/ 存附件）
extensions/<id>/         已安装扩展（ULID 目录名）
logs/gogoslingd.log      日志（按大小轮转、旧文件 gzip）
backups/                 升级前快照
```

所有文件与目录只对当前用户可见（文件 0600、目录 0700）；工具调用中引用这些内核文件会被权限引擎硬拒绝。

迁移快照位于数据库所在目录的 `backups/`，使用 SQLite `VACUUM INTO` 包含 WAL 中已提交的数据，配套 JSON 清单记录源/目标 schema、文件名、UTC 时间和 SHA-256。它不等于自动恢复工具，也不回滚用户工作区文件。Project、Draft、Artifact/Revision 的业务记录均在数据库；Electron userData 只保存窗口位置等非业务界面偏好。

## 8. 契约

| 契约 | 位置 | 版本策略 |
|---|---|---|
| 本地 HTTP API | `api/openapi/openapi.yaml` | 路径前缀 `/api/v1`；破坏性变更升 v2 |
| 守护进程 ↔ 工作进程 | `api/worker-rpc/protocol.md` | `worker.hello` 协商 `protocol_version`，不兼容则拒绝启动 |
| 事件流 | OpenAPI 中的 `Event` + `model.RunEvent` | 只增事件类型，不改既有字段语义 |
| 错误 | `model.Code` ↔ HTTP 状态 ↔ `i18n` 键 `errors.<code>` | 只增不删 |

## 9. 已做出的决策（ADR 摘要）

- **ADR-001 守护进程优先**：UI 是客户端而非宿主。理由：定时任务与 IM 通道需要脱离窗口存活；多个前端复用一套 API。
- **ADR-002 Go 内核 + Python 工作进程**：Go 负责控制面、网关、调度、沙箱、更新与分发（单二进制、交叉编译）；Python 保留智能体框架与 ML/RAG 生态。运行环境随安装包分发，不在首次启动时联网安装。
- **ADR-003 Electron 壳**：三平台渲染一致、生态成熟；壳内不放业务逻辑，故可替换。
- **ADR-004 SQLite 单文件控制面**：本地优先、零运维；多机同步属于后续能力，不改变本地数据模型。
- **ADR-005 工具在内核执行**：安全边界只有一处，便于审计、审批与沙箱。
