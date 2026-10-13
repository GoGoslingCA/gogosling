// 项目表单共享创建与编辑交互；创建入口按需加载助手，仍使用既有工作台权限和接口。
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { Agent, Project } from "../api/types";
import { describeError } from "../lib/errorMessage";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";
import { useWorkbenchCapabilities } from "../state/useWorkbenchCapabilities";
import { useToast } from "./Toast";
import { Dialog, ErrorBanner, Spinner } from "./ui";

interface ProjectFormProps {
  project: Project | null;
  agents: Pick<Agent, "id" | "name" | "enabled">[];
  busy: boolean;
  loadingAgents?: boolean;
  agentError?: unknown;
  unavailable?: boolean;
  onRetryAgents?: () => void;
  onClose: () => void;
  onSubmit: (agentId: string, name: string, description: string) => void;
}

/** 创建与编辑共享表单；已有项目的助手归属始终只读。 */
export function ProjectForm({ project, agents, busy, loadingAgents = false, agentError, unavailable = false, onRetryAgents, onClose, onSubmit }: ProjectFormProps) {
  const { t } = useTranslation();
  const [name, setName] = useState(project?.name ?? "");
  const [description, setDescription] = useState(project?.description ?? "");
  const [selectedAgent, setSelectedAgent] = useState("");
  const enabledAgents = agents.filter((agent) => agent.enabled);
  // 延迟加载完成后自动选中可用助手，但不覆盖仍有效的手动选择。
  const agentId = project?.agent_id ?? (enabledAgents.some((agent) => agent.id === selectedAgent) ? selectedAgent : enabledAgents[0]?.id ?? "");
  const options = agents.filter((agent) => agent.enabled || agent.id === project?.agent_id);
  const cannotCreate = !project && (loadingAgents || !!agentError);
  return <Dialog open className="project-form-dialog" title={t(project ? "projects.edit" : "projects.create")} dismissible={!busy} onClose={onClose} footer={<>
    <button type="button" className="btn" disabled={busy} onClick={onClose}>{t("common.cancel")}</button>
    <button type="button" className="btn primary" disabled={!name.trim() || !agentId || busy || unavailable || cannotCreate} onClick={() => onSubmit(agentId, name.trim(), description.trim())}>{t("common.save")}</button>
  </>}>
    <label className="field">{t("projects.name")}<input autoFocus className="input" value={name} maxLength={120} disabled={busy} onChange={(event) => setName(event.target.value)} /></label>
    <label className="field">{t("chat.agent")}<select className="select" value={agentId} disabled={!!project || busy || loadingAgents || !!agentError || unavailable} onChange={(event) => setSelectedAgent(event.target.value)}>
      {!agentId ? <option value="">{t(loadingAgents ? "common.loading" : "projects.noEnabledAgent")}</option> : null}
      {project && !options.some((agent) => agent.id === project.agent_id) ? <option value={project.agent_id}>{project.agent_id}</option> : null}
      {options.map((agent) => <option value={agent.id} key={agent.id}>{agent.name}</option>)}
    </select></label>
    {unavailable ? <p className="muted">{t("projects.unavailable")}</p> : loadingAgents ? <Spinner /> : null}
    <ErrorBanner error={agentError} />
    {!!agentError && onRetryAgents ? <button type="button" className="btn" disabled={busy || loadingAgents} onClick={onRetryAgents}>{t("projects.retryAgents")}</button> : null}
    <p className="muted">{t("projects.agentHint")}</p>
    <details className="project-form-description" open={project ? true : undefined}>
      <summary>{t("projects.description")}</summary>
      <label className="field"><span className="sr-only">{t("projects.description")}</span><textarea className="textarea" value={description} maxLength={2000} disabled={busy} onChange={(event) => setDescription(event.target.value)} /></label>
    </details>
  </Dialog>;
}

/** 侧栏可懒加载的创建入口；成功后更新共享缓存，再交回页面决定导航位置。 */
export function CreateProjectDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (project: Project) => void }) {
  const api = useApi();
  const { t } = useTranslation();
  const caps = useWorkbenchCapabilities();
  const client = useQueryClient();
  const toast = useToast();
  const agents = useQuery({ queryKey: queryKeys.agents, queryFn: () => api.agents.list(), enabled: caps.projects, retry: false });
  const create = useMutation({
    mutationFn: ({ agentId, name, description }: { agentId: string; name: string; description: string }) => api.workbench.createProject({ agent_id: agentId, name, description }),
    onSuccess: (project) => {
      client.setQueryData(queryKeys.project(project.id), project);
      void client.invalidateQueries({ queryKey: queryKeys.projects });
      onCreated(project);
    },
    onError: (error) => toast.show(describeError(error, t), "error"),
  });
  return <ProjectForm project={null} agents={agents.data ?? []} busy={create.isPending} loadingAgents={caps.loading || (caps.projects && agents.isFetching)} agentError={caps.error ?? agents.error} unavailable={!caps.loading && !caps.projects} onRetryAgents={caps.projects ? () => { void agents.refetch(); } : undefined} onClose={onClose} onSubmit={(agentId, name, description) => create.mutate({ agentId, name, description })} />;
}
