-- 本机文件操作拥有独立执行主体；审批必须关联真实 Run 或真实 Operation，不能两者兼有。
CREATE TABLE operations (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL CHECK(length(owner_id)>0),
  -- 终态保留归属墓碑和恢复审计；不能因旧会话/智能体删除级联丢失操作前像。
  agent_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  change_set_id TEXT NOT NULL CHECK(length(change_set_id)>0),
  status TEXT NOT NULL CHECK(status IN ('created','awaiting_approval','executing','completed','failed','cancelled','expired','recovery_required')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_operations_session ON operations(session_id,created_at,id);
CREATE INDEX idx_operations_change ON operations(change_set_id,created_at,id);
CREATE INDEX idx_operations_pending ON operations(status,created_at,id);
CREATE TRIGGER operation_scope_insert BEFORE INSERT ON operations
WHEN NOT EXISTS(SELECT 1 FROM sessions WHERE id=NEW.session_id AND agent_id=NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'operation session agent mismatch'); END;
CREATE TRIGGER operation_scope_immutable BEFORE UPDATE OF owner_id,agent_id,session_id,change_set_id ON operations
WHEN NEW.owner_id<>OLD.owner_id OR NEW.agent_id<>OLD.agent_id OR NEW.session_id<>OLD.session_id OR NEW.change_set_id<>OLD.change_set_id
BEGIN SELECT RAISE(ABORT, 'operation scope is immutable'); END;
CREATE TRIGGER session_active_operations_delete BEFORE DELETE ON sessions
WHEN EXISTS(SELECT 1 FROM operations WHERE session_id=OLD.id AND status IN ('created','awaiting_approval','executing','recovery_required'))
BEGIN SELECT RAISE(ABORT, 'active user operation blocks session deletion'); END;
CREATE TRIGGER agent_active_operations_delete BEFORE DELETE ON agents
WHEN EXISTS(SELECT 1 FROM operations WHERE agent_id=OLD.id AND status IN ('created','awaiting_approval','executing','recovery_required'))
BEGIN SELECT RAISE(ABORT, 'active user operation blocks agent deletion'); END;

CREATE TABLE approvals_v3 (
  id TEXT PRIMARY KEY,
  run_id TEXT REFERENCES runs(id) ON DELETE CASCADE,
  operation_id TEXT REFERENCES operations(id) ON DELETE CASCADE,
  session_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  tool_call TEXT NOT NULL,
  risk TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  details TEXT,
  status TEXT NOT NULL,
  scope TEXT NOT NULL DEFAULT '',
  decided_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  resolved_at TEXT,
  CHECK((run_id IS NOT NULL AND operation_id IS NULL) OR (run_id IS NULL AND operation_id IS NOT NULL)),
  CHECK(operation_id IS NULL OR scope IN ('','once'))
);
INSERT INTO approvals_v3(id,run_id,operation_id,session_id,agent_id,tool_call,risk,summary,details,status,scope,decided_by,created_at,expires_at,resolved_at)
SELECT id,run_id,NULL,session_id,agent_id,tool_call,risk,summary,details,status,scope,decided_by,created_at,expires_at,resolved_at FROM approvals;
DROP TABLE approvals;
ALTER TABLE approvals_v3 RENAME TO approvals;
CREATE INDEX idx_approvals_status_created ON approvals(status,created_at,id);
CREATE INDEX idx_approvals_run ON approvals(run_id);
CREATE INDEX idx_approvals_operation ON approvals(operation_id);
CREATE TRIGGER approval_operation_scope BEFORE INSERT ON approvals
WHEN NEW.operation_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM operations WHERE id=NEW.operation_id AND session_id=NEW.session_id AND agent_id=NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'approval operation scope mismatch'); END;
