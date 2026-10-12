// 受控输入与草稿控制器之间的桥；水合回调只在恢复/明确冲突选择时调用。
import { useContext, useEffect, useMemo, useRef, useSyncExternalStore } from "react";
import type { DraftContent, DraftScope } from "../api/types";
import { DraftContext, DraftRegistry } from "./DraftProvider";
import { draftScopeKey, normalizeDraft } from "./draftController";

/** scope=null 不访问 daemon；正文不会进入 localStorage 或其他客户端持久存储。 */
export function usePersistentDraft(scope: DraftScope | null, content: DraftContent, onRestore: (content: DraftContent) => void) {
  const provider = useContext(DraftContext);
  const fallback = useMemo(() => { const registry = new DraftRegistry(); registry.configure(null, false, false); return registry; }, []);
  const registry = scope && provider ? provider : fallback;
  const key = scope ? draftScopeKey(scope) : "disabled";
  const controller = registry.get(scope ?? { kind: "composer", agent_id: "", session_id: "disabled" }, content);
  const snapshot = useSyncExternalStore(controller.subscribe, controller.getSnapshot, controller.getSnapshot);
  const anyDirty = useSyncExternalStore(registry.subscribe, registry.getSnapshot, registry.getSnapshot);
  const restoreCallback = useRef(onRestore);
  restoreCallback.current = onRestore;
  const previousInput = useRef<{ key: string; fingerprint: string } | null>(null);
  const restoredEpoch = useRef<{ key: string; epoch: number } | null>(null);
  const inputFingerprint = JSON.stringify(normalizeDraft(content));

  useEffect(() => {
    registry.activate(controller);
    const cached = controller.getSnapshot();
    if (scope && provider && cached.hydrated && JSON.stringify(cached.content) !== inputFingerprint) restoreCallback.current(cached.content);
  }, [registry, controller]);
  useEffect(() => {
    if (previousInput.current?.key !== key) { previousInput.current = { key, fingerprint: inputFingerprint }; return; }
    if (previousInput.current.fingerprint !== inputFingerprint) {
      previousInput.current = { key, fingerprint: inputFingerprint };
      controller.setContent(content);
    }
  }, [controller, key, inputFingerprint, content]);
  useEffect(() => {
    if (!snapshot.restoreEpoch || (restoredEpoch.current?.key === key && restoredEpoch.current.epoch === snapshot.restoreEpoch)) return;
    restoredEpoch.current = { key, epoch: snapshot.restoreEpoch };
    restoreCallback.current(snapshot.content);
  }, [key, snapshot.restoreEpoch, snapshot.content]);
  useEffect(() => fallback.retain(), [fallback]);

  return {
    ...snapshot,
    anyDirty,
    capture: () => controller.capture(),
    saveNow: () => controller.saveNow(),
    clearAfterSend: (token: Parameters<typeof controller.clearAfterSend>[0]) => controller.clearAfterSend(token),
    discard: () => controller.discard(),
    resolveConflict: (choice: "local" | "remote") => controller.resolveConflict(choice),
    setBaseRevision: (id: string) => controller.setBaseRevision(id),
  };
}
