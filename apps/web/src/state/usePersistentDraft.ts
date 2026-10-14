// 受控输入与草稿控制器之间的桥；水合回调只在恢复/明确冲突选择时调用。
import { useCallback, useContext, useEffect, useMemo, useRef, useSyncExternalStore } from "react";
import type { DraftContent, DraftScope } from "../api/types";
import { DraftContext, DraftRegistry } from "./DraftProvider";
import { draftScopeKey, normalizeDraft, sameDraftContent } from "./draftController";

const cleanSnapshot = () => false;
const noSubscription = (_listener: () => void) => () => undefined;

/** scope=null 不访问 daemon；正文不会进入 localStorage 或其他客户端持久存储。 */
export function usePersistentDraft(scope: DraftScope | null, content: DraftContent, onRestore: (content: DraftContent) => void) {
  const provider = useContext(DraftContext);
  const fallback = useMemo(() => { const registry = new DraftRegistry(); registry.configure(null, false, false); return registry; }, []);
  const registry = scope && provider ? provider : fallback;
  // 路由加载期间作用域可能暂缺；全局保护与回收通知始终属于当前连接。
  const connectionRegistry = provider ?? fallback;
  const key = scope ? draftScopeKey(scope) : "disabled";
  const controller = registry.get(scope ?? { kind: "composer", agent_id: "", session_id: "disabled" }, content);
  const subscribe = useCallback((listener: () => void) => registry.subscribeDraft(controller, listener), [registry, controller]);
  const read = useCallback(() => registry.read(controller), [registry, controller]);
  const snapshot = useSyncExternalStore(subscribe, read, read);
  const connectionDirty = useSyncExternalStore(connectionRegistry.subscribe, connectionRegistry.getSnapshot, connectionRegistry.getSnapshot);
  // 无作用域时仍允许暂存输入；仅在本地集合与连接集合不同的情况下额外订阅。
  const localDirty = useSyncExternalStore(registry === connectionRegistry ? noSubscription : registry.subscribe,
    registry === connectionRegistry ? cleanSnapshot : registry.getSnapshot,
    registry === connectionRegistry ? cleanSnapshot : registry.getSnapshot);
  const anyDirty = connectionDirty || localDirty;
  const restoreCallback = useRef(onRestore);
  restoreCallback.current = onRestore;
  const previousInput = useRef<{ key: string; content: DraftContent } | null>(null);
  const restoredEpoch = useRef<{ controller: typeof controller; epoch: number } | null>(null);

  useEffect(() => {
    if (restoredEpoch.current?.controller !== controller) restoredEpoch.current = null;
    registry.activate(controller);
    const cached = registry.read(controller);
    if (scope && provider && cached.hydrated && !sameDraftContent(cached.content, content)
      && !(restoredEpoch.current?.controller === controller && restoredEpoch.current.epoch === cached.restoreEpoch)) {
      // 缓存恢复与下方 epoch 恢复共用确认点，StrictMode 重挂载也只交付一次。
      restoredEpoch.current = { controller, epoch: cached.restoreEpoch };
      restoreCallback.current(cached.content);
    }
  }, [registry, controller]);
  useEffect(() => {
    if (previousInput.current?.key !== key) { previousInput.current = { key, content: normalizeDraft(content) }; return; }
    if (!sameDraftContent(previousInput.current.content, content)) {
      previousInput.current = { key, content: normalizeDraft(content) };
      controller.setContent(content);
    }
  }, [controller, key, content]);
  useEffect(() => {
    if (!snapshot.restoreEpoch || (restoredEpoch.current?.controller === controller && restoredEpoch.current.epoch === snapshot.restoreEpoch)) return;
    restoredEpoch.current = { controller, epoch: snapshot.restoreEpoch };
    restoreCallback.current(snapshot.content);
  }, [controller, snapshot.restoreEpoch, snapshot.content]);
  useEffect(() => fallback.retain(), [fallback]);

  return {
    ...snapshot,
    anyDirty,
    subscribeReleased: connectionRegistry.subscribeReleased,
    capture: () => controller.capture(),
    retainOperation: () => controller.retainOperation(),
    setContent: (input: DraftContent) => controller.setContent(input),
    addAttachment: (attachment: Parameters<typeof controller.addAttachment>[0]) => { if (scope) controller.addAttachment(attachment); },
    saveNow: () => controller.saveNow(),
    clearAfterSend: (token: Parameters<typeof controller.clearAfterSend>[0]) => controller.clearAfterSend(token),
    discard: () => controller.discard(),
    resolveConflict: (choice: "local" | "remote") => controller.resolveConflict(choice),
    setBaseRevision: (id: string) => controller.setBaseRevision(id),
  };
}
