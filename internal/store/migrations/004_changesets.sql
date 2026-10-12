CREATE TABLE changesets (
 id TEXT PRIMARY KEY,
 owner_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 artifact_id TEXT NOT NULL,
 revision_id TEXT NOT NULL,
 target_path TEXT NOT NULL,
 operation TEXT NOT NULL CHECK(operation IN ('create','replace','delete')),
 status TEXT NOT NULL,
 version INTEGER NOT NULL CHECK(version > 0),
 operation_id TEXT REFERENCES operations(id),
 undo_of TEXT REFERENCES changesets(id),
 payload TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX changesets_session ON changesets(owner_id,session_id,created_at,id);
CREATE UNIQUE INDEX changesets_active_target ON changesets(agent_id,target_path)
 WHERE status IN ('awaiting_approval','applying','recovery_required');
CREATE TABLE change_requests (
 owner_id TEXT NOT NULL, scope TEXT NOT NULL, request_key TEXT NOT NULL,
 request_sha256 TEXT NOT NULL CHECK(length(request_sha256)=64),
 resource_id TEXT NOT NULL REFERENCES operations(id), created_at TEXT NOT NULL,
 PRIMARY KEY(owner_id,scope,request_key)
);
