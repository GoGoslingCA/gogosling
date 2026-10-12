-- 001_init.sql — 控制面初始模式。
--
-- 约定（全库统一）：
--   * 主键一律是 model.NewID() 生成的 26 位 ULID 文本，子表直接引用它；没有整数代理键。
--   * 时间列存 RFC 3339 UTC 文本，固定 9 位小数、以 Z 结尾（例如 2026-01-02T03:04:05.000000000Z）。
--     定宽使得字典序等于时间序，可直接用于 ORDER BY 与范围比较；可空时间列存 NULL。
--   * 布尔列存 INTEGER 0/1。
--   * 结构会演进的字段（parts、meta、manifest、config、details、tool_call 等）存 JSON 文本。
--     "无值"存 NULL，"空集合"存 '[]'，二者语义不同（例如 agents.tools 为 NULL 表示使用默认工具集）。
--   * 外键由连接级 PRAGMA foreign_keys=ON 启用；级联删除全部交给 ON DELETE CASCADE，
--     仓储代码里不手写级联 DELETE。
--   * 凭据永不落库：providers.secret_ref 与 channels.secret_ref 只是 secret 存储中的名称。
--   * schema_migrations 表由迁移执行器在应用本文件之前创建，本文件不负责。

-- ── 智能体与供应商 ─────────────────────────────────────────────────────────

CREATE TABLE agents (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  system_prompt TEXT NOT NULL DEFAULT '',
  provider_id   TEXT NOT NULL DEFAULT '',
  model         TEXT NOT NULL DEFAULT '',
  tools         TEXT,                       -- JSON 数组；NULL = 默认工具集
  extensions    TEXT,                       -- JSON 数组；NULL = 无
  autonomy      TEXT NOT NULL,
  workspace_dir TEXT NOT NULL,
  template      TEXT NOT NULL DEFAULT '',
  enabled       INTEGER NOT NULL DEFAULT 1,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE INDEX idx_agents_created ON agents(created_at, id);

CREATE TABLE providers (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  kind          TEXT NOT NULL,
  base_url      TEXT NOT NULL DEFAULT '',
  secret_ref    TEXT NOT NULL DEFAULT '',
  default_model TEXT NOT NULL DEFAULT '',
  models        TEXT,                       -- JSON 数组；NULL = 未知
  enabled       INTEGER NOT NULL DEFAULT 1,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE INDEX idx_providers_created ON providers(created_at, id);

-- ── 会话、消息、运行、审批 ──────────────────────────────────────────────────

CREATE TABLE sessions (
  id              TEXT PRIMARY KEY,
  agent_id        TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  title           TEXT NOT NULL DEFAULT '',
  origin          TEXT NOT NULL,
  key             TEXT NOT NULL,
  status          TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  last_message_at TEXT
);
-- 同一路由键同一时刻最多只有一个 active 会话；归档后允许用同一键新建会话。
CREATE UNIQUE INDEX ux_sessions_active_key ON sessions(key) WHERE status = 'active';
CREATE INDEX idx_sessions_agent_created ON sessions(agent_id, created_at, id);
CREATE INDEX idx_sessions_status_created ON sessions(status, created_at, id);

CREATE TABLE messages (
  id         TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq        INTEGER NOT NULL,              -- 会话内从 1 递增，由 AppendMessage 在写事务内分配
  role       TEXT NOT NULL,
  run_id     TEXT NOT NULL DEFAULT '',
  parts      TEXT NOT NULL,                 -- JSON 数组
  meta       TEXT,                          -- JSON 对象或 NULL
  created_at TEXT NOT NULL,
  UNIQUE (session_id, seq)
);

CREATE TABLE runs (
  id                TEXT PRIMARY KEY,
  session_id        TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  agent_id          TEXT NOT NULL,
  status            TEXT NOT NULL,
  trigger           TEXT NOT NULL,
  input_message_id  TEXT NOT NULL DEFAULT '',
  output_message_id TEXT NOT NULL DEFAULT '',
  error             TEXT NOT NULL DEFAULT '',
  input_tokens      INTEGER NOT NULL DEFAULT 0,
  output_tokens     INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  created_at        TEXT NOT NULL,
  started_at        TEXT,
  ended_at          TEXT
);
CREATE INDEX idx_runs_session_created ON runs(session_id, created_at, id);
-- 只索引非终态运行，ListActiveRuns 在启动恢复时不必扫描历史。
CREATE INDEX idx_runs_active ON runs(created_at, id)
  WHERE status IN ('queued', 'running', 'awaiting_approval');

CREATE TABLE approvals (
  id          TEXT PRIMARY KEY,
  run_id      TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  session_id  TEXT NOT NULL,
  agent_id    TEXT NOT NULL,
  tool_call   TEXT NOT NULL,                -- JSON：model.ToolCall
  risk        TEXT NOT NULL,
  summary     TEXT NOT NULL DEFAULT '',
  details     TEXT,                         -- JSON 或 NULL
  status      TEXT NOT NULL,
  scope       TEXT NOT NULL DEFAULT '',
  decided_by  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  expires_at  TEXT NOT NULL,
  resolved_at TEXT
);
CREATE INDEX idx_approvals_status_created ON approvals(status, created_at, id);
CREATE INDEX idx_approvals_run ON approvals(run_id);

-- ── 定时任务、扩展、通道 ─────────────────────────────────────────────────────

CREATE TABLE jobs (
  id            TEXT PRIMARY KEY,
  agent_id      TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  schedule      TEXT NOT NULL,
  timezone      TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL,
  input         TEXT NOT NULL DEFAULT '',
  session_key   TEXT NOT NULL DEFAULT '',
  missed_policy TEXT NOT NULL,
  enabled       INTEGER NOT NULL DEFAULT 1,
  next_run_at   TEXT,
  last_run_at   TEXT,
  last_status   TEXT NOT NULL DEFAULT '',
  last_error    TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE INDEX idx_jobs_agent_created ON jobs(agent_id, created_at, id);

CREATE TABLE extensions (
  id           TEXT PRIMARY KEY,
  kind         TEXT NOT NULL,
  name         TEXT NOT NULL,
  version      TEXT NOT NULL DEFAULT '',
  source       TEXT NOT NULL DEFAULT '',
  install_dir  TEXT NOT NULL DEFAULT '',
  manifest     TEXT,                        -- JSON 或 NULL
  mcp          TEXT,                        -- JSON：model.MCPServerConfig，仅 mcp_server
  enabled      INTEGER NOT NULL DEFAULT 1,
  digest       TEXT NOT NULL DEFAULT '',
  installed_at TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE INDEX idx_extensions_kind_installed ON extensions(kind, installed_at, id);

CREATE TABLE channels (
  id         TEXT PRIMARY KEY,
  agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  name       TEXT NOT NULL,
  secret_ref TEXT NOT NULL DEFAULT '',
  config     TEXT,                          -- JSON 或 NULL；只放非敏感配置
  enabled    INTEGER NOT NULL DEFAULT 1,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_channels_agent_created ON channels(agent_id, created_at, id);

-- ── 审计与设置 ──────────────────────────────────────────────────────────────

-- 审计只追加不修改，也不对 agents 建外键：智能体删除后审计记录必须保留。
CREATE TABLE audit_log (
  id         TEXT PRIMARY KEY,
  at         TEXT NOT NULL,
  actor      TEXT NOT NULL,
  agent_id   TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  run_id     TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL,
  resource   TEXT NOT NULL DEFAULT '',
  outcome    TEXT NOT NULL,
  details    TEXT                           -- JSON 或 NULL，写入前已脱敏
);
CREATE INDEX idx_audit_at ON audit_log(at, id);
CREATE INDEX idx_audit_agent_at ON audit_log(agent_id, at, id);
CREATE INDEX idx_audit_session_at ON audit_log(session_id, at, id);
CREATE INDEX idx_audit_action_at ON audit_log(action, at, id);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL                       -- JSON 文本
);
