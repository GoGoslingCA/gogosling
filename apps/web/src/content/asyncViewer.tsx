// 格式模块仍按需下载；轻量加载状态不触发 Suspense 的回退显示延迟。
import { useEffect, useState, type ComponentType } from "react";
import { Spinner } from "../components/ui";
import type { ViewerProps } from "./types";

/** 缓存的是格式代码，不是文件正文；卸载后的模块回调不会更新旧页面。 */
export function asyncViewer(loader: () => Promise<{ default: ComponentType<ViewerProps> }>): ComponentType<ViewerProps> {
  let resolved: ComponentType<ViewerProps> | undefined;
  let pending: Promise<ComponentType<ViewerProps>> | undefined;
  const load = () => pending ??= loader().then(module => {
    resolved = module.default;
    return module.default;
  }).catch((error: unknown) => { pending = undefined; throw error; });
  return function FormatViewer(props: ViewerProps) {
    const [ready, setReady] = useState(() => !!resolved);
    const [failure, setFailure] = useState<{ error: unknown } | null>(null);
    useEffect(() => {
      let active = true;
      if (!resolved) void load().then(() => { if (active) setReady(true); }, (error: unknown) => { if (active) setFailure({ error }); });
      else setReady(true);
      return () => { active = false; };
    }, []);
    if (failure) throw failure.error;
    const Viewer = resolved;
    return ready && Viewer ? <Viewer {...props} /> : <div className="preview-loading"><Spinner /></div>;
  };
}
