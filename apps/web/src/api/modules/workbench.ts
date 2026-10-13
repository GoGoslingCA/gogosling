// P3A 工作台资源：所有请求继续经过 ApiClient 的鉴权、错误与取消通道。
import type { ApiClient } from "../client";
import type { Artifact, ArtifactInput, ArtifactRevision, ArtifactRevisionInput, Draft, DraftInput, DraftScope, DraftUpdateInput, ListResponse, PageQuery, Project, ProjectInput, ProjectMaterial, ProjectPatch, Session, SessionProjectInput } from "../types";
import type { ChangeSet, ChangeInput, ChangeDiff } from "../types";

/** 项目、资料、草稿与不可变成果版本的 HTTP 客户端。 */
export function workbenchApi(client: ApiClient) {
  const project = (id: string) => `/api/v1/projects/${encodeURIComponent(id)}`;
  const draft = (id: string) => `/api/v1/drafts/${encodeURIComponent(id)}`;
  const artifact = (id: string) => `/api/v1/artifacts/${encodeURIComponent(id)}`;
  const revisions = (id: string) => `${artifact(id)}/revisions`;
  const revision = (id: string, revisionId: string) => `${revisions(id)}/${encodeURIComponent(revisionId)}`;
  return {
    listChanges: async (sessionId: string): Promise<ChangeSet[]> => (await client.get<ListResponse<ChangeSet>>(`/api/v1/sessions/${encodeURIComponent(sessionId)}/changesets`, { query: { limit: 200 } })).items ?? [],
    prepareChange: (sessionId: string, input: ChangeInput): Promise<ChangeSet> => client.post(`/api/v1/sessions/${encodeURIComponent(sessionId)}/changesets`, input),
    getChange: (id: string): Promise<ChangeSet> => client.get(`/api/v1/changesets/${encodeURIComponent(id)}`),
    changeDiff: (id: string): Promise<ChangeDiff> => client.get(`/api/v1/changesets/${encodeURIComponent(id)}/diff`),
    applyChange: (c: ChangeSet, key: string): Promise<ChangeSet> => client.post(`/api/v1/changesets/${encodeURIComponent(c.id)}/apply`, { expected_version: c.version, preview_digest: c.preview_digest }, { headers: { "Idempotency-Key": key } }),
    undoChange: (c: ChangeSet): Promise<ChangeSet> => client.post(`/api/v1/changesets/${encodeURIComponent(c.id)}/undo-preview`, { expected_version: c.version }),
    cancelChange: (c: ChangeSet): Promise<ChangeSet> => client.post(`/api/v1/changesets/${encodeURIComponent(c.id)}/cancel`, { expected_version: c.version }),
    reconcileChange: (c: ChangeSet): Promise<ChangeSet> => client.post(`/api/v1/changesets/${encodeURIComponent(c.id)}/reconcile`, { expected_version: c.version }),
    listProjects: async (query: PageQuery & { agent_id?: string; status?: Project["status"] } = {}, signal?: AbortSignal): Promise<Project[]> => (await client.get<ListResponse<Project>>("/api/v1/projects", { query: { ...query }, signal })).items ?? [],
    getProject: (id: string): Promise<Project> => client.get(project(id)),
    createProject: (input: ProjectInput): Promise<Project> => client.post("/api/v1/projects", input),
    updateProject: (id: string, input: ProjectPatch): Promise<Project> => client.patch(project(id), input),
    listMaterials: async (projectId: string, query: PageQuery = {}, signal?: AbortSignal): Promise<ProjectMaterial[]> => (await client.get<ListResponse<ProjectMaterial>>(`${project(projectId)}/materials`, { query: { ...query }, signal })).items ?? [],
    uploadMaterial: (projectId: string, file: File): Promise<ProjectMaterial> => { const form = new FormData(); form.append("file", file, file.name); return client.post(`${project(projectId)}/materials`, form); },
    removeMaterial: (projectId: string, materialId: string): Promise<void> => client.delete(`${project(projectId)}/materials/${encodeURIComponent(materialId)}`),
    setSessionProject: (sessionId: string, input: SessionProjectInput): Promise<Session> => client.request("PUT", `/api/v1/sessions/${encodeURIComponent(sessionId)}/project`, { body: input }),
    listDrafts: async (query: PageQuery & Partial<Omit<DraftScope, "base_revision_id">> = {}): Promise<Draft[]> => (await client.get<ListResponse<Draft>>("/api/v1/drafts", { query: { ...query } })).items ?? [],
    getDraft: (id: string): Promise<Draft> => client.get(draft(id)),
    createDraft: (input: DraftInput): Promise<Draft> => client.post("/api/v1/drafts", input),
    updateDraft: (id: string, input: DraftUpdateInput): Promise<Draft> => client.request("PUT", draft(id), { body: input }),
    deleteDraft: (id: string, expectedVersion: number): Promise<void> => client.delete(draft(id), { query: { expected_version: expectedVersion } }),
    listArtifacts: async (scope: ({ session_id: string; project_id?: never } | { project_id: string; session_id?: never }) & PageQuery, signal?: AbortSignal): Promise<Artifact[]> => {
      const path = scope.session_id ? `/api/v1/sessions/${encodeURIComponent(scope.session_id)}/artifacts` : `${project(scope.project_id!)}/artifacts`;
      return (await client.get<ListResponse<Artifact>>(path, { query: { limit: scope.limit, offset: scope.offset }, signal })).items ?? [];
    },
    getArtifact: (id: string): Promise<Artifact> => client.get(artifact(id)),
    createArtifact: (sessionId: string, input: ArtifactInput, idempotencyKey?: string): Promise<Artifact> => client.post(`/api/v1/sessions/${encodeURIComponent(sessionId)}/artifacts`, input, idempotencyKey ? { headers: { "Idempotency-Key": idempotencyKey } } : {}),
    listRevisions: async (id: string, query: PageQuery = {}): Promise<ArtifactRevision[]> => (await client.get<ListResponse<ArtifactRevision>>(revisions(id), { query: { ...query } })).items ?? [],
    createRevision: (id: string, input: ArtifactRevisionInput): Promise<ArtifactRevision> => client.post(revisions(id), input),
    getRevision: (id: string, revisionId: string): Promise<ArtifactRevision> => client.get(revision(id, revisionId)),
    readRevision: (id: string, revisionId: string): Promise<string> => client.get(`${revision(id, revisionId)}/content`, { responseType: "text" }),
    downloadRevision: (id: string, revisionId: string): Promise<Blob> => client.get(`${revision(id, revisionId)}/download`, { responseType: "blob" }),
  };
}

export type WorkbenchApi = ReturnType<typeof workbenchApi>;
