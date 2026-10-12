# 工作台重构实施交付记录

本文记录 P3A 交付时点；后续用户已授权并推进 P3B，最新状态见 [P3B 与样式统一交付](2026-10-12-p3b-delivery.md)。

日期：2026-10-12。状态：本地实现和验证完成，未提交、未推送、未发布安装包。基线为 `b8bbd0d`，保留仓库中原有的两份 team-workflow 未提交文档修改。

用户已在本次会话明确授权工作台 P3A 的共享模型、仓储接口、OpenAPI 和架构文档变更。文件覆盖、原子应用与磁盘撤销作为 P3B 后置，没有启用未完成接口或占位按钮。

## 已实现的完整流程

1. 开始页与任务页共用输入组件。首页先选文件，再一次完成创建、上传和发送；空任务仍可直接新建。输入法、Enter/Shift+Enter 行为一致。
2. 常驻最近任务、固定、搜索、重命名、归档和恢复保留旧路由。右键菜单与键盘可操作；配置入口仍在可折叠分组中。
3. 项目固定属于一个助手，组织资料、任务和成果。资料上传不自动全选；本次勾选明确带入消息。旧任务可以原位关联项目，ID、Key 与消息历史不变。
4. 输入区集中显示助手/模型、工作目录、助手的审批/自治模式、资料引用及当前轮次 token 用量。没有用量时显示未上报，不捏造费用。
5. 运行展示基于真实 Run/SSE、工具调用及结果。首条目标与最近补充可检查；恢复状态读取期间不伪称运行或完成。审批同时标明所属任务。
6. 用户明确选择“保存初稿并编辑”，才建立正式成果。支持完整文档和独立代码片段；语法预览、编辑、差异、导出与副本保存继续保留。
7. 正式成果版本不可变，修改保存追加新版本。历史预览不修改当前草稿；使用历史内容恢复也生成新版本。并发版本冲突需明确选择，不静默覆盖。
8. 输入与成果草稿由守护进程持久保存，按主体与作用域隔离。自动保存只有在服务成功确认后才显示完成；冲突保留本地和远端内容。发送完成仅清理发送时的草稿版本。
9. 开始任务阶段保存已创建会话和已上传资料的检查点；失败续传不重复创建。创建/发送响应未知时先核对历史，不自动重发。迟到回调不抢走用户当前页面。
10. 桌面窗口尺寸、最大化状态，成果宽度、主题、固定任务和最后打开位置可恢复。客户端只存 UI 偏好及资源 ID，正文、附件数据和凭据不写浏览器持久存储。退出时先完成未保存确认，再停止守护进程。

## 数据与协议

- 新增 `Project`、`ProjectMaterial`、`Draft`、`Artifact`、`ArtifactRevision`，保留原 `model.Store`，另设 `WorkbenchStore`。
- schema 从 1 升为 2。旧数据库升级前执行 SQLite 一致快照、完整性/版本校验和摘要记录；文件 0600、目录 0700。备份失败则拒绝迁移，新数据库不制造空备份。
- 项目关联和草稿更新使用预期值/版本条件；成果追加使用 `expected_latest_revision_id`。草稿基线可在同一成果内通过 CAS 显式更新。
- 材料发送前检查 Agent/项目归属、存在性、大小和 SHA-256；失败整体拒绝，不静默使用变化后的文件。
- 新建成果校验真实 assistant 消息来源；创建幂等键按主体及端点作用域处理。
- 新增 22 条工作台 HTTP 操作；内容读取和下载单独鉴权，元数据不带正文。纯文本预览、attachment 下载、文件名净化和 nosniff/CSP 边界保持有效。
- `/version` 声明已完成的能力；旧 daemon 没有能力字段时保留既有会话与附件副本流程，不探测或调用新写接口。
- 新事件只发 ID/版本等元数据；原 SSE Seq、gap、hello 和重连处理不变。工作台资源收到事件后失效相应查询。

## 主要公开接口

```go
workbench.New(store model.WorkbenchStore, files workbench.Files, bus *event.Bus) (*workbench.Service, error)
```

完整共享资源/仓储签名见 `internal/model/workbench.go`、`internal/model/repository.go`。新增 HTTP 路径与输入约束见 `api/openapi/openapi.yaml`：项目/材料、会话项目关联、草稿、成果及版本、版本正文和下载。无 worker RPC、go.mod、Makefile 或 AGENTS.md 修改。

```ts
workbenchApi(client: ApiClient): WorkbenchApi
usePersistentDraft(scope: DraftScope | null, content: DraftContent, onRestore: (content: DraftContent) => void)
TaskStarter.start(input: StartTaskInput, onStage: (stage: StartStage) => void): Promise<StartTaskResult>
SavedArtifactEditor({ artifactId, sessionId, visible, onClose, onDirtyChange })
```

`Composer.onSend` 向后兼容原 text/attachments 参数，第三个可选参数为材料 ID。StartPage 的第四个可选参数携带恢复的已上传引用，不包含本地 File 二进制。ApiClient 新增可选 text/blob 响应类型，默认 JSON 行为不变。

## 实际执行的验证

命令均在仓库根目录执行，先设置仓库指定 PATH：

```bash
make lint
make test
make e2e
make build
npm run typecheck
npm test
npm run build
go test -race ./internal/store
git diff --check -- apps internal api docs/architecture.md docs/design/workbench-contract-proposal.md docs/reports docs/research
```

- Go 格式、vet、全仓测试通过；store race 通过。
- Python ruff、格式、mypy 通过，176 项测试通过。
- Web 与桌面 TypeScript 类型检查通过。
- 最终前端测试：桌面 231 项，Web 275 项，合计 506 项通过。
- Go 二进制与 Web/Electron 构建通过。复用现有锁定依赖，没有新增依赖或执行 go mod tidy。
- `make e2e` 使用真实 Python worker、本地假模型、审批与工具调用，完整通过且未跳过。
- OpenAPI 用现有解析器核验：44 个路径、48 个 schema，引用均可解析。
- 全仓差异检查仍包含原有 team-workflow 文档的空白字符问题；本次相关文件范围通过，未顺手修改那些文档。

### 实际进程和浏览器验证

- 已运行独立的两进程验证：同数据目录关闭 daemon 1，再启动不同 PID、不同令牌的 daemon 2；相同草稿正文、版本、owner 恢复，旧令牌 401，过期版本写入 409。
- 实际 HTTP 并发 16 次同名上传，得到 16 个独立路径，每份内容正确；材料内容变化后消息提交 409；owner 注入及缺失/null/超限正文被拒，原草稿保持不变。
- 浏览器中创建项目、上传资料、明确勾选、开始任务、批准读取、生成初稿、保存版本 1、编辑并自动保存、刷新恢复、追加版本 2、读取版本 1，以及重启后的再次编辑并保存版本 3 均成功。
- 再次停止整个测试 daemon，启动新进程并从应用根入口进入，自动回到原任务/成果，包含 `P3-20261012` 标记的未完成编辑稿完整恢复。
- 1440×900 并排与 900×760 标签布局已检查；保存按钮均在可见窗口内，切回对话后成果仍保留。项目成果列表显示最新版本 3。文件导出与守护进程中的副本内容曾逐字节比较一致。
- 最终验收发现临时代理未转发 PUT，补齐验收环境后使用“重试保存”恢复，无需修改产品接口；失败期间窗口内编辑仍保留。

上述完整任务使用本地假模型，不能作为真实模型质量结论。用户提供网关的 `auto:fast` 最小文本请求已单独返回 HTTP 200；没有据此声称真实网关的完整工具回路或所有协议已验证。密钥未写入仓库、测试、配置文件或日志。

## 设计与研究交付

- [第三阶段契约提案](../design/workbench-contract-proposal.md)：保留授权范围与 P3B 后置设计。

## 仍然明确保留的边界

- 新的项目、材料、项目内任务与成果列表首版最多显示 200 项，尚未补完整分页交互；原历史任务分页保留。
- 未点击开始、尚未上传的本地 File 不能跨重启恢复；界面说明需要重新选择。正文与已上传引用可以恢复。
- 项目不是独立文件沙箱：同一 Agent 的项目共享其工作区，勾选资料不等于撤销其他已授权文件的访问。
- 支持文本/代码成果，不包含 Office 格式转换平台。保存版本、保存副本、导出与修改原文件是不同动作。
- 原文件覆盖/原子应用/磁盘撤销仍未实现，也未启用对应 capability。
- 没有跨进程精确一次发送保证；未知结果必须核对，不自动重发。
- 没有正式账户额度、费用核算或消费上限面板；目前展示服务实际提供的 token 用量。
- Electron 原生退出确认、多显示器拔插、Windows/Linux 安装包尚未真机运行验证。
- 自动保存仍存在写入确认前的时间窗口；失败时保留窗口内编辑并提示，不能承诺断电前最后一个字符必然已保存。
