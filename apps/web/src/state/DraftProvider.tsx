// 每个连接拥有一个内存草稿控制器集合；切换页面不清空尚未成功持久化的编辑。
import { createContext, useEffect, useMemo, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { Draft, DraftContent, DraftScope } from "../api/types";
import { useApi } from "./ConnectionContext";
import { useWorkbenchCapabilities } from "./useWorkbenchCapabilities";
import { queryKeys } from "./queryKeys";
import { DraftController, draftScopeKey, type DraftApi, type DraftSnapshot } from "./draftController";

// 已退出集合的快照必须与渲染时不同，useSyncExternalStore 才能在并发提交前重新读取。
const releasedSnapshot: DraftSnapshot = { content: { text: "" }, hydrated: false, status: "loading", dirty: false, restored: false, error: null, conflict: null, restoreEpoch: 0 };

/** 控制器只在当前连接的组件树中共享，不使用模块全局业务缓存。 */
export class DraftRegistry {
  private controllers = new Map<string, DraftController>();
  private listeners = new Set<() => void>();
  private api: DraftApi | null = null;
  private supported = false;
  private checking = true;
  private dirty = false;
  // 每次编辑只更新当前控制器，避免扫描所有曾打开的作用域。
  private dirtyControllers = new Set<DraftController>();
  private mounts = 0;
  private readers = new Map<DraftController, number>();
  private keys = new WeakMap<DraftController, string>();
  private scopes = new WeakMap<DraftController, DraftScope>();
  private releasedListeners = new Set<(scope: DraftScope, content: DraftContent) => void>();
  private candidates = new Set<DraftController>();
  private releaseQueued = false;
  /** 外部状态只标记未确认编辑或显式操作，不暴露正文，也不因只读加载提示退出。 */
  getSnapshot = () => this.dirty;
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  private changed = (controller: DraftController) => {
    if (controller.getSnapshot().dirty || controller.hasPendingOperation()) this.dirtyControllers.add(controller);
    else this.dirtyControllers.delete(controller);
    this.publishDirty();
    this.scheduleRelease(controller);
  };
  private scheduleRelease(controller: DraftController) {
    if (this.readers.has(controller) || !controller.canRelease()) return;
    this.candidates.add(controller);
    if (this.releaseQueued) return;
    this.releaseQueued = true;
    // 同一提交中的订阅重挂载可撤销候选；不增加轮询或每个草稿一个计时器。
    queueMicrotask(() => {
      this.releaseQueued = false;
      for (const candidate of this.candidates) {
        const key = this.keys.get(candidate);
        if (key !== undefined && this.controllers.get(key) === candidate && !this.readers.has(candidate) && candidate.canRelease()) {
          this.controllers.delete(key);
          const scope = this.scopes.get(candidate), content = candidate.getSnapshot().content;
          candidate.dispose();
          if (scope) for (const listener of this.releasedListeners) listener(scope, content);
        }
      }
      this.candidates.clear();
    });
  }
  /** 同步通知尚存的输入界面释放同一份已保存副本；不发布正文事件或写入存储。 */
  subscribeReleased = (listener: (scope: DraftScope, content: DraftContent) => void) => {
    this.releasedListeners.add(listener);
    return () => { this.releasedListeners.delete(listener); };
  };
  /** 提交时重新校验实例，阻止并发渲染持有已回收控制器。 */
  read(controller: DraftController): DraftSnapshot {
    return this.controllers.get(this.keys.get(controller) ?? "") === controller ? controller.getSnapshot() : releasedSnapshot;
  }
  /** UI 订阅就是占用凭据；只有最后一个读者离开后才考虑回收。 */
  subscribeDraft(controller: DraftController, listener: () => void): () => void {
    if (this.read(controller) === releasedSnapshot) return () => undefined;
    this.candidates.delete(controller);
    this.readers.set(controller, (this.readers.get(controller) ?? 0) + 1);
    const unsubscribe = controller.subscribe(listener);
    let released = false;
    return () => {
      if (released) return;
      released = true;
      unsubscribe();
      const count = (this.readers.get(controller) ?? 1) - 1;
      if (count > 0) this.readers.set(controller, count);
      else { this.readers.delete(controller); this.scheduleRelease(controller); }
    };
  }
  private publishDirty() {
    const dirty = this.dirtyControllers.size > 0;
    if (this.dirty === dirty) return;
    this.dirty = dirty;
    for (const listener of this.listeners) listener();
  }
  /** 同一作用域重新挂载时继续使用其尚未确认的内存草稿。 */
  get(scope: DraftScope, content: DraftContent): DraftController {
    const key = draftScopeKey(scope);
    let controller = this.controllers.get(key);
    if (!controller) {
      controller = new DraftController(scope, content);
      this.controllers.set(key, controller);
      this.keys.set(controller, key);
      const created = controller;
      if (created.getSnapshot().dirty) this.dirtyControllers.add(created);
      created.subscribe(() => this.changed(created));
      // 在 effect 中启动网络，渲染阶段只构造本地状态。
    }
    this.scopes.set(controller, scope);
    controller.setScope(scope);
    return controller;
  }
  /** 能力发现的结果适用于该连接内所有草稿。 */
  configure(api: DraftApi | null, supported: boolean, checking: boolean) {
    this.api = api; this.supported = supported; this.checking = checking;
    for (const controller of this.controllers.values()) controller.configure(api, supported, checking);
    this.publishDirty();
  }
  /** 新挂载的作用域使用当前能力结果启动恢复。 */
  activate(controller: DraftController) { if (this.read(controller) === releasedSnapshot) return; controller.configure(this.api, this.supported, this.checking); this.changed(controller); }
  /** StrictMode 的同一轮卸载/重挂载不销毁控制器，真正离开后才释放。 */
  retain() {
    this.mounts += 1;
    return () => {
      this.mounts -= 1;
      queueMicrotask(() => { if (!this.mounts) this.dispose(); });
    };
  }
  /** 断开连接时释放计时器和订阅，不跨主体保留草稿。 */
  dispose() { for (const controller of this.controllers.values()) controller.dispose(); this.controllers.clear(); this.readers.clear(); this.candidates.clear(); this.dirtyControllers.clear(); this.dirty = false; this.listeners.clear(); this.releasedListeners.clear(); }
}

export const DraftContext = createContext<DraftRegistry | null>(null);

/** 放在连接与 QueryClient Provider 以内，未提供时 usePersistentDraft 自动兼容内存模式。 */
export function DraftProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const capability = useWorkbenchCapabilities();
  const registry = useMemo(() => new DraftRegistry(), [api]);
  const gateway = useMemo<DraftApi>(() => {
    const releaseQuery = (queryKey: readonly unknown[]) => {
      const query = queryClient.getQueryCache().find({ queryKey, exact: true });
      if (query && query.getObserversCount() === 0 && query.state.fetchStatus === "idle") queryClient.removeQueries({ queryKey, exact: true });
    };
    const updateObserved = (saved: Draft) => {
      const queryKey = queryKeys.draft(saved.id);
      if (queryClient.getQueryCache().find({ queryKey, exact: true })?.getObserversCount()) queryClient.setQueryData(queryKey, saved);
      else releaseQuery(queryKey);
    };
    return {
      // 控制器负责正文恢复，查询缓存只合并在途读取，不再额外保留五分钟正文。
      listDrafts: async (query) => {
        const queryKey = [...queryKeys.drafts, "scope", query];
        try { return await queryClient.fetchQuery({ queryKey, queryFn: () => api.workbench.listDrafts(query), staleTime: 0 }); }
        finally { releaseQuery(queryKey); }
      },
      getDraft: async (id) => {
        const queryKey = queryKeys.draft(id);
        try { return await queryClient.fetchQuery({ queryKey, queryFn: () => api.workbench.getDraft(id), staleTime: 0 }); }
        finally { releaseQuery(queryKey); }
      },
      createDraft: async (input) => { const saved = await api.workbench.createDraft(input); updateObserved(saved); return saved; },
      updateDraft: async (id, input) => { const saved = await api.workbench.updateDraft(id, input); updateObserved(saved); return saved; },
      deleteDraft: async (id, version) => {
        await api.workbench.deleteDraft(id, version);
        const queryKey = queryKeys.draft(id);
        releaseQuery(queryKey);
        void queryClient.invalidateQueries({ queryKey, exact: true });
      },
    };
  }, [api, queryClient]);
  useEffect(() => { registry.configure(gateway, capability.drafts, capability.loading); }, [registry, gateway, capability.drafts, capability.loading]);
  useEffect(() => registry.retain(), [registry]);
  useEffect(() => {
    // 编辑页卸载后后台保存仍可能失败；守卫与连接同寿命，读取同步状态避免 effect 延迟。
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (!registry.getSnapshot()) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", beforeUnload);
    return () => window.removeEventListener("beforeunload", beforeUnload);
  }, [registry]);
  return <DraftContext.Provider value={registry}>{children}</DraftContext.Provider>;
}
