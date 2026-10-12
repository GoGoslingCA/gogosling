import { act, StrictMode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Draft, DraftInput, DraftUpdateInput, VersionInfo } from "../api/types";
import { DraftProvider } from "./DraftProvider";
import { usePersistentDraft } from "./usePersistentDraft";
import { DraftSaveStatus } from "../components/DraftSaveStatus";

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
