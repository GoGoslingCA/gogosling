// 输入区弹层共享开关；独立使用任务条件栏时保留相同的关闭行为。
import { createContext, useEffect, useState, type RefObject } from "react";

export interface TaskPopoverController {
  open: string | null;
  setOpen: (id: string | null) => void;
}

export const TaskPopoverContext = createContext<TaskPopoverController | null>(null);

/** 开关仅影响当前输入区，不改变嵌套说明或外部对话框。 */
export function useTaskPopoverController(root: RefObject<HTMLElement | null>, scope?: string, enabled = true): TaskPopoverController {
  const [open, setOpen] = useState<string | null>(null);
  useEffect(() => { setOpen(null); }, [scope]);
  useEffect(() => {
    if (!enabled || !open) return;
    const dismiss = (event: Event) => {
      const active = root.current?.querySelector(`[data-task-popover="${open}"]`);
      if (event.target instanceof Node && !active?.contains(event.target)) setOpen(null);
    };
    const blur = () => setOpen(null);
    document.addEventListener("pointerdown", dismiss, true);
    document.addEventListener("focusin", dismiss, true);
    window.addEventListener("blur", blur);
    return () => {
      document.removeEventListener("pointerdown", dismiss, true);
      document.removeEventListener("focusin", dismiss, true);
      window.removeEventListener("blur", blur);
    };
  }, [enabled, open, root]);
  return { open, setOpen };
}
