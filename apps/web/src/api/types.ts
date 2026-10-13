// 本文件按 api/openapi/openapi.yaml（本地 HTTP API，版本 "1"）手写而成。
// 字段命名与契约一致（snake_case），时间为 RFC 3339 字符串（UTC）。
// 契约变更时同步修改这里；不要在页面里另起一套类型。

// ---- 错误 ------------------------------------------------------------------

/** 服务端错误码，与 internal/model.Code 一一对应。 */
export type ServerErrorCode =
  | "invalid"
  | "unauthorized"
  | "forbidden"
  | "not_found"
  | "conflict"
  | "denied"
  | "unavailable"
  | "timeout"
  | "cancelled"
  | "internal";

/** 客户端自身产生的错误码：请求根本没有到达服务端（离线、拒绝连接、DNS）或被调用方中止。 */
export type ClientErrorCode = "network" | "aborted";

export type ErrorCode = ServerErrorCode | ClientErrorCode;

export interface ErrorResponse {
  error: {
    code: ServerErrorCode;
    message: string;
    details?: Record<string, unknown>;
  };
}

// ---- 系统 ------------------------------------------------------------------

export type WorkerState = "ready" | "starting" | "unavailable";

export interface WorkerStatus {
  status: WorkerState;
  version?: string;
  python?: string;
}

export interface VersionInfo {
  version: string;
  commit?: string;
  date?: string;
  started_at?: string;
  worker?: WorkerStatus;
  capabilities?: WorkbenchCapabilities;
}

/** 能力键缺失表示守护进程不支持；不根据版本号猜测。 */
export interface WorkbenchCapabilities {
  /** 版本接口的可选能力扩展；旧守护进程缺席时不新增此工具选项。 */
  "tools.read_document"?: number;
  "workbench.preview"?: number;
  "workbench.preview_layout"?: number;
  "runtime.resources"?: number;
  "prompt.enhance"?: number;
  "workbench.projects"?: number;
  "workbench.materials"?: number;
  "workbench.drafts"?: number;
  "workbench.artifacts"?: number;
  "workbench.changesets"?: number;
  "workbench.limits"?: { text_bytes: number };
}

/** 来源：OpenAPI ResourceState；数字是任务槽，不代表 CPU 百分比。 */
export interface ResourceState { max_parallel: number; max_queued: number; running: number; queued: number; awaiting_approval: number }

// ---- 智能体 ----------------------------------------------------------------

export type AutonomyLevel = "ask_all" | "ask_risky" | "auto_in_sandbox";

export interface ModelRef {
  provider_id: string;
  model: string;
}

export interface Agent {
  id: string;
  name: string;
  description?: string;
  system_prompt?: string;
  model: ModelRef;
  /**
   * 启用的内置工具名。契约规定输出侧始终是数组：[] 表示不启用任何内置工具（与默认工具集不同）。
   * 旧版守护进程会因 omitempty 省略空数组，所以读取时用 agentTools() 把缺席当作 []。
   */
  tools: string[];
  extensions: string[];
  autonomy: AutonomyLevel;
  workspace_dir: string;
  template?: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

/** 创建时 name 与 model 必填；更新时只传需要修改的字段。 */
export interface AgentInput {
  name?: string;
  description?: string;
  system_prompt?: string;
  model?: ModelRef;
  /** 缺席 = 不修改（创建时使用默认工具集）；null = 重置为默认工具集；[] = 不启用任何内置工具。 */
  tools?: string[] | null;
  extensions?: string[];
  autonomy?: AutonomyLevel;
  template?: string;
  enabled?: boolean;
}

export type ToolRisk = "read" | "write" | "execute" | "network" | "destructive";

export interface ToolSpec {
  name: string;
  description: string;
  input_schema: Record<string, unknown>;
  /** builtin / mcp:<extension_id> / extension:<extension_id> */
  source: string;
  risk: ToolRisk;
}

// ---- 供应商 ----------------------------------------------------------------

export type ProviderKind = "openai_compatible" | "anthropic" | "ollama";

export interface Provider {
  id: string;
  name: string;
  kind: ProviderKind;
  base_url?: string;
  /** 是否已配置密钥；密钥本身永不返回。 */
  has_secret?: boolean;
  default_model?: string;
  models?: string[];
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface ProviderInput {
  name?: string;
  kind?: ProviderKind;
  base_url?: string;
  /** 只写：创建时写入 secret 存储，更新时传入则轮换。 */
  api_key?: string;
  default_model?: string;
  enabled?: boolean;
}

export interface ProviderTestResult {
  ok: boolean;
  models?: string[];
  latency_ms?: number;
  error?: string;
}

// ---- 会话与消息 --------------------------------------------------------------

export type Origin = "desktop" | "cli" | "channel" | "job" | "acp";

export type SessionStatus = "active" | "archived";

export interface Session {
  id: string;
  agent_id: string;
  project_id?: string;
  title?: string;
  origin: Origin;
  key: string;
  status: SessionStatus;
  /** 当前未结束的运行 ID；没有时省略。页面刷新后据此恢复"生成中"状态与取消按钮。 */
  active_run_id?: string;
  created_at: string;
  updated_at: string;
  last_message_at?: string;
}

export interface SessionCreateInput {
  agent_id: string;
  title?: string;
  project_id?: string;
}

export interface SessionPatchInput {
  title?: string;
  status?: SessionStatus;
}

export interface Attachment {
  name: string;
  /** 工作区相对路径。 */
  path: string;
  mime?: string;
  size: number;
}

export interface ToolCall {
  id: string;
  name: string;
  arguments: Record<string, unknown>;
}

export interface ToolResult {
  call_id: string;
  content: string;
  is_error?: boolean;
  /** 内容超过大小上限被截断。 */
  truncated?: boolean;
}

export type PartType = "text" | "tool_call" | "tool_result" | "attachment";

export interface Part {
  type: PartType;
  text?: string;
  tool_call?: ToolCall;
  tool_result?: ToolResult;
  attachment?: Attachment;
}

export type Role = "system" | "user" | "assistant" | "tool";

export interface Message {
  id: string;
  session_id: string;
  seq: number;
  role: Role;
  run_id?: string;
  parts: Part[];
  meta?: Record<string, unknown>;
  created_at: string;
}

export interface SendMessageInput {
  text: string;
  attachments?: Attachment[];
  material_ids?: string[];
}

// ---- 工作台 P3A（与 internal/model/workbench.go、OpenAPI 同步） ----------------

export interface Project {
  id: string;
  agent_id: string;
  name: string;
  description?: string;
  status: "active" | "archived";
  version: number;
  created_at: string;
  updated_at: string;
}
export interface ProjectInput { agent_id: string; name: string; description?: string }
export interface ProjectPatch { expected_version: number; name?: string; description?: string; status?: Project["status"] }
export interface ProjectMaterial {
  id: string;
  project_id: string;
  agent_id: string;
  name: string;
  mime?: string;
  size: number;
  sha256: string;
  attachment: Attachment;
  created_at: string;
}
export interface SessionProjectInput { project_id: string | null; expected_project_id: string | null }
export interface DraftScope {
  kind: "composer" | "artifact";
  agent_id: string;
  project_id?: string;
  session_id?: string;
  artifact_id?: string;
  base_revision_id?: string;
}
export interface DraftContent {
  text: string;
  filename?: string;
  material_ids?: string[];
  attachments?: Attachment[];
}
export interface Draft extends DraftScope, DraftContent {
  id: string;
  owner_id: string;
  version: number;
  created_at: string;
  updated_at: string;
}
export interface DraftInput extends DraftScope, DraftContent {}
export interface DraftUpdateInput extends DraftInput { expected_version: number }
export interface ArtifactSource { message_id: string; run_id?: string; message_seq: number; source_sha256: string; partial: boolean; fragment?: string }
export interface Artifact {
  id: string;
  agent_id: string;
  session_id: string;
  project_id?: string;
  title: string;
  kind: "document" | "code" | "text";
  filename: string;
  media_type: string;
  source: ArtifactSource;
  latest_revision_id?: string;
  version: number;
  created_at: string;
  updated_at: string;
}
export interface ArtifactInput { title: string; kind: Artifact["kind"]; filename: string; media_type: string; source_message_id: string; fragment?: string; content: string }
/** 版本元数据不含正文，正文只能经鉴权的 content/download 接口读取。 */
export interface ArtifactRevision { id: string; artifact_id: string; number: number; parent_revision_id?: string; sha256: string; byte_size: number; filename: string; media_type: string; created_by: string; created_at: string }
export interface ArtifactRevisionInput { expected_latest_revision_id: string; content: string; filename: string; media_type: string }

// ---- 运行 ------------------------------------------------------------------

export type RunStatus =
  | "queued"
  | "running"
  | "awaiting_approval"
  | "completed"
  | "failed"
  | "cancelled";

export type RunTrigger = "user" | "job" | "channel" | "agent";

/** 用量。`usage` 事件是一次模型调用的增量（客户端累加）；run.completed 与 Run.usage 是整次运行的累计值。 */
export interface Usage {
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface Run {
  id: string;
  session_id: string;
  agent_id: string;
  status: RunStatus;
  trigger: RunTrigger;
  input_message_id?: string;
  output_message_id?: string;
  error?: string;
  usage: Usage;
  created_at: string;
  started_at?: string;
  ended_at?: string;
}

// ---- 审批 ------------------------------------------------------------------

export type ApprovalStatus = "pending" | "approved" | "denied" | "expired";

export type ApprovalScope = "once" | "session" | "always";

export interface ApprovalRequest {
  id: string;
  run_id?: string;
  operation_id?: string;
  session_id: string;
  agent_id: string;
  tool_call: ToolCall;
  risk: ToolRisk;
  summary: string;
  details?: Record<string, unknown>;
  status: ApprovalStatus;
  scope?: ApprovalScope;
  decided_by?: string;
  created_at: string;
  expires_at: string;
  resolved_at?: string;
}

/** P3B：冻结预览与独立人工执行主体，来源为 OpenAPI ChangeSet。 */
export type ChangeStatus = "prepared" | "awaiting_approval" | "applying" | "applied" | "denied" | "expired" | "conflict" | "failed" | "cancelled" | "recovery_required";
export interface ChangeSet {
  id: string; owner_id: string; agent_id: string; session_id: string; artifact_id: string; revision_id: string;
  target_path: string; operation: "create" | "replace" | "delete"; status: ChangeStatus; version: number;
  preview_digest: string; base_exists: boolean; base_sha256: string; proposed_sha256: string; proposed_size: number;
  operation_id?: string; undo_of?: string; reason?: string; created_at: string; expires_at: string; applied_at?: string;
}
export interface ChangeInput { artifact_id: string; revision_id: string; target_path: string; operation: "create" | "replace" }
export interface ChangeDiff { change: ChangeSet; before: string; after: string }

export interface Decision {
  approve: boolean;
  scope?: ApprovalScope;
  reason?: string;
}

// ---- 定时任务 --------------------------------------------------------------

export type JobKind = "push_text" | "run_agent";

export type MissedPolicy = "skip" | "run_once";

export type JobRunStatus = "ok" | "failed" | "skipped";

export interface Job {
  id: string;
  agent_id: string;
  name: string;
  schedule: string;
  timezone?: string;
  kind: JobKind;
  input: string;
  session_key?: string;
  missed_policy: MissedPolicy;
  enabled: boolean;
  next_run_at?: string;
  last_run_at?: string;
  last_status?: JobRunStatus;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

export interface JobInput {
  agent_id?: string;
  name?: string;
  schedule?: string;
  timezone?: string;
  kind?: JobKind;
  input?: string;
  session_key?: string;
  missed_policy?: MissedPolicy;
  enabled?: boolean;
}

// ---- 扩展 ------------------------------------------------------------------

export type ExtensionKind = "skill" | "mcp_server" | "plugin";

export type MCPTransport = "stdio" | "http";

export interface MCPServerConfig {
  transport: MCPTransport;
  command?: string;
  args?: string[];
  /** 值可以是 "secret:<ref>" 形式的密钥引用；界面不展示也不回填明文。 */
  env?: Record<string, string>;
  url?: string;
  /** http 传输的附加请求头；值可以是 "secret:<ref>"，界面只展示名称。 */
  headers?: Record<string, string>;
}

/** 扩展的运行时状态（MCP 连接、工具声明是否变化）。 */
export interface ExtensionStatus {
  id?: string;
  connected?: boolean;
  tool_count?: number;
  /** 工具声明与首次启用时固定的摘要不一致，需用户确认（accept-tools）后才会再暴露给模型。 */
  tools_changed?: boolean;
  last_error?: string;
}

export interface Extension {
  id: string;
  kind: ExtensionKind;
  name: string;
  version?: string;
  source: string;
  install_dir: string;
  manifest?: Record<string, unknown>;
  mcp?: MCPServerConfig;
  enabled: boolean;
  digest?: string;
  installed_at: string;
  updated_at: string;
}

export interface ExtensionInstallInput {
  /** 本地目录（技能或插件）。 */
  path?: string;
  /** 注册 MCP 服务时提供。 */
  mcp?: MCPServerConfig;
  /** MCP 服务的显示名。 */
  name?: string;
}

// ---- IM 通道 ---------------------------------------------------------------

/** 契约包含的平台；是否可创建取决于守护进程注册的适配器。 */
export type ChannelKind = "console" | "telegram" | "feishu" | "dingtalk" | "wecom" | "discord";

export type ChannelStatus = "stopped" | "starting" | "running" | "failed";

export interface Channel {
  id: string;
  agent_id: string;
  kind: ChannelKind;
  name: string;
  /** 凭据本身永不返回。 */
  has_secret?: boolean;
  /** 仅非敏感配置，如 allowed_peers、group_mode、trigger_prefix。 */
  config?: Record<string, unknown> | null;
  enabled: boolean;
  status?: ChannelStatus;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

/** 创建通道；字段与 OpenAPI ChannelInput 一致。 */
export interface ChannelInput {
  agent_id: string;
  kind: ChannelKind;
  name: string;
  /** 只写：保存到凭据后端，不进入通道响应与数据库。 */
  secret?: string;
  config?: Record<string, unknown>;
  enabled?: boolean;
}

/** 更新通道；所属智能体与平台不允许修改。secret 缺席保留、空串清除。 */
export interface ChannelPatch {
  name?: string;
  secret?: string;
  /** 缺席或 null 保留；{} 清除自定义配置并使用默认值。 */
  config?: Record<string, unknown> | null;
  enabled?: boolean;
}

// ---- 设置与审计 --------------------------------------------------------------

export type Locale = "zh" | "en" | "fr";

export interface Settings {
  /** 用户显式设置的时区；未设置时省略。 */
  timezone?: string;
  /** 用户显式设置的语言；未设置时省略。 */
  locale?: Locale;
  /** 只读：内核实际使用的语言。 */
  effective_locale?: Locale;
  /** 只读：内核实际使用的时区。 */
  effective_timezone?: string;
  allowed_origins?: string[];
}

export interface AuditEntry {
  id: string;
  at: string;
  actor: string;
  agent_id?: string;
  session_id?: string;
  run_id?: string;
  action: string;
  resource?: string;
  outcome: string;
  details?: Record<string, unknown>;
}

// ---- 列表与分页 --------------------------------------------------------------

export interface ListResponse<T> {
  items: T[];
}

export interface PageQuery {
  limit?: number;
  offset?: number;
}

// ---- 事件流 ------------------------------------------------------------------

/** 运行事件类型，与 model.RunEventType 一致。 */
export type RunEventType =
  | "run.started"
  | "text.delta"
  | "reasoning.delta"
  | "tool.call.started"
  | "tool.call.completed"
  | "approval.requested"
  | "approval.resolved"
  | "message.created"
  | "usage"
  | "run.completed"
  | "run.failed"
  | "run.cancelled";

export const RUN_EVENT_TYPES: readonly RunEventType[] = [
  "run.started",
  "text.delta",
  "reasoning.delta",
  "tool.call.started",
  "tool.call.completed",
  "approval.requested",
  "approval.resolved",
  "message.created",
  "usage",
  "run.completed",
  "run.failed",
  "run.cancelled",
];

/** model.RunEvent：只有与 type 对应的字段会被填充。 */
export interface RunEvent {
  type: RunEventType;
  run_id: string;
  session_id: string;
  agent_id: string;
  at: string;
  /** text.delta 与 reasoning.delta 的增量文本。 */
  delta?: string;
  tool_call?: ToolCall;
  tool_result?: ToolResult;
  approval?: ApprovalRequest;
  message?: Message;
  usage?: Usage;
  error?: string;
}

interface EventBase {
  seq: number;
  at: string;
  agent_id?: string;
  session_id?: string;
  run_id?: string;
}

export interface RunStreamEvent extends EventBase {
  type: RunEventType;
  payload: RunEvent;
}

export interface SessionLifecycleEvent extends EventBase {
  type: "session.created" | "session.updated" | "session.deleted";
  payload: Session;
}

export interface JobTriggeredEvent extends EventBase {
  type: "job.triggered";
  payload: Job;
}

export interface WorkerStatusEvent extends EventBase {
  type: "worker.status";
  payload: WorkerStatus;
}

export interface GapEvent extends EventBase {
  type: "gap";
  payload?: undefined;
}


/** 未来新增、本客户端尚不认识的事件类型；保留原样以便调试，不参与业务。 */
export interface UnknownEvent extends EventBase {
  type: string;
  payload?: unknown;
}

export type ApiEvent =
  | RunStreamEvent
  | SessionLifecycleEvent
  | JobTriggeredEvent
  | WorkerStatusEvent
  | GapEvent
  | UnknownEvent;

export function isRunEventType(type: string): type is RunEventType {
  return (RUN_EVENT_TYPES as readonly string[]).includes(type);
}

function hasObjectPayload(ev: ApiEvent): boolean {
  return typeof ev.payload === "object" && ev.payload !== null;
}

export function isRunStreamEvent(ev: ApiEvent): ev is RunStreamEvent {
  return isRunEventType(ev.type) && hasObjectPayload(ev);
}

export function isSessionLifecycleEvent(ev: ApiEvent): ev is SessionLifecycleEvent {
  return (ev.type === "session.created" || ev.type === "session.updated" || ev.type === "session.deleted") && hasObjectPayload(ev);
}

export function isJobTriggeredEvent(ev: ApiEvent): ev is JobTriggeredEvent {
  return ev.type === "job.triggered" && hasObjectPayload(ev);
}

export function isWorkerStatusEvent(ev: ApiEvent): ev is WorkerStatusEvent {
  return ev.type === "worker.status" && hasObjectPayload(ev);
}

export function isGapEvent(ev: ApiEvent): ev is GapEvent {
  return ev.type === "gap";
}

// agent.* / provider.* / extension.* 只用于让对应缓存失效，不读取 payload，所以下面三个判定
// 不是类型守卫（返回 boolean）：它们的事件形状与 UnknownEvent 相同，做成守卫会把其他联合成员一起收窄掉。

/** agent.created / agent.updated / agent.deleted（删除会级联删除会话、任务与通道）。 */
export function isAgentEvent(ev: ApiEvent): boolean {
  return ev.type.startsWith("agent.");
}

/** provider.created / provider.updated / provider.deleted。 */
export function isProviderEvent(ev: ApiEvent): boolean {
  return ev.type.startsWith("provider.");
}

/** extension.installed / updated / removed / tools_changed。 */
export function isExtensionEvent(ev: ApiEvent): boolean {
  return ev.type.startsWith("extension.");
}

/** 读取智能体的工具列表：契约保证是数组；旧版守护进程省略空数组时按 [] 处理。 */
export function agentTools(agent: Pick<Agent, "tools"> | { tools?: string[] | null }): string[] {
  return Array.isArray(agent.tools) ? agent.tools : [];
}
