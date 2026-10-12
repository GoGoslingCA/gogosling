// react-query 的键集中定义，便于事件驱动的失效。

import type { ApprovalStatus, ExtensionKind } from "../api/types";
import type { AuditQuery } from "../api/modules/audit";

export const queryKeys = {
  version: ["version"] as const,
  resources: ["resources"] as const,
  settings: ["settings"] as const,
  agents: ["agents"] as const,
  agentTools: (agentId: string) => ["agents", agentId, "tools"] as const,
  providers: ["providers"] as const,
  sessions: (agentId?: string) => ["sessions", agentId ?? "all"] as const,
  sessionsRoot: ["sessions"] as const,
  session: (sessionId: string) => ["session", sessionId] as const,
  sessionRoot: ["session"] as const,
  messages: (sessionId: string) => ["messages", sessionId] as const,
  messagesRoot: ["messages"] as const,
  approvals: (status: ApprovalStatus) => ["approvals", status] as const,
  approvalsRoot: ["approvals"] as const,
  jobs: (agentId?: string) => ["jobs", agentId ?? "all"] as const,
  jobsRoot: ["jobs"] as const,
  channels: (agentId?: string) => ["channels", agentId ?? "all"] as const,
  channelsRoot: ["channels"] as const,
  audit: (query: AuditQuery) => ["audit", query] as const,
  auditRoot: ["audit"] as const,
  extensions: (kind?: ExtensionKind) => ["extensions", kind ?? "all"] as const,
  extensionsRoot: ["extensions"] as const,
  /** 扩展运行时状态；挂在 extensions 根下，扩展事件一并失效。 */
  extensionStatus: (id: string) => ["extensions", "status", id] as const,
  run: (runId: string) => ["runs", runId] as const,
  workbench: ["workbench"] as const,
  changes: ["workbench", "changes"] as const,
  sessionChanges: (id: string) => ["workbench", "changes", "session", id] as const,
  change: (id: string) => ["workbench", "changes", id] as const,
  projects: ["workbench", "projects"] as const,
  project: (id: string) => ["workbench", "projects", id] as const,
  materials: (id: string) => ["workbench", "materials", id] as const,
  drafts: ["workbench", "drafts"] as const,
  draft: (id: string) => ["workbench", "drafts", id] as const,
  artifacts: ["workbench", "artifacts"] as const,
  artifact: (id: string) => ["workbench", "artifacts", id] as const,
  revisions: (id: string) => ["workbench", "artifacts", id, "revisions"] as const,
};
