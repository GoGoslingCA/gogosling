import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../api/client";
import type { Artifact, ArtifactRevision, ArtifactRevisionInput, ChangeSet, ChangeDiff, Draft, DraftInput, DraftUpdateInput, VersionInfo } from "../../api/types";
import { DraftProvider } from "../../state/DraftProvider";
import { queryKeys } from "../../state/queryKeys";
import { SavedArtifactEditor } from "./SavedArtifactEditor";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("../../state/ConnectionContext", () => ({ useApi: () => api }));
vi.mock("../../components/Toast", () => ({ useToast: () => ({ show: toast }) }));

const date = "2026-10-12T00:00:00Z";
const toast = vi.fn();
let root: Root;
let host: HTMLDivElement;
let client: QueryClient;
let api: ReturnType<typeof fixture>;
let artifacts: Map<string, Artifact>;
let revisions: Map<string, ArtifactRevision[]>;
let contents: Map<string, string>;
let drafts: Map<string, Draft>;
let nextDraft: number;
let onDirtyChange = vi.fn<(dirty: boolean) => void>();

function seedArtifact(id: string, sessionId: string): Artifact {
  const artifact: Artifact = { id, agent_id: "agent-a", session_id: sessionId, title: `成果 ${id}`, kind: "document", filename: `${id}.md`, media_type: "text/markdown", latest_revision_id: `${id}-r2`, version: 2, source: { message_id: `${id}-message`, message_seq: 2, source_sha256: "source-digest", partial: false }, created_at: date, updated_at: date };
  artifacts.set(id, artifact);
  revisions.set(id, [2, 1].map((number): ArtifactRevision => ({ id: `${id}-r${number}`, artifact_id: id, number, ...(number > 1 ? { parent_revision_id: `${id}-r1` } : {}), filename: artifact.filename, media_type: artifact.media_type, sha256: "digest", byte_size: 10, created_by: "local", created_at: date })));
  contents.set(`${id}:${id}-r1`, `${id} 第一版原稿`);
  contents.set(`${id}:${id}-r2`, `${id} 第二版原稿`);
  return artifact;
}

function seedDraft(artifact: Artifact, base = `${artifact.id}-r1`, text = "基于旧版本尚未完成的编辑") {
  const draft: Draft = { id: `draft-${artifact.id}`, owner_id: "local", kind: "artifact", agent_id: artifact.agent_id, session_id: artifact.session_id, artifact_id: artifact.id, base_revision_id: base, text, filename: artifact.filename, version: 3, created_at: date, updated_at: date };
  drafts.set(draft.id, draft);
  return draft;
}

function commitRevision(id: string, input: ArtifactRevisionInput): ArtifactRevision {
  const artifact = artifacts.get(id)!;
  if (artifact.latest_revision_id !== input.expected_latest_revision_id) throw new ApiError("conflict", "版本已变化", { status: 409, details: { current_latest_revision_id: artifact.latest_revision_id } });
  const revision: ArtifactRevision = { id: `${id}-r${artifact.version + 1}`, artifact_id: id, number: artifact.version + 1, parent_revision_id: artifact.latest_revision_id, filename: input.filename, media_type: input.media_type, sha256: "digest", byte_size: input.content.length, created_by: "local", created_at: date };
  contents.set(`${id}:${revision.id}`, input.content);
  revisions.set(id, [revision, ...revisions.get(id)!]);
  artifacts.set(id, { ...artifact, filename: revision.filename, media_type: revision.media_type, latest_revision_id: revision.id, version: revision.number });
  return revision;
}

function fixture() {
  return {
    system: { version: vi.fn(async (): Promise<VersionInfo> => ({ version: "dev", capabilities: { "workbench.drafts": 1, "workbench.artifacts": 1 } })) },
    sessions: { upload: vi.fn(async (_sessionId: string, file: File) => ({ name: file.name, path: `inbound/${file.name}`, size: file.size })) },
    workbench: {
      listChanges: vi.fn(async (): Promise<ChangeSet[]> => []),
      getChange: vi.fn(async (_id: string): Promise<ChangeSet> => { throw new ApiError("not_found", ""); }),
      changeDiff: vi.fn(async (_id: string): Promise<ChangeDiff> => { throw new ApiError("not_found", ""); }),
      prepareChange: vi.fn(), applyChange: vi.fn(), undoChange: vi.fn(), cancelChange: vi.fn(), reconcileChange: vi.fn(),
      getArtifact: vi.fn(async (id: string) => ({ ...artifacts.get(id)! })),
      listRevisions: vi.fn(async (id: string) => [...revisions.get(id)!]),
      readRevision: vi.fn(async (id: string, revisionId: string) => { const text = contents.get(`${id}:${revisionId}`); if (text === undefined) throw new ApiError("not_found", "不存在", { status: 404 }); return text; }),
      createRevision: vi.fn(async (id: string, input: ArtifactRevisionInput) => commitRevision(id, input)),
      listDrafts: vi.fn(async (query: { artifact_id?: string }) => [...drafts.values()].filter((draft) => draft.artifact_id === query.artifact_id)),
      getDraft: vi.fn(async (id: string) => { const draft = drafts.get(id); if (!draft) throw new ApiError("not_found", "不存在", { status: 404 }); return { ...draft }; }),
      createDraft: vi.fn(async (input: DraftInput): Promise<Draft> => {
        const existing = [...drafts.values()].find((draft) => draft.artifact_id === input.artifact_id);
        if (existing) throw new ApiError("conflict", "已有草稿", { status: 409, details: { existing_draft_id: existing.id } });
        const saved: Draft = { ...input, id: `draft-new-${++nextDraft}`, owner_id: "local", version: 1, created_at: date, updated_at: date };
        drafts.set(saved.id, saved); return { ...saved };
      }),
      updateDraft: vi.fn(async (id: string, input: DraftUpdateInput): Promise<Draft> => {
        const draft = drafts.get(id); if (!draft) throw new ApiError("not_found", "不存在", { status: 404 });
        if (draft.version !== input.expected_version) throw new ApiError("conflict", "草稿已变化", { status: 409, details: { current_version: draft.version } });
        const saved: Draft = { ...draft, ...input, version: draft.version + 1 }; drafts.set(id, saved); return { ...saved };
      }),
      deleteDraft: vi.fn(async (id: string, version: number) => {
        const draft = drafts.get(id); if (draft && draft.version !== version) throw new ApiError("conflict", "草稿已变化", { status: 409 });
        drafts.delete(id);
      }),
    },
  };
}

async function ready(assertion: () => void) {
  await vi.waitFor(async () => { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); }); assertion(); }, { interval: 10, timeout: 2500 });
}
async function mount(artifactId = "a", sessionId = "s1", visible = true) {
  await act(async () => root.render(<QueryClientProvider client={client}><DraftProvider><SavedArtifactEditor artifactId={artifactId} sessionId={sessionId} visible={visible} onClose={vi.fn()} onDirtyChange={onDirtyChange} /></DraftProvider></QueryClientProvider>));
}
function editor() { return host.querySelector<HTMLTextAreaElement>("textarea[aria-label='artifact.editor']"); }
function button(label: string) { const found = [...host.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent === label); if (!found) throw new Error(`未找到按钮 ${label}`); return found; }
async function type(text: string) {
  await act(async () => { const input = editor(); if (!input) throw new Error("未找到成果编辑器"); Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, text); input.dispatchEvent(new Event("input", { bubbles: true })); });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  artifacts = new Map(); revisions = new Map(); contents = new Map(); drafts = new Map(); nextDraft = 0;
  seedArtifact("a", "s1"); seedArtifact("b", "s2"); api = fixture(); toast.mockReset(); onDirtyChange = vi.fn<(dirty: boolean) => void>();
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); client.clear(); host.remove(); vi.restoreAllMocks(); });

describe("SavedArtifactEditor 版本与草稿一致性", () => {
  it("恢复旧基线草稿后用旧revision保存，409出现前后均不静默改用最新基线", async () => {
    seedDraft(artifacts.get("a")!);
    await mount(); await ready(() => expect(editor()?.value).toBe("基于旧版本尚未完成的编辑"));
    await ready(() => expect(button("versions.save").disabled).toBe(false));
    await act(async () => button("versions.save").click());
    await ready(() => expect(host.textContent).toContain("versions.conflict"));
    expect(api.workbench.createRevision).toHaveBeenCalledWith("a", expect.objectContaining({ expected_latest_revision_id: "a-r1", content: "基于旧版本尚未完成的编辑" }));
    expect(editor()?.value).toBe("基于旧版本尚未完成的编辑");
    expect(artifacts.get("a")?.latest_revision_id).toBe("a-r2");
    expect(button("versions.save").disabled).toBe(true);
    expect(api.workbench.createRevision).toHaveBeenCalledOnce();
  });

  it("版本冲突选择保留当前编辑后才采用最新基线，再保存为新版本", async () => {
    seedDraft(artifacts.get("a")!);
    await mount(); await ready(() => expect(editor()?.value).toBe("基于旧版本尚未完成的编辑"));
    await ready(() => expect(button("versions.save").disabled).toBe(false));
    await act(async () => button("versions.save").click());
    await ready(() => expect(host.textContent).toContain("versions.keepMine"));
    await act(async () => button("versions.keepMine").click());
    await ready(() => expect(button("versions.save").disabled).toBe(false));
    expect(editor()?.value).toBe("基于旧版本尚未完成的编辑");
    expect(api.workbench.createRevision).toHaveBeenCalledOnce();
    await act(async () => button("versions.save").click());
    await ready(() => expect(artifacts.get("a")?.latest_revision_id).toBe("a-r3"));
    expect(api.workbench.createRevision).toHaveBeenLastCalledWith("a", expect.objectContaining({ expected_latest_revision_id: "a-r2", content: "基于旧版本尚未完成的编辑" }));
    expect(contents.get("a:a-r2")).toBe("a 第二版原稿");
  });

  it("版本冲突选择载入远端后才替换正文，不自动创建版本", async () => {
    seedDraft(artifacts.get("a")!);
    await mount(); await ready(() => expect(editor()?.value).toBe("基于旧版本尚未完成的编辑"));
    await ready(() => expect(button("versions.save").disabled).toBe(false));
    await act(async () => button("versions.save").click());
    await ready(() => expect(host.textContent).toContain("versions.loadLatest"));
    expect(editor()?.value).toBe("基于旧版本尚未完成的编辑");
    await act(async () => button("versions.loadLatest").click());
    await ready(() => expect(editor()?.value).toBe("a 第二版原稿"));
    expect(api.workbench.createRevision).toHaveBeenCalledOnce();
    expect(button("versions.save").disabled).toBe(true);
  });

  it("晚到版本保存响应不覆盖提交后继续输入的内容", async () => {
    let finish!: () => void;
    api.workbench.createRevision.mockImplementation((id, input) => new Promise((resolve) => { finish = () => resolve(commitRevision(id, input)); }));
    await mount(); await ready(() => expect(editor()?.disabled).toBe(false)); await type("本次提交的版本");
    await act(async () => button("versions.save").click());
    await ready(() => expect(api.workbench.createRevision).toHaveBeenCalledOnce());
    await type("提交后继续输入的新内容");
    await act(async () => finish());
    await ready(() => expect(artifacts.get("a")?.latest_revision_id).toBe("a-r3"));
    expect(editor()?.value).toBe("提交后继续输入的新内容");
    expect(contents.get("a:a-r3")).toBe("本次提交的版本");
    await ready(() => expect(button("versions.save").disabled).toBe(false));
  });

  it("切换成果与会话后旧保存结果不能覆盖新成果正文或保存目标", async () => {
    let finish!: () => void;
    api.workbench.createRevision.mockImplementation((id, input) => new Promise((resolve) => { finish = () => resolve(commitRevision(id, input)); }));
    await mount(); await ready(() => expect(editor()?.disabled).toBe(false)); await type("A的修改");
    await act(async () => button("versions.save").click()); await ready(() => expect(api.workbench.createRevision).toHaveBeenCalledOnce());
    await mount("b", "s2"); await ready(() => expect(editor()?.value).toBe("b 第二版原稿")); await type("B的新修改");
    await act(async () => finish()); await ready(() => expect(artifacts.get("a")?.latest_revision_id).toBe("a-r3"));
    expect(editor()?.value).toBe("B的新修改");
    expect(api.workbench.createRevision).toHaveBeenCalledWith("a", expect.objectContaining({ content: "A的修改" }));
    expect(artifacts.get("b")?.latest_revision_id).toBe("b-r2");
    await ready(() => expect([...drafts.values()].some((draft) => draft.artifact_id === "a")).toBe(false));
    await mount("a", "s1"); await ready(() => expect(editor()?.value).toBe("A的修改"));
    expect(host.querySelector(".draft-conflict")).toBeNull();
  });

  it("成果不属于当前会话时不读取正文且不给编辑入口", async () => {
    await mount("a", "s2"); await ready(() => expect(host.textContent).toContain("versions.unavailable"));
    expect(editor()).toBeNull(); expect(api.workbench.readRevision).not.toHaveBeenCalled(); expect(api.workbench.createRevision).not.toHaveBeenCalled();
  });

  it("元数据刷新到新版本时仍保留当前编辑及旧基线直到用户选择", async () => {
    await mount(); await ready(() => expect(editor()?.disabled).toBe(false)); await type("尚未保存的本地编辑");
    const latest = commitRevision("a", { content: "另一窗口的新版本", filename: "a.md", media_type: "text/markdown", expected_latest_revision_id: "a-r2" });
    await act(async () => { client.setQueryData(queryKeys.artifact("a"), { ...artifacts.get("a")! }); });
    await ready(() => expect(editor()?.value).toBe("尚未保存的本地编辑"));
    await act(async () => button("versions.save").click());
    await ready(() => expect(api.workbench.createRevision).toHaveBeenCalled());
    expect(api.workbench.createRevision).toHaveBeenLastCalledWith("a", expect.objectContaining({ expected_latest_revision_id: "a-r2" }));
    expect(artifacts.get("a")?.latest_revision_id).toBe(latest.id);
  });

  it("历史预览中不能通过按钮或快捷键保存隐藏的当前草稿", async () => {
    await mount(); await ready(() => expect(editor()?.disabled).toBe(false)); await type("尚未保存的当前草稿");
    await act(async () => { const select = host.querySelector<HTMLSelectElement>("#version-history")!; select.value = "a-r1"; select.dispatchEvent(new Event("change", { bubbles: true })); });
    await ready(() => expect(host.textContent).toContain("versions.historyPreview"));
    expect(editor()).toBeNull(); expect(button("versions.save").disabled).toBe(true); expect(button("artifact.saveCopy").disabled).toBe(true);
    await act(async () => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "s", ctrlKey: true, cancelable: true })); });
    expect(api.workbench.createRevision).not.toHaveBeenCalled(); expect(api.sessions.upload).not.toHaveBeenCalled();
    await act(async () => button("artifact.edit").click());
    expect(editor()?.value).toBe("尚未保存的当前草稿");
  });

  it("采用远端基线的请求未完成时锁定编辑，避免晚到读取覆盖新输入", async () => {
    seedDraft(artifacts.get("a")!);
    await mount(); await ready(() => expect(editor()?.value).toBe("基于旧版本尚未完成的编辑"));
    await ready(() => expect(button("versions.save").disabled).toBe(false));
    await act(async () => button("versions.save").click()); await ready(() => expect(host.textContent).toContain("versions.loadLatest"));
    let finish!: (value: string) => void;
    api.workbench.readRevision.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    await act(async () => button("versions.loadLatest").click());
    expect(editor()?.disabled).toBe(true); expect(host.querySelector<HTMLInputElement>("#version-filename")?.disabled).toBe(true);
    expect(button("versions.loadLatest").disabled).toBe(true); expect(button("versions.keepMine").disabled).toBe(true);
    expect(editor()?.value).toBe("基于旧版本尚未完成的编辑");
    await act(async () => finish("a 第二版原稿"));
    await ready(() => expect(editor()?.disabled).toBe(false)); expect(editor()?.value).toBe("a 第二版原稿");
  });

  it("旧内核没有文件修改能力时不显示新tab也不请求任何change端点", async () => {
    await mount(); await ready(() => expect(editor()?.disabled).toBe(false));
    expect([...host.querySelectorAll("button")].some((item) => item.textContent === "changes.tab")).toBe(false);
    for (const endpoint of [api.workbench.listChanges, api.workbench.getChange, api.workbench.changeDiff, api.workbench.prepareChange, api.workbench.applyChange]) expect(endpoint).not.toHaveBeenCalled();
    expect(button("artifact.saveCopy")).not.toBeNull();
  });

  it("文件tab保留目标，未保存草稿不能预览且Cmd+S不会保存隐藏草稿", async () => {
    api.system.version.mockResolvedValue({ version: "dev", capabilities: { "workbench.drafts": 1, "workbench.artifacts": 1, "workbench.changesets": 1 } });
    await mount(); await ready(() => expect(editor()?.disabled).toBe(false));
    await act(async () => button("changes.tab").click());
    const path = host.querySelector<HTMLInputElement>('.file-change-form input:not([type="radio"])')!;
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(path, "docs/example.md"); path.dispatchEvent(new Event("input", { bubbles: true })); });
    await act(async () => button("artifact.edit").click()); await type("尚未保存的正文");
    await act(async () => button("changes.tab").click());
    expect(host.textContent).toContain("changes.saveFirst"); expect(host.querySelector(".file-change-form")).toBeNull();
    await act(async () => window.dispatchEvent(new KeyboardEvent("keydown", { key: "s", ctrlKey: true, cancelable: true })));
    expect(api.workbench.createRevision).not.toHaveBeenCalled(); expect(api.workbench.prepareChange).not.toHaveBeenCalled(); expect(api.sessions.upload).not.toHaveBeenCalled();
    await act(async () => button("artifact.edit").click()); await type("a 第二版原稿");
    await act(async () => button("changes.tab").click());
    expect(host.querySelector<HTMLInputElement>('.file-change-form input:not([type="radio"])')?.value).toBe("docs/example.md");
  });

  it("历史预览隐藏文件面板后不能申请已经准备好的变更", async () => {
    api.system.version.mockResolvedValue({ version: "dev", capabilities: { "workbench.drafts": 1, "workbench.artifacts": 1, "workbench.changesets": 1 } });
    const change: ChangeSet = { id: "change-a", owner_id: "local", agent_id: "agent-a", session_id: "s1", artifact_id: "a", revision_id: "a-r2", target_path: "docs/a.md", operation: "create", status: "prepared", version: 1, preview_digest: "preview", base_exists: false, base_sha256: "before", proposed_sha256: "after", proposed_size: 5, created_at: date, expires_at: new Date(Date.now() + 900_000).toISOString() };
    api.workbench.listChanges.mockResolvedValue([change]); api.workbench.getChange.mockResolvedValue(change); api.workbench.changeDiff.mockResolvedValue({ change, before: "", after: "saved" });
    await mount(); await ready(() => expect(editor()?.disabled).toBe(false));
    await act(async () => button("changes.tab").click()); await ready(() => expect(button("changes.requestApply").disabled).toBe(false));
    await act(async () => { const history = host.querySelector<HTMLSelectElement>("#version-history")!; history.value = "a-r1"; history.dispatchEvent(new Event("change", { bubbles: true })); });
    await ready(() => expect(host.textContent).toContain("versions.historyPreview"));
    expect(button("changes.tab").disabled).toBe(true); expect(button("changes.requestApply").disabled).toBe(true);
    await act(async () => button("changes.requestApply").click()); expect(api.workbench.applyChange).not.toHaveBeenCalled();
  });
});
