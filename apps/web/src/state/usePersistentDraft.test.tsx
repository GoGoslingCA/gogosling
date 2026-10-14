import { act, startTransition, StrictMode, Suspense, useContext, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider, QueryObserver } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Draft, DraftInput, DraftUpdateInput, VersionInfo } from "../api/types";
import { DraftContext, DraftProvider, type DraftRegistry } from "./DraftProvider";
import { usePersistentDraft } from "./usePersistentDraft";
import { DraftSaveStatus } from "../components/DraftSaveStatus";
import { queryKeys } from "./queryKeys";
import { Composer } from "../pages/chat/Composer";
import { StartPage } from "../pages/chat/StartPage";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("./ConnectionContext", () => ({ useApi: () => api }));
const scope = { kind: "composer" as const, agent_id: "agent-a", session_id: "session-a" };
const initial: Draft = { ...scope, id: "draft-a", owner_id: "local", text: "已保存草稿", version: 1, created_at: "2026-10-12T01:00:00Z", updated_at: "2026-10-12T01:00:00Z" };
let api: ReturnType<typeof fixture>;
let root: Root;
let client: QueryClient;
let container: HTMLDivElement;
function fixture() {
  let remote: Draft | null = initial;
  return {
    system: { version: vi.fn(async (): Promise<VersionInfo> => ({ version: "dev", capabilities: { "workbench.drafts": 1 } })) },
    workbench: {
      listDrafts: vi.fn(async () => remote ? [remote] : []),
      getDraft: vi.fn(async () => remote!),
      createDraft: vi.fn(async (input: DraftInput) => { remote = { ...initial, ...input, version: 1 }; return remote; }),
      updateDraft: vi.fn(async (_id: string, input: DraftUpdateInput) => { remote = { ...initial, ...input, version: input.expected_version + 1 }; return remote; }),
      deleteDraft: vi.fn(async () => { remote = null; }),
    },
  };
}
function Editor() {
  const [text, setText] = useState("");
  const draft = usePersistentDraft(scope, { text }, (restored) => setText(restored.text));
  return <><textarea aria-label="draft" value={text} onChange={(event) => setText(event.target.value)} /><button onClick={() => void draft.saveNow().catch(() => undefined)}>save</button><output>{String(draft.dirty)}</output><DraftSaveStatus draft={draft} /></>;
}
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  api = fixture();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  container = document.createElement("div"); document.body.append(container); root = createRoot(container);
});
afterEach(async () => { await act(async () => root.unmount()); client.clear(); container.remove(); vi.restoreAllMocks(); });
async function ready(assertion: () => void) {
  await vi.waitFor(async () => { await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); }); assertion(); }, { timeout: 2000, interval: 10 });
}
async function mount() { await act(async () => root.render(<StrictMode><QueryClientProvider client={client}><DraftProvider><Editor /></DraftProvider></QueryClientProvider></StrictMode>)); }
function input() { return container.querySelector("textarea")!; }
async function type(text: string) { await act(async () => { Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input(), text); input().dispatchEvent(new Event("input", { bubbles: true })); }); }

describe("持久草稿与受控输入集成", () => {
  it("StrictMode 下恢复、保存、重新挂载仍能读到内核确认的草稿", async () => {
    await mount(); await ready(() => expect(input().value).toBe("已保存草稿"));
    await type("继续修改"); expect(container.querySelector("output")?.textContent).toBe("true");
    await act(async () => container.querySelector<HTMLButtonElement>("button")!.click());
    await ready(() => expect(container.querySelector("output")?.textContent).toBe("false"));
    expect(api.workbench.updateDraft).toHaveBeenCalledWith("draft-a", expect.objectContaining({ text: "继续修改", expected_version: 1 }));
    await act(async () => root.unmount()); client.clear(); root = createRoot(container);
    await mount(); await ready(() => expect(input().value).toBe("继续修改"));
  });

  it("旧 daemon 缺少能力时不请求草稿 API，也不把正文写入浏览器存储", async () => {
    api.system.version.mockResolvedValue({ version: "old" });
    const store = vi.spyOn(Storage.prototype, "setItem");
    await mount(); await ready(() => expect(container.textContent).toContain("p3draft.unsupported"));
    await type("仅在内存"); await act(async () => container.querySelector<HTMLButtonElement>("button")!.click());
    expect(api.workbench.listDrafts).not.toHaveBeenCalled(); expect(api.workbench.createDraft).not.toHaveBeenCalled();
    expect(store).not.toHaveBeenCalled(); expect(input().value).toBe("仅在内存");
  });

  it("加载过程中输入不会被远端覆盖，点击明确选择后才恢复远端内容", async () => {
    let resolve!: (value: Draft[]) => void;
    api.workbench.listDrafts.mockReturnValue(new Promise((yes) => { resolve = yes; }));
    await mount(); await ready(() => expect(api.workbench.listDrafts).toHaveBeenCalled());
    await type("先写下的新想法"); await act(async () => resolve([initial]));
    await ready(() => expect(container.textContent).toContain("p3draft.conflictHint"));
    expect(input().value).toBe("先写下的新想法");
    const useRemote = Array.from(container.querySelectorAll<HTMLButtonElement>("button")).find((button) => button.textContent === "p3draft.useRemote")!;
    await act(async () => useRemote.click());
    expect(input().value).toBe("已保存草稿"); expect(api.workbench.updateDraft).not.toHaveBeenCalled();
  });
});

describe("草稿回收与 React 提交", () => {
  it("同一个 Provider 保留期间关闭编辑器回收实例和查询正文，再打开恢复更新后的远端", async () => {
    let registry!: DraftRegistry;
    function Inspect() { registry = useContext(DraftContext)!; return null; }
    const render = async (visible: boolean) => { await act(async () => root.render(<StrictMode><QueryClientProvider client={client}><DraftProvider><Inspect />{visible ? <Editor /> : null}</DraftProvider></QueryClientProvider></StrictMode>)); };
    await render(true); await ready(() => expect(input().value).toBe(initial.text));
    const original = registry.get(scope, { text: "" });
    expect(client.getQueriesData({ queryKey: queryKeys.drafts })).toHaveLength(0);
    await render(false);
    expect(registry.read(original).hydrated).toBe(false);
    api.workbench.listDrafts.mockResolvedValue([{ ...initial, text: "另一窗口继续了工作", version: 5 }]);
    await render(true); await ready(() => expect(input().value).toBe("另一窗口继续了工作"));
    await type("在最新版本继续"); await act(async () => container.querySelector<HTMLButtonElement>("button")!.click());
    await ready(() => expect(api.workbench.updateDraft).toHaveBeenCalledWith(initial.id, expect.objectContaining({ expected_version: 5 })));
    expect(client.getQueriesData({ queryKey: queryKeys.drafts })).toHaveLength(0);
  });

  it("活跃查询不被正文清理误删，保存后仍更新其观察值", async () => {
    const observer = new QueryObserver(client, { queryKey: queryKeys.draft(initial.id), queryFn: async () => initial, staleTime: Infinity });
    const listener = vi.fn(); const unsubscribe = observer.subscribe(listener);
    let registry!: DraftRegistry;
    function Inspect() { registry = useContext(DraftContext)!; return null; }
    await act(async () => root.render(<StrictMode><QueryClientProvider client={client}><DraftProvider><Inspect /><Editor /></DraftProvider></QueryClientProvider></StrictMode>));
    await ready(() => expect(input().value).toBe(initial.text));
    await type("观察中的新稿"); await act(async () => container.querySelector<HTMLButtonElement>("button")!.click());
    await ready(() => expect(client.getQueryData<Draft>(queryKeys.draft(initial.id))?.text).toBe("观察中的新稿"));
    const observedQuery = client.getQueryCache().find({ queryKey: queryKeys.draft(initial.id), exact: true });
    expect(observedQuery?.getObserversCount()).toBe(1);
    await act(async () => registry.get(scope, { text: "" }).discard());
    expect(client.getQueryCache().find({ queryKey: queryKeys.draft(initial.id), exact: true })).toBe(observedQuery);
    expect(observedQuery?.getObserversCount()).toBe(1);
    unsubscribe();
  });

  it("startTransition 中暂停的旧渲染遇到原读者离开后，恢复提交会取得新实例", async () => {
    let unmountOriginal!: () => void, startPending!: () => void;
    let unblock!: () => void;
    let blocked = true, suspendedRenders = 0;
    const gate = new Promise<void>((resolve) => { unblock = resolve; });
    function PendingEditor() {
      const [text, setText] = useState("");
      const draft = usePersistentDraft(scope, { text }, (restored) => setText(restored.text));
      if (blocked) { suspendedRenders += 1; throw gate; }
      return <><textarea aria-label="draft" value={text} onChange={(event) => setText(event.target.value)} /><button onClick={() => void draft.saveNow()}>save pending</button></>;
    }
    function Harness() {
      const [original, setOriginal] = useState(true), [pending, setPending] = useState(false);
      unmountOriginal = () => setOriginal(false); startPending = () => setPending(true);
      return <>{original ? <Editor /> : null}<Suspense fallback={<span>waiting</span>}>{pending ? <PendingEditor /> : null}</Suspense></>;
    }
    await act(async () => root.render(<StrictMode><QueryClientProvider client={client}><DraftProvider><Harness /></DraftProvider></QueryClientProvider></StrictMode>));
    await ready(() => expect(input().value).toBe(initial.text));
    await act(async () => startTransition(startPending));
    expect(suspendedRenders).toBeGreaterThan(0);
    await act(async () => unmountOriginal());
    api.workbench.listDrafts.mockResolvedValue([{ ...initial, text: "暂停期间远端已更新", version: 7 }]);
    blocked = false; await act(async () => unblock());
    await ready(() => expect(input().value).toBe("暂停期间远端已更新"));
    await type("并发恢复后新输入"); await act(async () => container.querySelector<HTMLButtonElement>("button")!.click());
    await ready(() => expect(api.workbench.updateDraft).toHaveBeenCalledWith(initial.id, expect.objectContaining({ expected_version: 7, text: "并发恢复后新输入" })));
  });
});

describe("连接级草稿退出保护", () => {
  it("只读水合不提示退出，显式操作在编辑页卸载后仍受保护", async () => {
    let finishRead!: (value: Draft[]) => void;
    api.workbench.listDrafts.mockReturnValueOnce(new Promise((resolve) => { finishRead = resolve; }));
    let registry!: DraftRegistry;
    function Inspect() { registry = useContext(DraftContext)!; return null; }
    const render = async (visible: boolean) => { await act(async () => root.render(<StrictMode><QueryClientProvider client={client}><DraftProvider><Inspect />{visible ? <Editor /> : <div>settings</div>}</DraftProvider></QueryClientProvider></StrictMode>)); };
    const closing = () => { const event = new Event("beforeunload", { cancelable: true }); window.dispatchEvent(event); return event.defaultPrevented; };
    await render(true); expect(closing()).toBe(false);
    await act(async () => finishRead([initial])); await ready(() => expect(input().value).toBe(initial.text));
    const controller = registry.get(scope, { text: "" }); let release!: () => void;
    await act(async () => { release = controller.retainOperation(); });
    await render(false); expect(closing()).toBe(true);
    await act(async () => release()); expect(closing()).toBe(false);
  });

  it("真实 Composer 卸载到设置后上传仍被保护，保存失败保留引用及退出提示", async () => {
    let finishUpload!: (attachment: { name: string; path: string; size: number }) => void;
    const uploaded = { name: "notes.txt", path: "inbound/notes.txt", size: 10 };
    const upload = vi.fn(() => new Promise<typeof uploaded>((resolve) => { finishUpload = resolve; }));
    let registry!: DraftRegistry;
    function Inspect() { registry = useContext(DraftContext)!; return null; }
    const render = async (visible: boolean) => { await act(async () => root.render(<StrictMode><QueryClientProvider client={client}><DraftProvider><Inspect />{visible ? <Composer draftKey={scope.session_id} agentId={scope.agent_id} disabled={false} busy={false} activeRunId={null} onSend={async () => undefined} onCancel={() => undefined} upload={upload} /> : <div>settings</div>}</DraftProvider></QueryClientProvider></StrictMode>)); };
    const closing = () => { const event = new Event("beforeunload", { cancelable: true }); window.dispatchEvent(event); return event.defaultPrevented; };
    await render(true); await ready(() => expect(input().value).toBe(initial.text));
    await act(async () => {
      const files = container.querySelector('input[type="file"]')!;
      Object.defineProperty(files, "files", { configurable: true, value: [new File(["note"], "notes.txt")] });
      files.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await render(false); expect(container.querySelector("textarea")).toBeNull(); expect(closing()).toBe(true);
    await act(async () => finishUpload(uploaded));
    const controller = registry.get(scope, { text: "" });
    expect(controller.getSnapshot().content).toMatchObject({ text: initial.text, attachments: [uploaded] });
    api.workbench.updateDraft.mockRejectedValueOnce(new Error("offline"));
    await act(async () => { await expect(controller.saveNow()).rejects.toThrow("offline"); });
    expect(closing()).toBe(true); expect(controller.getSnapshot().dirty).toBe(true);
    await render(true); await ready(() => expect(container.querySelector(".attachment-chip")?.textContent).toContain(uploaded.name));
    expect(input().value).toBe(initial.text);
  });
});

describe("尚无 Agent 时的首页草稿保护", () => {
  it.each([false, true])("Provider 内无 Agent、加载状态 %s 时，本地输入仍提示未保存并阻止关闭", async (loading) => {
    const dirtyChanged = vi.fn();
    await act(async () => root.render(<StrictMode><QueryClientProvider client={client}><DraftProvider><MemoryRouter><StartPage agents={[]} sessions={[]} loading={loading} error={null} busy={false} timeZone="UTC" onStart={async () => undefined} onSelect={() => undefined} onHistory={() => undefined} onDraftDirtyChange={dirtyChanged} /></MemoryRouter></DraftProvider></QueryClientProvider></StrictMode>));
    expect(input().disabled).toBe(false);
    await type("配置 Agent 前写下的想法");
    expect(dirtyChanged).toHaveBeenLastCalledWith(true);
    const closing = new Event("beforeunload", { cancelable: true }); window.dispatchEvent(closing);
    expect(closing.defaultPrevented).toBe(true);
    expect(api.workbench.listDrafts).not.toHaveBeenCalled();
    await type(""); expect(dirtyChanged).toHaveBeenLastCalledWith(false);
    const cleared = new Event("beforeunload", { cancelable: true }); window.dispatchEvent(cleared);
    expect(cleared.defaultPrevented).toBe(false);
  });
});

describe("缓存草稿恢复的单次交付", () => {
  it("重新打开未保存的大草稿时只回写一次正文，StrictMode 不重复恢复", async () => {
    const restored = vi.fn();
    function TrackedEditor() {
      const [text, setText] = useState("");
      const draft = usePersistentDraft(scope, { text }, (content) => { restored(content); setText(content.text); });
      return <><textarea value={text} onChange={event => setText(event.target.value)} /><output>{String(draft.dirty)}</output></>;
    }
    const render = async (visible: boolean) => { await act(async () => root.render(<StrictMode><QueryClientProvider client={client}><DraftProvider>{visible ? <TrackedEditor /> : <div>settings</div>}</DraftProvider></QueryClientProvider></StrictMode>)); };
    await render(true); await ready(() => expect(input().value).toBe(initial.text));
    const text = "draft line\n".repeat(13107); await type(text); await render(false);
    restored.mockClear(); await render(true);
    expect(input().value).toBe(text); expect(container.querySelector("output")?.textContent).toBe("true");
    expect(restored).toHaveBeenCalledTimes(1);
    expect(api.workbench.updateDraft).not.toHaveBeenCalled();
  });
});
