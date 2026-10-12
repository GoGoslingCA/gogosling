# 工作台第三阶段契约变更提案

状态：用户已于 2026-10-12 授权 P3A，并继续授权 P3B 文件预览、应用、撤销和兼容验证；两阶段已在本地实现。本文保留设计推演，最终字段及签名以 internal/model/workbench.go、change.go、operation.go、repository.go 与 OpenAPI 为准。核对基线：`b8bbd0d` 与本次工作树的界面改动。

本文沿用现有中文工程说明的写法，目的是让负责人可以明确批准变更范围。它不替代 `docs/architecture.md`。

## 1. 本次建议批准的范围

先实现 P3A：一个项目组织同一智能体下的会话和资料；输入与成果草稿由内核保存；成果有可重新打开的索引与不可变版本；用户能读取、下载和继续修改这些版本。完整路径为“加入需求文档 → 明确本次资料 → 对话形成初稿 → 修改 → 保存新版本 → 重启后继续”。

P3A 不提供任意工作区文件写入、覆盖、执行或磁盘撤销按钮。现有“保存副本”保留，新增版本保存与文件下载分别命名，避免将保存版本说成已经修改原文件。

P3B 的单文件变更和独立 Operation 主体已获后续用户授权。完整装配后声明 capability；未支持平台拒绝原位修改。UI 申请仍经 `tools.Executor` 和 `permission`，每份冻结预览仅允许单次批准。

P3A 需要允许修改的共享文件只有：

- `internal/model/session.go`：给 Session 增加可选 `project_id`，不改 ID、Key、Origin 或现有状态。
- `internal/model/repository.go`：新增项目、材料、草稿、成果仓储接口及独立 WorkbenchStore；扩展 SessionFilter 的可选项目过滤。原 `model.Store` 不强制嵌入新接口，以免无关测试和消费者全部扩大依赖。
- 新增 `internal/model/workbench.go`：Project、ProjectMaterial、Draft、Artifact、ArtifactRevision 的共享类型与输入约束；P3A 不加入运行时未支持的 ChangeSet 类型。
- `api/openapi/openapi.yaml`：新增资源路径、字段、能力声明和事件；旧字段与旧路径语义不变。
- `docs/architecture.md`：登记 `internal/workbench` 的职责、依赖和数据归属，说明 P3A 没有新增直接文件修改通道；明确升级前备份的实际实现要求。

不需要修改 `go.mod`、`Makefile`、`AGENTS.md`、`api/worker-rpc/*`、`internal/model/run.go` 或 `internal/model/approval.go`。后两项仅在 P3B 审批主体方案确定后另行列出。

普通实现文件预计涉及 `internal/workbench/*`、`internal/store/*`、`internal/session/*`、`internal/server/*`、`internal/app/*`、必要的 `internal/i18n/*` 和前端 API/状态层。新增 SQL 的实际序号在合并时分配，不能抢占并行团队方案草案中的 `002`、`003`；不修改已经发布的 `001_init.sql`。

## 2. 已核对的现状与复用边界

- `internal/model/session.go:27`：Session 是持续对话线程，绑定 Agent；Key 用于路由，不是项目标识。
- `internal/model/repository.go:131`：已有 `Store.Tx`；`internal/session/run.go` 的 `Send` 把用户消息和 Run 放在同一事务后才调用模型。
- `internal/server/handlers_sessions.go:199`：附件必须是对应 Agent 工作区 `inbound/` 下的真实文件；`sessionWorkspace` 按 Agent 定位工作区。
- `internal/workspace/inbound.go:32`：上传净化名称、重名另存、限制大小、临时文件后原子替换；它不是文件版本管理。
- `internal/workspace/workspace.go:105`：现 Workspace 的并发写语义是最后一次 rename 生效，不能把它当成带基线校验的 compare-and-swap。
- `internal/tools/executor.go:139`：现 Executor 以真实 RunID 解析上下文，然后检查工具启用状态、权限和执行路径。
- `internal/permission/service.go:306`：`Check` 统一执行 guard、规则与审批；请求可以阻塞等待决定。
- `internal/store/migrations/001_init.sql:99`：`approvals.run_id` 是非空外键；`internal/store/approvals.go:14` 同样依赖真实运行。人工文件操作不能填空 RunID 或伪造一个模型 Run 来复用它。
- `apps/web/src/lib/artifacts.ts:50`：当前成果是助手消息派生视图；`ArtifactPanel` 保存调用附件上传，不是成果仓储。
- `apps/web/src/pages/chat/Composer.tsx:53` 与 `ArtifactPanel.tsx:53`：未发送与未保存正文只在 React 内存中。离开确认不能恢复崩溃时的草稿。
- `internal/store/migrate.go:86`：已有按版本事务迁移和拒绝旧内核打开新 schema；`internal/update` 当前只有 `doc.go`，尚不能声称已有自动备份回滚实现。

## 3. 最小领域边界

只新增 `internal/workbench` 一个领域包，内部按 project、material、draft、artifact 文件拆分，不建通用知识平台、团队权限平台或新的模型执行框架。

该包只直接导入叶子包。它依赖的会话、工作区和权限能力用本包消费者接口描述，由 `internal/app` 适配，避免直接导入 `session`、`workspace`、`permission`、`store`。传输层负责鉴权、HTTP、大小限制和下载响应；领域层负责归属、版本、来源与一致性；store 负责 SQL 与事务。

P3A 的 Project 固定 `agent_id`，只是材料和任务的组织容器，不拥有独立工作区、不改变工具可访问范围。同一 Agent 的不同项目仍共享 Agent 工作区，这一点应写进项目文件范围说明。跨 Agent 移动项目、在一项目中混用多个 Agent、独立项目沙箱均不在本版。

新业务状态在内核数据库；只含布局的偏好仍由 Web/Electron 保存。正文、材料引用、成果版本不放到 localStorage、Electron userData 或 IPC。

## 4. P3A 资源定义

所有 ID 继续使用 `model.NewID()` 的 ULID，时间为 UTC，列表复用 `model.Page`。所有 `version` 为从 1 开始的正整数；更新采用数据库条件写入，不接受“先读再无条件覆盖”。下文的内容上限为本提案默认值，发布时通过能力响应公布。

### 4.1 Project 与会话关联

Project 字段：`id`、不可变 `agent_id`、`name`（1–120 个 Unicode 字符）、`description`（最多 2,000 字符）、`status: active|archived`、`version`、`created_at`、`updated_at`。

Project 没有可写 `workspace_dir`；响应可引用现有 Agent 信息，由已有 Agent 接口提供真实文件范围。项目归档只隐藏组织入口，不归档、停止或删除已有会话，不改变旧客户端对这些会话的操作能力。本版没有项目硬删除接口。

Session 增加 `project_id?: string`。旧行全部为 NULL，旧 JSON 省略该字段；新会话可以不带项目。关联时服务端校验项目存在且 `project.agent_id == session.agent_id`。移入、移出项目不改 `session.id/key/origin`，不重放消息、不迁移磁盘文件、不创建新的 Run。

关联变化用单独接口处理，避免与已有 title/status PATCH 的并发更新互相覆盖：

```text
PUT /api/v1/sessions/{id}/project
{ "project_id": "<id>" | null, "expected_project_id": "<id>" | null }
```

两个字段都必填；expected 不匹配返回 `conflict`。成功后发布既有 `session.updated`。创建会话增加可选 `project_id`，落盘前验证 Agent 一致；无此字段的旧创建流程原样保留。

### 4.2 ProjectMaterial 与本次资料选择

ProjectMaterial 字段：`id`、`project_id`、`agent_id`、`name`、`mime`、`size`、`sha256`、内部校验过的 `attachment`（复用现 Attachment）、`created_at`。本版每次上传是一个新材料，不覆盖旧材料；材料名字不是身份。

上传复用 `Workspace.SaveInbound`，由项目的 Agent 解析工作区；响应沿用服务端最终净化后的名称与相对路径。先完成文件写入再提交索引，失败必须报错；只能清理本请求新建且可证明没有被引用的文件，不能扫描删除其他历史 inbound 文件。

项目材料从上传开始就有列表索引，不要求先发一条聊天消息。已上传但未索引的历史文件不会被扫描并默认加入任何项目。

`POST /sessions/{id}/messages` 增加可选 `material_ids: string[]`，旧 `attachments` 仍有效。新客户端明确提交本次选中项，默认不自动加入整个项目材料库。服务端校验材料项目与会话项目一致、Agent 一致、文件存在、当前大小及 SHA-256 与材料记录一致，展开为现有 Attachment 后调用 `Session.Send`。任一引用无效时拒绝整次发送；不静默跳过，也不偷偷使用变化后的文件。

如果同时出现 `material_ids` 与 `attachments`，按规范化相对路径去重，总数仍受现有附件上限约束。来源材料 ID 和采集摘要作为消息非推理元数据保存；实际推理仍使用现有 attachment part，不改 worker RPC。

这里的资料勾选是上下文选择，不是工具文件权限白名单。所选资料带来源标记，不能洗成系统指令。要增加“模型只能访问勾选资料”的能力，必须另做运行级文件能力约束，不能靠提示词或隐藏列表实现。

`DELETE /projects/{project_id}/materials/{id}` 仅移除项目索引，不删除 inbound 文件、不修改已发送历史消息。草稿里的失效引用在恢复时保留为“资料不可用”，发送前要求用户处理；不丢弃草稿正文。

### 4.3 Draft 与乐观版本

Draft 字段：`id`、`owner_id`（服务端从 Principal 得到）、`kind: composer|artifact`、`agent_id`、可选 `project_id/session_id/artifact_id/base_revision_id`、`text`、可选 `filename`、`material_ids`、兼容的 `attachments`、`version`、`created_at`、`updated_at`。

作用域由服务器生成，不接受任意字符串：

- 开始页输入：`owner + composer + agent + project-or-none`，无 session。
- 会话输入：`owner + composer + session`。
- 成果编辑：`owner + artifact + artifact_id`；以 `base_revision_id` 引用不可变的成果版本。基线引用可随 Draft 的 expected_version CAS 修改，但新 revision 必须属于同一 artifact；不能借此修改 owner 或作用域。

数据库对上述作用域建唯一约束。首次创建 POST，若作用域已存在返回冲突及已有草稿 ID，客户端读取后提供恢复/替换选择。后续 PUT 必须带 `expected_version`；成功返回 version+1；不匹配返回 409 与 `reason=stale_version,current_version`，不在错误里塞完整正文。删除同样必须带 expected_version。

正文上限 512 KiB UTF-8，文件名最多 160 字符，附件上传仍走独立文件接口；草稿不能嵌入文件二进制、连接令牌或模型凭据。字段关联全部在服务端核实，客户端传入 owner_id 一律拒绝。

自动保存需明确区分“正在保存 / 已保存 / 保存失败”。客户端只在服务端返回后提升已确认版本；离线或请求失败时保留内存正文。跨窗口冲突保留双方内容供用户选择，不自动最后写入者获胜。

从开始页创建会话后，以版本校验绑定该草稿到新 session。绑定失败不删除开始页草稿；恢复时提示已有会话与草稿，不重新自动发送。首版不承诺跨 API 的原子“建会话并发送”。

成功发送后删除旧草稿必须使用发送时的草稿 version；如果用户在发送期间继续编辑，删除因版本不符而失败，新稿保留。若响应丢失或客户端崩溃，草稿可能留存：再次打开先展示“可能已发送，请检查历史”，不得自动重发。将来若要严格一次发送，需要单独补入客户端消息幂等键；本版不声称已有此能力。

### 4.4 Artifact、Revision 与来源

Artifact 字段：`id`、`agent_id`、`session_id`、派生显示的 `project_id?`、`title`、`kind: document|code|text`、`filename`、`media_type`、`source`、`latest_revision_id`、`version`、`created_at`、`updated_at`。

source 必须引用该会话的一条真实 assistant Message：`message_id`、服务器从消息取得的 `run_id?`、`message_seq`、`source_sha256`、`partial`，可选 `fragment`（全文或代码片段序号，仅为 UI 定位）。客户端不能引用其他会话的消息，不能提交工具输出冒充助手成果。服务端验证源消息，记录原始来源；用户保存的正文明确为从来源编辑出的副本，不承诺代码片段解析和原文件路径存在。

ArtifactRevision 字段：`id`、`artifact_id`、`number`、`parent_revision_id?`、`content`、`sha256`、`byte_size`、`filename`、`media_type`、`created_by`、`created_at`。每个版本不可变，`UNIQUE(artifact_id, number)`。最多 512 KiB UTF-8，先支持文档与代码文本，不扩展为办公文件转换平台。

第一次保存显式创建 Artifact 与 Revision 1，二者和来源验证在同一数据库事务内。修改保存 POST 新 revision，必须带 `expected_latest_revision_id`；只有基线仍匹配才插入并推进 latest。冲突返回当前 latest ID，不覆盖别人的版本。读旧版本不会改变 latest。

恢复旧版本使用“以旧版本为内容保存新版本”，保留完整审计与版本链；不删除较新的版本。编辑器撤销未保存编辑只影响 Draft；二者与 P3B 的磁盘撤销分开命名。

历史助手消息不自动迁移为成果。旧 UI 使用的派生 ID 仍可用，用户首次保存时建立 `source message + fragment → artifact ID` 映射；创建支持 `Idempotency-Key`，同一主体、同一路径、同一请求内容重试返回原资源，不同内容复用 key 返回冲突。

列表与元数据响应不含正文。内容读取与下载端点必须经过同一 API 鉴权，不返回 `file://`、系统绝对路径或带令牌的 URL。下载强制 `Content-Disposition: attachment`，使用净化文件名与 `X-Content-Type-Options: nosniff`；文本预览固定安全 Content-Type，不能将模型 HTML 当同源页面执行。字节总数和 SHA-256 与 revision 一致，超过上限拒绝而不是静默截断。

## 5. P3A HTTP、事件与能力发现

候选端点（都是 `/api/v1` 下的向后兼容扩展）：

```text
GET, POST   /projects
GET, PATCH  /projects/{id}                  PATCH 需 expected_version
GET, POST   /projects/{id}/materials        POST 为 multipart 文件上传
DELETE      /projects/{id}/materials/{material_id}
PUT         /sessions/{id}/project          CAS 更新项目关联

GET, POST   /drafts                         GET 可按 scope 字段过滤，owner 隐式
GET, PUT    /drafts/{id}                    PUT 需 expected_version
DELETE      /drafts/{id}?expected_version=N

GET, POST   /sessions/{id}/artifacts         创建含首个 revision
GET         /projects/{id}/artifacts        按会话当前项目关联聚合
GET         /artifacts/{id}
GET, POST   /artifacts/{id}/revisions        POST 需 expected_latest_revision_id
GET         /artifacts/{id}/revisions/{revision_id}
GET         /artifacts/{id}/revisions/{revision_id}/content
GET         /artifacts/{id}/revisions/{revision_id}/download
```

ArtifactRevision 的元数据 GET 不带 content；`/content` 返回 `text/plain; charset=utf-8` 原始 UTF-8 字节；`/download` 返回 `application/octet-stream` 且 attachment。两者都带 `Cache-Control: no-store`。首版不支持范围下载。

读取返回 200，创建返回 201，成功删除返回 204。沿用现 `invalid/unauthorized/forbidden/not_found/conflict/unavailable/internal`，不新增与现模型重复的一套错误码。冲突详情的 reason 候选为 `stale_version/source_changed/material_changed/scope_mismatch`，只含资源 ID、版本和必要的相对路径。

`GET /version` 增加可选 `capabilities` 对象，键和值示例：

```json
{
  "workbench.projects": 1,
  "workbench.materials": 1,
  "workbench.drafts": 1,
  "workbench.artifacts": 1,
  "workbench.limits": { "text_bytes": 524288 }
}
```

只有已实现、通过迁移与初始化检查的完整能力才能出现。旧 daemon 缺字段视为均不支持；新 UI 隐藏对应写入口，继续提供旧会话、附件上传、消息副本导出。不能用版本字符串猜功能，不能向旧服务发送它不认识的字段。P3B 的 `workbench.changesets` 在后续完整实现前始终缺失。

新增事件：`project.created/updated`、`project.material.created/removed`、`draft.updated/deleted`、`artifact.created/revision.created`。只发 ID、version、来源 ID 等元数据，不发送草稿/文档全文、密钥或上传二进制。客户端收到事件后失效相应查询；只在对应资源重新读取成功后显示新版本。

保留既有 `session.*`、`run.*`、审批事件、全局 Seq、`hello.started_at`、`since` 与 `gap` 语义。daemon 重启必须丢弃旧 Seq 并全量刷新。新事件被旧客户端忽略即可，不复用 run.* 表示草稿自动保存。

## 6. P3A 仓储与消费者接口候选

以下是拟在评审通过后加入的签名轮廓。Input/Filter 均在共享 workbench 类型中明确定义，不允许实现时悄悄增加另一套领域类型。

```go
type ProjectRepo interface {
    CreateProject(context.Context, *Project) error
    GetProject(context.Context, string) (*Project, error)
    ListProjects(context.Context, ProjectFilter, Page) ([]*Project, error)
    UpdateProject(context.Context, *Project, int64) error // expectedVersion
    SetSessionProject(context.Context, string, string, string) error // session, expected, next
    CreateProjectMaterial(context.Context, *ProjectMaterial) error
    ListProjectMaterials(context.Context, string, Page) ([]*ProjectMaterial, error)
    GetProjectMaterial(context.Context, string) (*ProjectMaterial, error)
    DeleteProjectMaterial(context.Context, string) error // 只删引用
}

type DraftRepo interface {
    CreateDraft(context.Context, *Draft) error
    GetDraft(context.Context, string) (*Draft, error)
    ListDrafts(context.Context, DraftFilter, Page) ([]*Draft, error)
    UpdateDraft(context.Context, *Draft, int64) error
    DeleteDraft(context.Context, string, int64) error
}

type ArtifactRepo interface {
    CreateArtifact(context.Context, *Artifact) error
    GetArtifact(context.Context, string) (*Artifact, error)
    ListArtifacts(context.Context, ArtifactFilter, Page) ([]*Artifact, error)
    AppendArtifactRevision(context.Context, *ArtifactRevision, string) error // expectedLatest
    GetArtifactRevision(context.Context, string, string) (*ArtifactRevision, error)
    ListArtifactRevisions(context.Context, string, Page) ([]*ArtifactRevision, error)
}

type WorkbenchStore interface {
    ProjectRepo
    DraftRepo
    ArtifactRepo
    SessionRepo
    MessageRepo
    AuditRepo
    WorkbenchTx(context.Context, func(WorkbenchStore) error) error
}
```

`store.Store` 同时满足原 Store 和 WorkbenchStore；WorkbenchTx 与现 Tx 共用事务实现，回调内不可重入、不持有网络请求或用户审批。新增仓储方法使用原有 CodeNotFound/CodeConflict 约定。附加的请求幂等记录只针对新创建资源，限主体与端点作用域，存请求摘要不存凭据。

`workbench` 消费者接口复用的能力：Agent lookup、Session lookup、Message lookup、工作区 `SaveInbound/ReadFile/Stat/Resolve`、事件总线、审计追加。文件读写由 app 注入的 workspace 适配器完成；现 HTTP handler 上传逻辑可提取共享 helper，但 server 不直接 import store。

数据表建议为 `projects`、`project_materials`、`drafts`、`artifacts`、`artifact_revisions`、新资源请求幂等表，并为 sessions 增加 nullable project_id。文本内容可先使用 SQLite TEXT，避免本版同时引入外置 blob 的双写一致性；内容大小和总量必须有明示限制。revision 的内容只通过专门读取接口序列化。

项目归属必须保持同 Agent：领域校验之外，迁移通过组合约束或触发器防止跨 Agent 关联。项目无硬删除；Agent、Session 的既有删除语义仍生效，新增子记录按已确认外键级联，界面删除确认需说明草稿和成果也会删除。P3A 不自动清理已有磁盘 inbound 文件。会话从项目移出只改变关联，不删除成果；项目成果列表随会话归属变化。

## 7. P3B 单文件 ChangeSet 契约

用户于 2026-10-12 授权继续 P3B，采用独立持久 Operation 主体。首版限制为一个工作区内的普通 UTF-8 文本文件，最多 512 KiB，支持创建、完整替换及独立预览的撤销；创建的撤销按 destructive 删除审批。不支持多文件事务、目录、任意删除、改名、二进制、权限位修改、工作区外路径、脚本执行或自动发布。

候选 ChangeSet 字段：`id`、`session_id/agent_id`、`artifact_id/revision_id`、`target_path`（仅规范化相对路径）、`operation: create|replace`、`base_exists/base_sha256`、`proposed_sha256`、`preview_digest`、`version`、`status`、`created_by/created_at/expires_at`，以及完成后的 `applied_sha256/applied_at`、`undo_of?`。所有目标、基线和来源由服务端计算/验证；客户端传入的内容只从已保存 revision 读取，不接受 apply 时替换正文。

候选接口：

```text
POST /sessions/{id}/changesets
  { artifact_id, revision_id, target_path, operation }
  → 201 ChangeSet，仅读取目标、记录快照和预览，未写目标
GET /changesets/{id}
GET /changesets/{id}/diff
POST /changesets/{id}/apply
  { expected_version, preview_digest } + Idempotency-Key
  → 202 操作受理；可能等待审批，不能立即显示已应用
POST /changesets/{id}/undo-preview
  { expected_version }
  → 201 新的撤销预览 ChangeSet，只有当前目标仍匹配 applied_sha256 才创建
POST /changesets/{undo_id}/apply
  → 同一套执行、审批、基线验证，不设绕过路径
```

撤销创建文件属于删除副作用，风险应提升为 destructive；若第一版不实现受控删除，则明确只开放 replace 的撤销，创建操作只提供保留文件的手动处理指引。不能用普通 write 风险掩盖删除。

状态候选：`prepared → awaiting_approval → applying → applied`；拒绝/超时为 `denied/expired`；基线变化为 `conflict`；写入或落账无法确定为 `recovery_required`。prepared 过期后必须重新预览。每个副作用独立 ChangeSet，undo 不是把 applied 记录篡改回 prepared。

preview_digest 绑定 Agent、会话、操作者、工作区真实根身份、目标相对路径、基线摘要、revision ID、提议摘要及过期时间；它是内容一致性摘要，不是访问令牌，也不能替代鉴权或审批。审批通过后再次核对全部绑定，不允许“预览 A、应用 B”。

### 7.1 不能直接复用的审批条件

现 Executor 以 RunID 找执行上下文，审批表的 run_id 又是非空外键。因此“拿到用户点击就直接调用 Workspace.WriteFile”与“造一个空 RunID 去调用 permission.Check”均不可接受。

本轮采用方案一，避免扩张模型 Run 语义。原候选方案保留如下：

1. 给内核增加正式的本机文件操作执行主体及其审批关联；Executor 支持该主体，permission 仍使用同一 guard/deny/rule/once-approval 逻辑，审批持久化能引用真实操作记录；旧运行审批列表与旧 SSE 语义不变。需要明确新增 Operation/Approval 共享字段或独立资源及 schema 迁移。
2. 正式扩展 Run 为可表达非模型用户操作的运行，而不是伪造推理运行；需要明确 RunKind、上下文解析、消息/用量/统计、取消、重启恢复和旧客户端行为。然后才允许通过现 Executor 路径执行。该方案可能改动更多既有语义，不应在 P3A 顺手引入。

无论选择哪种，应用与撤销都必须进入 `tools.Executor`，以经鉴权的实际用户身份执行，使用同一工作区边界与权限引擎；不是新建一个只相信 boolean 的独立确认系统。旧规则中的 deny 和内核保护路径具有优先级。会话级/永久放行不能扩大本次已预览目标或使任意版本被自动替换。

这里需要先评审的新增接口可以表述为 `ExecuteUserOperation(ctx, operationID, call)`，但 operationID 必须来自上述正式持久化主体；未完成模型、审批存储与解析器前，这个签名不能作为可运行旁路加入代码。

### 7.2 基线、原子写入与恢复职责

ChangeSet 的来源、基线和状态迁移由 workbench 管理；实际副作用由受控工具调用 workspace 的条件写能力。候选 workspace 能力为：

```text
Snapshot(ctx, rel, maxBytes) -> exists, sha256, fileIdentity, content
ReplaceIfUnchanged(ctx, rel, expectedSnapshot, proposedContent) -> newSnapshot
RemoveIfUnchanged(ctx, rel, expectedSnapshot) -> removedSnapshot  // 仅在批准删除撤销后
```

已有 WriteFile 不能代替 ReplaceIfUnchanged。需在工作区适配器统一串行化 daemon 对目标路径的写入，重新解析根与符号链接边界、核对当前内容摘要，写临时文件、fsync、原子替换并校验结果。所有相关工具路径也必须遵守同一协调机制。等待用户审批时不得持有文件锁或 SQL 事务。

同进程锁不能阻止外部编辑器或外部进程并发写入。实现评审必须明确平台上“检查后、替换前”竞态的保障与限制，不能把“hash + rename”宣传成对任意外部写者的原子 CAS。未解决的路径/平台只能降级为另存副本，不能静默覆盖。符号链接替换、大小写别名、hard link、外部编辑和运行中的工具写入都要纳入测试及支持范围。

数据库事务不能原子覆盖文件系统。执行前必须持久化操作意图与原文件前像，执行成功后落 applied 状态与审计；原图快照放内核受保护目录，不能放 Agent 可写的普通工作区里。崩溃恢复按目标实际 hash 判断：仍是 base 则未应用；等于 proposed 则补齐成功落账；两者都不是则 recovery_required，禁止自动覆盖或自动撤销。

撤销必须以 applied hash 为新的基线，再进行预览和批准；发现其他编辑就返回 conflict，保留现文件和前像供用户比较。审计只记录资源 ID、相对目标、摘要、风险、操作者、结果与原因，不保存正文或令牌。应用成功必须在可重读的最终状态确认后显示，不能只凭 HTTP 202 显示。

## 8. 兼容、迁移与备份

保留 HashRouter、`/chat/:sessionId` 和旧 query；新项目入口只是补充导航。保留 sessionId、message seq、runId、Key 与 Origin；不因界面改名重建任何会话。P3A 不增加 IPC，打包版继续 app:// 同源代理注入令牌，开发版只在内存传递连接信息。新内容端点同样受 Host/Origin/Bearer 校验。

迁移执行前必须具备可验证的备份流程，而不是仅在发布说明写“可回滚”：

1. 在获得 daemon 单实例锁、尚未接收业务写入时，检测是否有待迁移 schema。现 Store.Open 会自动迁移，因此备份检查必须安排在该迁移之前；不能 Open 完再备份。
2. 对现库使用 SQLite 一致性备份机制，或确保所有连接关闭并完成 WAL 处理后离线备份完整数据库状态；不能只复制活动的 `.db` 文件而漏掉 WAL。备份由 store/update 能力完成，HTTP 层不打开数据库。
3. 备份目录 0700、文件 0600，记录 schema 版本、程序版本、备份时间、文件清单和校验和，不包含访问令牌明文。P3A 文本在 DB、已有 inbound 原位保留；需要备份的配置文件只包含恢复所必需项。
4. 验证备份可打开及 `integrity_check`，失败就拒绝迁移。每个 SQL 版本沿用现有事务；失败不能启动新 capability。
5. 演练使用备份和匹配旧内核恢复。迁移完成后旧内核拒绝新 schema 的现行为必须保留。旧代码能忽略字段不代表旧内核能打开新版本数据库。
6. 回滚不是自动抹掉升级后的新工作：应先停写、保留当前数据副本并说明恢复点，再恢复已核验的匹配快照。P3B 还需备份操作记录/前像并先完成文件状态对账，不能靠单独回滚 DB 撤销已经修改的文件。

P3A 只增加新表与可空字段，不扫文件重归属、不把既有消息全部复制成版本、不执行历史工具调用。删除旧 UI 偏好或回退前端不应影响内核草稿和成果。新客户端连接旧 daemon 时通过 capabilities 降级；能力调用失败保留当前编辑内容并提供导出，不清空草稿。

## 9. 实施顺序与验收

建议按以下顺序拆分审核：

1. 共享类型/API/架构评审，以及可恢复备份前置能力；无 UI 行为变化。
2. 项目与材料索引、会话关联、严格归属校验；旧会话/上传回归通过后开启 projects/materials。
3. Draft CAS、重启恢复、跨窗口冲突、失败保留；确认没有新编辑被旧发送响应清空后开启 drafts。
4. Artifact 与不可变版本、来源验证、重新打开、读取下载、导出安全；完成最短用户路径后开启 artifacts。
5. 单独评审 P3B 的执行主体与审批存储方案、文件条件写实现和竞态边界；通过后再实现 ChangeSet。没有这一决定就保留副本保存，不挂假的应用/撤销入口。

必须覆盖的 P3A 验收：旧数据库升级及备份恢复、旧客户端基本 CRUD、新客户端连接旧 daemon、跨 Agent 项目关联拒绝、无效材料整次发送失败、材料变化检测、同作用域并发草稿创建、两个窗口同版更新只有一个成功、发送期间新编辑保留、离线恢复不自动重发、成果源消息越权拒绝、部分输出标记、并发新版本冲突、下载字节/摘要一致、HTML 不在同源执行、删除会话的关联清理、SSE gap/重启重拉、不把全文/令牌放事件与日志。

P3B 另需拒绝越界/符号链接/别名/保护路径、规则拒绝优先、审批目标与版本绑定、重复请求幂等、应用/撤销外部修改冲突、各崩溃切点恢复、取消与超时、失败审计、不同平台文件语义验证。没有这些证据不能宣称覆盖和撤销已安全完成。

2026-10-12 用户再次明确授权 P3B 文件预览、应用、撤销及兼容验证；独立 Operation、审批关联和共享契约均在该实施范围内。能力只在完成服务装配及恢复后声明。
