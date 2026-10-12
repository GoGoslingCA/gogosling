// 本次任务条件集中展示；引用附件不等于改变内核文件访问权限。
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import type { Agent, Attachment, Message, Usage } from "../../api/types";
import { Icon } from "../../components/Icon";
import "../../components/compact-workbench.css";

interface ContextItemProps { id: string; open: string | null; setOpen: (id: string | null) => void; label: string; title?: string; children: ReactNode; trigger: ReactNode; prefix: string }
function ContextItem({id,open,setOpen,label,title,children,trigger,prefix}:ContextItemProps) {
  return <details className={`context-${id}-menu`} data-context-id={id} open={open===id}>
    <summary role="button" aria-label={label} title={title ?? label} aria-expanded={open===id} aria-controls={`${prefix}-${id}`} onClick={event=>{event.preventDefault();setOpen(open===id?null:id);}}>{trigger}</summary>
    <div id={`${prefix}-${id}`} className="context-popover">{children}</div>
  </details>;
}
export function TaskContextBar({ agent, messages, onAddMaterial, disabled, usage }: { agent: Agent; messages: Message[]; usage?: Usage | null; onAddMaterial: (attachment: Attachment) => void; disabled: boolean }) {
  const { t } = useTranslation(); const root=useRef<HTMLDivElement>(null);const prefix=useId();
  const [open,setOpen]=useState<string | null>(null);
  // 单一状态控制顶层弹层；不改嵌套说明、审批对话框或其他组件的开关。
  useEffect(()=>{setOpen(null);},[agent.id]);
  useEffect(()=>{
    if(!open)return;
    const dismiss=(event:Event)=>{const active=root.current?.querySelector(`[data-context-id="${open}"]`);if(event.target instanceof Node && !active?.contains(event.target))setOpen(null);};
    const blur=()=>setOpen(null);
    document.addEventListener("pointerdown",dismiss,true);document.addEventListener("focusin",dismiss,true);window.addEventListener("blur",blur);
    return()=>{document.removeEventListener("pointerdown",dismiss,true);document.removeEventListener("focusin",dismiss,true);window.removeEventListener("blur",blur);};
  },[open]);
  const materials = [...new Map(messages.flatMap((message) => message.parts.flatMap((part) => part.attachment ? [[part.attachment.path, part.attachment] as const] : []))).values()];
  const folder = agent.workspace_dir.replace(/[\\/]+$/, "").split(/[\\/]/).at(-1) ?? "";
  const opaqueFolder = folder === agent.id || /^[0-9A-HJKMNP-TV-Z]{26}$/i.test(folder) || /^[a-f\d]{16,}$/i.test(folder) || /^[a-f\d]{8}(?:-[a-f\d]{4}){3}-[a-f\d]{12}$/i.test(folder);
  const directory = !folder || opaqueFolder ? t("phase2.workspace") : folder;
  const metrics = [{ label: "compact.inputTokens", value: usage?.input_tokens }, { label: "compact.outputTokens", value: usage?.output_tokens }, ...(usage?.cache_read_tokens !== undefined ? [{ label: "compact.cachedTokens", value: usage.cache_read_tokens }] : [])];
  const maximum = Math.max(1, ...metrics.map((metric) => metric.value ?? 0));
  const shared={open,setOpen,prefix};
  return <div ref={root} className="task-context compact-context" aria-label={t("phase2.taskContext")} onKeyDown={event=>{
    if(event.key!=="Escape"||!open||event.defaultPrevented)return;
    event.preventDefault();event.stopPropagation();const trigger=root.current?.querySelector<HTMLElement>(`[data-context-id="${open}"] > summary`);setOpen(null);trigger?.focus();
  }}>
    <ContextItem {...shared} id="workspace" label={t("phase2.workspace")} title={agent.workspace_dir} trigger={<><Icon name="folder" size={14}/><span className="context-directory">{directory}</span></>}><strong>{t("phase2.workspace")}</strong><code>{agent.workspace_dir}</code><p className="muted">{t("phase2.workspaceHint")}</p></ContextItem>
    <ContextItem {...shared} id="permission" label={t(`agents.autonomy_${agent.autonomy}`)} trigger={<><Icon name="shield" size={14}/>{t(`agents.autonomy_${agent.autonomy}`)}<Icon name="chevron" size={11}/></>}><strong>{t("phase2.permissionForTask")}</strong><p>{t(`agents.autonomyHint_${agent.autonomy}`)}</p><Link to="/agents" onClick={()=>setOpen(null)}>{t("preferences.reviewPermissions")}</Link></ContextItem>
    {usage !== undefined || materials.length ? <ContextItem {...shared} id="more" label={materials.length ? `${t("usage.title")} · ${t("phase2.taskMaterials",{n:materials.length})}` : t("usage.title")} trigger={<Icon name="more" size={16}/>}>
      {usage !== undefined ? <><strong>{t("usage.round")}</strong>{usage && (usage.input_tokens !== undefined || usage.output_tokens !== undefined) ? <><dl className="usage-metrics">{metrics.map(metric=><div key={metric.label}><dt>{t(metric.label)}</dt><dd><span className="usage-bar" aria-hidden="true" style={{width:`${(metric.value ?? 0)/maximum*100}%`}}/><strong>{metric.value===undefined?t("common.unknown"):metric.value.toLocaleString()}</strong></dd></div>)}</dl><small>{t("compact.tokenUnit")}</small></> : <p>{t("usage.unreported")}</p>}<details className="compact-explainer"><summary><Icon name="chevron" size={12}/>{t("compact.usageDetails")}</summary><p className="muted">{t("usage.costHint")}</p></details></> : null}
      {materials.length ? <><p><strong>{t("phase2.taskMaterials",{n:materials.length})}</strong></p>{materials.map(attachment=><button type="button" disabled={disabled} className="context-material" key={attachment.path} onClick={()=>{onAddMaterial(attachment);setOpen(null);}}><span>{attachment.name}</span><Icon name="plus" size={14}/></button>)}</> : null}
    </ContextItem> : null}
    <ContextItem {...shared} id="model" label={`${t("phase2.modelForTask")}: ${agent.model.model}`} title={agent.name} trigger={<><Icon name="spark" size={14}/><span className="context-model">{agent.model.model}</span><Icon name="chevron" size={11}/></>}><strong>{agent.name}</strong><p>{agent.model.model}</p><p className="muted">{t("phase2.agentModelHint")}</p><Link to="/agents" onClick={()=>setOpen(null)}>{t("preferences.manageAgents")}</Link></ContextItem>
  </div>;
}
