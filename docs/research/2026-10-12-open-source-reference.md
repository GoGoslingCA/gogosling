# 开源参考与 gogosling 的落地选择

核查日期：2026-10-12。选择 Goose、OpenCode、AionUi，分别参考通用本地 Agent、编程内核与多引擎桌面工作台。保留 gogosling 的 Electron/React → Go 内核 → Python 推理工作进程，不整体搬入其他项目。

检索包含 open、closed Issues 和 PR，关键词为 memory、memory leak、performance、conversation、permission、keyring、SSE、cache；再读取具体条目、讨论和 PR diff。不是全量缺陷审计，也未在本机复现竞品报告的内存数字。状态快照见 [opensource-evidence.json](opensource-evidence.json)。

## 架构与关键模块

- **Goose**：Rust 核心分出 Provider、MCP 扩展、Config/Recipes，桌面端 Electron，CLI 和其他客户端通过服务层接入。`agent.rs` 将推理、工具审批、执行、重试、压缩和子任务组成状态机操作。对我们的启发是保持能力接口与权限边界，把新能力放入独立模块，避免让桌面壳变成执行内核。[架构说明](https://github.com/aaif-goose/goose/blob/main/CUSTOM_DISTROS.md)、[Agent 实现](https://github.com/aaif-goose/goose/blob/main/crates/goose/src/agents/agent.rs)
- **OpenCode**：当前检索到的 2.0.27 源码已拆成 `core/server/protocol/cli/app/desktop` 等包；core 对 SQLite、PTY 等区分 Bun、Node、workerd 实现，server 显式依赖核心和协议。历史 1.x Issue 中的 `packages/opencode` 路径已不能直接当当前路径。我们继续以 Go 接口和 worker RPC 分离运行时，平台差异放到适配层，不以更换语言来推断性能一定更好。[仓库配置](https://github.com/anomalyco/opencode/blob/dev/package.json)、[core](https://github.com/anomalyco/opencode/blob/dev/packages/core/package.json)、[server](https://github.com/anomalyco/opencode/blob/dev/packages/server/package.json)
- **AionUi**：Electron 主进程和 React 渲染进程通过 IPC 分离；消息层分页读取，通过消息、工具调用和权限调用 ID 建索引，并用 WeakMap 关联消息列表。适合借鉴多引擎接入和统一展示，但我们保持服务端 Seq 去重，不新增多套推测性对账索引。[进程边界](https://github.com/iOfficeAI/AionUi/blob/main/AGENTS.md)、[桌面包](https://github.com/iOfficeAI/AionUi/blob/main/packages/desktop/package.json)、[消息实现](https://github.com/iOfficeAI/AionUi/blob/main/packages/desktop/src/renderer/pages/conversation/Messages/hooks.ts)

## 逐项结论、影响与建议

### 1. 回收完成任务，要先确认结果已保存

[Goose PR #9514 — fix(summon): evict stale completed tasks to prevent unbounded memory growth](https://github.com/aaif-goose/goose/pull/9514)，**已合并**。未被父 Agent 领取的结果原先会在 Map 中累积；修复以完成时间为起点设置 TTL。

**对我们**：原实时视图只在打开对应聊天并读到历史消息时清理，未访问的已完成任务可能常驻。**已落地**：已确认助手消息落库且页面未订阅时释放实时正文；退出页面清理订阅与快照。未保存的失败输出继续保留，不直接照搬 TTL 删除结果。后续若做子 Agent 委派，同样先持久结果再释放执行对象。

### 2. 流式事件不能驱动整个 App 重绘

[OpenCode #16697 — Multiple memory leaks cause unbounded RAM growth during extended TUI usage](https://github.com/anomalyco/opencode/issues/16697)，**closed/completed**；关联 [PR #16695](https://github.com/anomalyco/opencode/pull/16695) **closed、未合并**，讨论显示因长期无更新关闭，不能据此声称修复已发布。报告涉及监听未释放、会话缓存和定时器等多个来源。

**对我们**：原 React 全局实时状态在每个 token 后更新，背景任务会带动当前聊天。**已落地**：按 session 订阅，显示增量合批；可见页约80ms、隐藏页约300ms，缓冲达到64K字符或256段立即刷新。审批、工具状态、终态先刷新文字再立即处理；断线重放按 Seq 去重，内核重启重置序号。合成验证中，8000条交错增量完整保留，单会话通知80次；这是通知次数验证，不是 CPU 或能耗降幅。

### 3. 展开前不创建大段工具结果，稳定消息不重复解析

[AionUi PR #4141 — perf(sidebar): memoize conversation rows](https://github.com/iOfficeAI/AionUi/pull/4141)，**open、未合并**。它利用稳定 props，让无变化的会话行跳过重绘。

**对我们**：原消息列表每个增量重建结果索引并重渲染历史 Markdown；折叠工具正文仍进入 DOM。**已落地**：结果索引和消息组件复用，成果回调保持稳定，工具详情和思考正文展开后才挂载。后续大型文件预览应按需加载，不在启动时导入 Office/媒体全套依赖。

### 4. 切换会话时，要分清“用户在翻历史”和“跟随新消息”

[AionUi #2196 — Switching conversations sometimes causes chat pane flicker and does not auto-follow the latest messages](https://github.com/iOfficeAI/AionUi/issues/2196)，**open**；[PR #4140 — cache messages across navigation](https://github.com/iOfficeAI/AionUi/pull/4140) **open、未合并**。讨论明确只部分改善，滚动行为仍需报告者复验。

**对我们**：已有按会话挂载的消息列表和底部跟随判断，不能为减少渲染而删除这条交互边界。**已落地**：历史抽屉打开后才查询完整列表；延迟返回不会带入旧成果目标。**下一步**：长会话窗口化前补齐滚动锚点、向前翻页、搜索跳转三类浏览器验证，避免直接套用固定200条裁剪。

### 5. 多 Agent 需要统一调度，窗口事件需要明确归属

[AionUi PR #4222 — make conversation view multi-instance safe](https://github.com/iOfficeAI/AionUi/pull/4222)，**open、未合并**。方案给广播携带 conversation/window ID，处理全局“当前会话”单例带来的误路由；[Goose #9514 的后台任务模块](https://github.com/aaif-goose/goose/pull/9514)同时说明子任务对象有独立生命周期。

**对我们**：同会话互斥不足以限制多个 Agent 总占用。**已落地**：全局默认2个执行槽、最多32个等待项，可调1–8；同一解析后的工作区串行，前台优先、等待30秒后提升；审批占槽，取消排队不启动引擎。等待时不预读完整历史、不保留凭据副本。并行上限不是 CPU 调度器；模型内部算力由供应商决定，嵌套工作区和不合作的外部进程仍须依赖文件冲突检测。

### 6. 上游挂流必须释放，但关闭界面不应误停已受理任务

[OpenCode #36739 — opencode serve leaks memory and wedges when an upstream SSE endpoint hangs](https://github.com/anomalyco/opencode/issues/36739)，**closed**；末条讨论为60天无活动自动关闭，**未找到已合并修复证据**。报告者描述响应头返回后无正文、重试叠加连接的情况。

**对我们**：要区分展示 SSE 与推理请求。切换页面或关闭展示流不能中断本地已受理任务；用户停止、总超时、工作进程退出必须传递取消。现有 worker 有读超时和取消关闭流，组合根运行总上限1小时。本轮保留并跑过 provider/worker/端到端检查。**后续验收**：增加真实本地服务器“只发头不发正文”的连接数与取消延迟测试，不能只用抛出 ReadTimeout 的 mock 推断所有连接已释放。

### 7. 磁盘治理应按数据类别，不能自动删用户成果

[OpenCode PR #16730 — reduce memory usage and database bloat in long-running instances](https://github.com/anomalyco/opencode/pull/16730)，**closed、未合并**。方案含大输出落盘、WAL维护、压缩旧工具输出和归档保留期限，是参考提案而非当前产品保证。

**对我们**：成果版本、撤销前像、审计与缓存不是同一种数据。现有文件差异分页、工具输出上限、路由懒加载继续保留。**下一步**：先做只读存储统计；临时预览可重建，成果版本先内容去重；恢复中的前像绝不清理。用户确认的历史保留策略与后台空闲维护再单独实现。不开启默默删除90天归档的默认策略。

### 8. 设置必须在重启后仍然有效，平台兼容单独验收

[Goose #6287 — GOOSE_DISABLE_KEYRING not working](https://github.com/aaif-goose/goose/issues/6287)，**closed/completed**；[PR #8219 — honour GOOSE_DISABLE_KEYRING from config.yaml at startup](https://github.com/aaif-goose/goose/pull/8219)，**已合并**。根因是配置写入后启动只看环境变量；讨论还涉及无头Linux、本地无密钥模型与 URL 差异。

**对我们**：资源上限“保存成功”必须同时代表内核应用和下次启动可恢复。**已落地**：设置持久化、非法旧值回落、旧配置默认2、旧内核隐藏不支持入口的测试。密钥仍经现有 secret 服务，不因兼容问题回退明文日志或数据库。P3B 当前 macOS 实测，Linux/Windows 已有编译/不支持路径覆盖；Windows 原位文件修改仍使用明确的导出回退，不宣称已完成原生写入验收。

## 下一批改进顺序

1. 实际任务负载记录峰值 RSS、CPU时间、首个输出延迟、取消后活动连接/进程数；同一设备、模型和输入比较，公开样本及失败次数。
2. 只读磁盘统计与可重建缓存清理，再做成果内容去重。历史与恢复数据采用不同策略。
3. 长会话按窗口展示、重型格式预览按需载入；保留全文检索、导出和旧成果入口。
4. 多 Agent 真正委派时复用同一预算与等待队列，父任务等待子任务不得持槽造成死锁；本轮没有新增自动委派工具。
