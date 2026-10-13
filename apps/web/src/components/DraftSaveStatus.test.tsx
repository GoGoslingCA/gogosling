import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { usePersistentDraft } from "../state/usePersistentDraft";
import { DraftSaveStatus } from "./DraftSaveStatus";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
function state(): ReturnType<typeof usePersistentDraft> {
  const token = { editVersion: 0, fingerprint: "draft" };
  return { content: { text: "本地输入" }, hydrated: true, status: "unsupported", dirty: true, anyDirty: true, restored: true, error: null, conflict: null, restoreEpoch: 1, capture: () => token, retainOperation: () => () => undefined, setContent: () => undefined, addAttachment: () => undefined, subscribeReleased: () => () => undefined, saveNow: async () => token, clearAfterSend: async () => undefined, discard: async () => undefined, resolveConflict: async () => undefined, setBaseRevision: () => undefined };
}
function render(draft: ReturnType<typeof usePersistentDraft>) { const host = document.createElement("div"); host.innerHTML = renderToStaticMarkup(<DraftSaveStatus draft={draft} localFiles />); return host; }

describe("简短草稿状态", () => {
  it("主状态简短，刷新丢稿、已恢复可能发送及本地文件后果保留在折叠详情", () => {
    const host = render(state());
    expect(host.querySelector('[role="status"]')?.textContent).toContain("compact.memoryOnly");
    const details = host.querySelector<HTMLDetailsElement>(".draft-explainer")!;
    expect(details.open).toBe(false);
    expect(details.textContent).toContain("p3draft.unsupported");
    expect(details.textContent).toContain("p3draft.restored");
    expect(details.textContent).toContain("p3draft.localFiles");
  });

  it("冲突的两个明确选择始终可操作，远端全文按需展开且不丢失", () => {
    const draft = state();
    draft.status = "conflict";
    draft.conflict = { id: "draft-a", owner_id: "local", kind: "composer", agent_id: "a", text: "完整远端文字", version: 2, created_at: "2026-10-12T00:00:00Z", updated_at: "2026-10-12T00:00:00Z" };
    const host = render(draft);
    const buttons = host.querySelectorAll(".draft-conflict button");
    expect(buttons).toHaveLength(2);
    for (const button of buttons) expect(button.closest("details")).toBeNull();
    expect(host.querySelector(".draft-conflict pre")?.textContent).toBe("完整远端文字");
    expect(host.querySelector<HTMLDetailsElement>(".draft-conflict details")?.open).toBe(false);
  });
});
