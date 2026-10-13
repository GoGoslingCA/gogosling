// 只呈现已有本地文件与任务入口；打开菜单不请求网络、不改变权限。
import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/Icon";
import type { TaskPopoverController } from "./taskPopovers";

/** 本地项目资料由宿主已有查询提供，菜单不自行加载或持久化。 */
export interface LocalMenuMaterial { id: string; name: string; selected: boolean }

export function LocalAddMenu({ controller, disabled, label, hasTaskContext, onChooseFiles, projectMaterials = [], onToggleMaterial }: {
  controller: TaskPopoverController;
  disabled: boolean;
  label: string;
  hasTaskContext: boolean;
  onChooseFiles: () => void;
  projectMaterials?: readonly LocalMenuMaterial[];
  onToggleMaterial?: (id: string) => void;
}) {
  const { t } = useTranslation();
  const prefix = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const root = useRef<HTMLDivElement>(null);
  const filesTrigger = useRef<HTMLButtonElement>(null);
  const [filesOpen, setFilesOpen] = useState(false);
  const [position, setPosition] = useState({ side: "above", space: 480 });
  const [filePosition, setFilePosition] = useState({ side: "right", top: 0, height: 340, width: 220 });
  const open = controller.open === "add";
  useEffect(() => { if (!open) setFilesOpen(false); }, [open]);
  useEffect(() => { if (disabled && open) controller.setOpen(null); }, [disabled, open, controller.setOpen]);

  const placeMenu = () => {
    const bounds = trigger.current?.getBoundingClientRect();
    if (!bounds) return;
    const above = Math.max(0, bounds.top - 16);
    const below = Math.max(0, window.innerHeight - bounds.bottom - 16);
    setPosition(above >= below ? { side: "above", space: above } : { side: "below", space: below });
  };
  useEffect(() => {
    if (!open) return;
    window.addEventListener("resize", placeMenu);
    return () => window.removeEventListener("resize", placeMenu);
  }, [open]);
  useLayoutEffect(() => {
    if (!open || !filesOpen || window.innerWidth <= 700) return;
    const bounds = root.current?.querySelector<HTMLElement>('[data-local-menu="root"]')?.getBoundingClientRect();
    if (!bounds) return;
    const right = Math.max(0, window.innerWidth - bounds.right - 22);
    const left = Math.max(0, bounds.left - 22);
    const side = right >= 220 || right >= left ? "right" : "left";
    const top = Math.max(0, 16 - bounds.top);
    setFilePosition({ side, top, width: Math.min(220, side === "right" ? right : left), height: Math.max(0, window.innerHeight - bounds.top - top - 16) });
  }, [open, filesOpen, position]);
  // 窄屏只有显式点击或键盘展开，防止触控与菜单重排造成悬停反复开关。
  const hoverFiles = (expanded: boolean) => { if (window.innerWidth > 700) setFilesOpen(expanded); };

  const focusItem = (menu: "root" | "files", last = false) => {
    queueMicrotask(() => {
      const items = root.current?.querySelectorAll<HTMLButtonElement>(`[data-local-menu="${menu}"] > button:not(:disabled)`);
      if (items?.length) items[last ? items.length - 1 : 0]?.focus();
    });
  };
  const close = (returnFocus = false) => {
    controller.setOpen(null);
    setFilesOpen(false);
    if (returnFocus) trigger.current?.focus();
  };
  const show = (last = false) => {
    if (disabled) return;
    placeMenu();
    controller.setOpen("add");
    setFilesOpen(false);
    focusItem("root", last);
  };
  const showFiles = (focus = true) => {
    setFilesOpen(true);
    if (focus) focusItem("files");
  };
  const openContext = (id: "workspace" | "permission") => {
    // 将焦点交给原入口，再切换单一开关，保留原有详情和 Esc 返回位置。
    trigger.current?.form?.querySelector<HTMLElement>(`[data-context-id="${id}"] > summary`)?.focus();
    setFilesOpen(false);
    controller.setOpen(id);
  };
  const onMenuKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const target = event.target as HTMLElement;
    const menu = target.closest<HTMLElement>("[data-local-menu]");
    if (!menu) return;
    if (event.key === "Escape" || (event.key === "ArrowLeft" && menu.dataset.localMenu === "files")) {
      event.preventDefault();
      event.stopPropagation();
      if (filesOpen) { setFilesOpen(false); filesTrigger.current?.focus(); }
      else close(true);
      return;
    }
    if (event.key === "Tab") { close(true); return; }
    if (event.key === "ArrowRight" && target === filesTrigger.current) {
      event.preventDefault(); showFiles(); return;
    }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const items = [...menu.querySelectorAll<HTMLButtonElement>(":scope > button:not(:disabled)")];
    const index = items.indexOf(target as HTMLButtonElement);
    const next = event.key === "Home" ? 0 : event.key === "End" ? items.length - 1 : (index + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
    items[next]?.focus();
  };

  return <div ref={root} className="composer-add-menu" data-task-popover="add">
    <button ref={trigger} type="button" className="btn ghost sm composer-icon" aria-label={label} title={label} disabled={disabled} aria-haspopup="menu" aria-expanded={open} aria-controls={`${prefix}-menu`}
      onClick={() => { if (open) close(); else show(); }}
      onKeyDown={(event) => {
        if (event.key === "ArrowDown" || event.key === "ArrowUp") { event.preventDefault(); show(event.key === "ArrowUp"); }
        else if (event.key === "Escape" && open) { event.preventDefault(); close(true); }
      }}><Icon name="plus" size={19}/><span className="sr-only">{label}</span></button>
    {open ? <div className="composer-add-panels" data-placement={position.side} style={{ "--composer-menu-space": `${position.space}px` } as CSSProperties} onKeyDown={onMenuKeyDown}>
      <div id={`${prefix}-menu`} className="composer-local-menu" role="menu" aria-label={t("compose.addMenu")} data-local-menu="root">
        <button ref={filesTrigger} type="button" role="menuitem" tabIndex={-1} aria-haspopup="menu" aria-expanded={filesOpen} aria-controls={`${prefix}-files`} onPointerEnter={() => hoverFiles(true)} onClick={() => showFiles()}>
          <Icon name="document" size={16}/><span>{t("compose.addFiles")}</span><Icon name="chevron" size={13}/>
        </button>
        {hasTaskContext ? <>
          <div role="separator" className="composer-menu-separator"/>
          <button type="button" role="menuitem" tabIndex={-1} onFocus={() => setFilesOpen(false)} onPointerEnter={() => hoverFiles(false)} onClick={() => openContext("workspace")}><Icon name="folder" size={16}/><span>{t("phase2.workspace")}</span></button>
          <button type="button" role="menuitem" tabIndex={-1} onFocus={() => setFilesOpen(false)} onPointerEnter={() => hoverFiles(false)} onClick={() => openContext("permission")}><Icon name="shield" size={16}/><span>{t("compose.approvalMode")}</span></button>
        </> : null}
      </div>
      {filesOpen ? <div id={`${prefix}-files`} className="composer-local-menu composer-file-menu" role="menu" aria-label={t("compose.addFiles")} data-local-menu="files" data-side={filePosition.side} style={{ "--composer-submenu-top": `${filePosition.top}px`, "--composer-submenu-height": `${filePosition.height}px`, "--composer-submenu-width": `${filePosition.width}px` } as CSSProperties}>
        <button type="button" role="menuitem" tabIndex={-1} onClick={() => { close(); onChooseFiles(); }}><Icon name="folder" size={16}/><span>{t("compose.localFiles")}</span></button>
        {projectMaterials.length && onToggleMaterial ? <>
          <div role="separator" className="composer-menu-separator"/>
          <span className="composer-menu-caption">{t("compose.projectMaterials")}</span>
          {projectMaterials.map((material) => <button key={material.id} type="button" role="menuitemcheckbox" aria-checked={material.selected} title={material.name} tabIndex={-1} onClick={() => {
            onToggleMaterial(material.id);
            close();
            trigger.current?.form?.querySelector("textarea")?.focus();
          }}><Icon name="document" size={16}/><span>{material.name}</span>{material.selected ? <Icon name="check" size={14}/> : null}</button>)}
        </> : null}
      </div> : null}
    </div> : null}
  </div>;
}
