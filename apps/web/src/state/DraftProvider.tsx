// 每个连接拥有一个内存草稿控制器集合；切换页面不清空尚未成功持久化的编辑。
import { createContext, useEffect, useMemo, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { DraftContent, DraftScope } from "../api/types";
import { useApi } from "./ConnectionContext";
import { useWorkbenchCapabilities } from "./useWorkbenchCapabilities";
import { queryKeys } from "./queryKeys";
import { DraftController, draftScopeKey, type DraftApi } from "./draftController";

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
  /** 外部状态快照只有未确认编辑是否存在，不暴露正文。 */
  getSnapshot = () => this.dirty;
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  private changed = (controller: DraftController) => {
    if (controller.getSnapshot().dirty) this.dirtyControllers.add(controller);
    else this.dirtyControllers.delete(controller);
    this.publishDirty();
  };
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
      const created = controller;
      if (created.getSnapshot().dirty) this.dirtyControllers.add(created);
      created.subscribe(() => this.changed(created));
      // 在 effect 中启动网络，渲染阶段只构造本地状态。
    }
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
  activate(controller: DraftController) { controller.configure(this.api, this.supported, this.checking); this.changed(controller); }
  /** StrictMode 的同一轮卸载/重挂载不销毁控制器，真正离开后才释放。 */
  retain() {
    this.mounts += 1;
    return () => {
      this.mounts -= 1;
      queueMicrotask(() => { if (!this.mounts) this.dispose(); });
    };
  }
  /** 断开连接时释放计时器和订阅，不跨主体保留草稿。 */
  dispose() { for (const controller of this.controllers.values()) controller.dispose(); this.controllers.clear(); this.dirtyControllers.clear(); this.dirty = false; this.listeners.clear(); }
}

export const DraftContext = createContext<DraftRegistry | null>(null);

/** 放在连接与 QueryClient Provider 以内，未提供时 usePersistentDraft 自动兼容内存模式。 */
export function DraftProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const capability = useWorkbenchCapabilities();
  const registry = useMemo(() => new DraftRegistry(), [api]);
  const gateway = useMemo<DraftApi>(() => ({
    listDrafts: (query) => queryClient.fetchQuery({ queryKey: [...queryKeys.drafts, "scope", query], queryFn: () => api.workbench.listDrafts(query), staleTime: 0 }),
    getDraft: (id) => queryClient.fetchQuery({ queryKey: queryKeys.draft(id), queryFn: () => api.workbench.getDraft(id), staleTime: 0 }),
    createDraft: async (input) => { const saved = await api.workbench.createDraft(input); queryClient.setQueryData(queryKeys.draft(saved.id), saved); return saved; },
    updateDraft: async (id, input) => { const saved = await api.workbench.updateDraft(id, input); queryClient.setQueryData(queryKeys.draft(saved.id), saved); return saved; },
    deleteDraft: async (id, version) => { await api.workbench.deleteDraft(id, version); queryClient.removeQueries({ queryKey: queryKeys.draft(id) }); },
  }), [api, queryClient]);
  useEffect(() => { registry.configure(gateway, capability.drafts, capability.loading); }, [registry, gateway, capability.drafts, capability.loading]);
  useEffect(() => registry.retain(), [registry]);
  return <DraftContext.Provider value={registry}>{children}</DraftContext.Provider>;
}
