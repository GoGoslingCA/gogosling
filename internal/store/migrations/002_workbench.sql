-- 工作台持久状态；旧会话保持未归入项目，已有消息与文件不改写。
CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 120),
  description TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK(status IN ('active','archived')),
  version INTEGER NOT NULL CHECK(version > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(id, agent_id)
);
CREATE INDEX idx_projects_agent ON projects(agent_id, created_at, id);
ALTER TABLE sessions ADD COLUMN project_id TEXT REFERENCES projects(id) ON DELETE SET NULL;
CREATE INDEX idx_sessions_project ON sessions(project_id, created_at, id);
CREATE TRIGGER session_project_insert BEFORE INSERT ON sessions
WHEN NEW.project_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM projects WHERE id=NEW.project_id AND agent_id=NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'session project agent mismatch'); END;
CREATE TRIGGER session_project_update BEFORE UPDATE OF project_id,agent_id ON sessions
WHEN NEW.project_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM projects WHERE id=NEW.project_id AND agent_id=NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'session project agent mismatch'); END;

CREATE TABLE project_materials (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  name TEXT NOT NULL,
  mime TEXT NOT NULL,
  size INTEGER NOT NULL CHECK(size >= 0),
  sha256 TEXT NOT NULL CHECK(length(sha256)=64),
  attachment TEXT NOT NULL CHECK(json_valid(attachment)),
  created_at TEXT NOT NULL,
  FOREIGN KEY(project_id,agent_id) REFERENCES projects(id,agent_id) ON DELETE CASCADE
);
CREATE INDEX idx_materials_project ON project_materials(project_id,created_at,id);

CREATE TABLE artifacts (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  title TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('document','code','text')),
  filename TEXT NOT NULL,
  media_type TEXT NOT NULL,
  source_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  source TEXT NOT NULL CHECK(json_valid(source)),
  latest_revision_id TEXT,
  version INTEGER NOT NULL CHECK(version >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_artifacts_session ON artifacts(session_id,created_at,id);
CREATE TRIGGER artifact_source_insert BEFORE INSERT ON artifacts
WHEN NOT EXISTS(SELECT 1 FROM sessions s JOIN messages m ON m.session_id=s.id WHERE s.id=NEW.session_id AND s.agent_id=NEW.agent_id AND m.id=NEW.source_message_id AND m.role='assistant')
BEGIN SELECT RAISE(ABORT, 'artifact source mismatch'); END;

CREATE TABLE artifact_revisions (
  id TEXT PRIMARY KEY,
  artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
  number INTEGER NOT NULL CHECK(number > 0),
  parent_revision_id TEXT REFERENCES artifact_revisions(id),
  content TEXT NOT NULL CHECK(length(CAST(content AS BLOB)) <= 524288),
  sha256 TEXT NOT NULL CHECK(length(sha256)=64),
  byte_size INTEGER NOT NULL CHECK(byte_size >= 0),
  filename TEXT NOT NULL,
  media_type TEXT NOT NULL,
  created_by TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(artifact_id, number)
);
CREATE TRIGGER revision_immutable BEFORE UPDATE ON artifact_revisions
BEGIN SELECT RAISE(ABORT, 'artifact revisions are immutable'); END;

CREATE TABLE drafts (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL CHECK(length(owner_id)>0),
  scope_key TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('composer','artifact')),
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
  session_id TEXT REFERENCES sessions(id) ON DELETE CASCADE,
  artifact_id TEXT REFERENCES artifacts(id) ON DELETE CASCADE,
  base_revision_id TEXT REFERENCES artifact_revisions(id) ON DELETE CASCADE,
  text TEXT NOT NULL CHECK(length(CAST(text AS BLOB)) <= 524288),
  filename TEXT NOT NULL,
  material_ids TEXT NOT NULL CHECK(json_valid(material_ids)),
  attachments TEXT NOT NULL CHECK(json_valid(attachments)),
  version INTEGER NOT NULL CHECK(version>0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(owner_id,scope_key)
);
CREATE INDEX idx_drafts_owner ON drafts(owner_id,updated_at,id);

CREATE TABLE workbench_requests (
  owner_id TEXT NOT NULL,
  scope TEXT NOT NULL,
  request_key TEXT NOT NULL,
  request_sha256 TEXT NOT NULL CHECK(length(request_sha256)=64),
  resource_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  PRIMARY KEY(owner_id,scope,request_key)
);
