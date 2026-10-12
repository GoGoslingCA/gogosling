// 旧任务可原位加入或移出项目，不改变会话ID和历史消息。
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../../state/ConnectionContext";
import { useWorkbenchCapabilities } from "../../state/useWorkbenchCapabilities";
import { queryKeys } from "../../state/queryKeys";
import { useToast } from "../../components/Toast";
import { describeError } from "../../lib/errorMessage";
import type { Session } from "../../api/types";

export function SessionProjectControl({ session, running }: { session: Session; running: boolean }) {
  const { t } = useTranslation(); const api = useApi(); const caps = useWorkbenchCapabilities(); const client = useQueryClient(); const toast = useToast();
  const projects = useQuery({ queryKey: [...queryKeys.projects, "agent", session.agent_id], queryFn: () => api.workbench.listProjects({ agent_id: session.agent_id, limit: 200 }), enabled: caps.projects });
  const move = useMutation({
    mutationFn: (projectId: string) => api.workbench.setSessionProject(session.id, { project_id: projectId || null, expected_project_id: session.project_id ?? null }),
    onSuccess: (saved) => { client.setQueryData(queryKeys.session(saved.id), saved); void client.invalidateQueries({ queryKey: queryKeys.sessionsRoot }); void client.invalidateQueries({ queryKey: queryKeys.artifacts }); },
    onError: (error) => { toast.show(describeError(error, t), "error"); void client.invalidateQueries({ queryKey: queryKeys.session(session.id) }); },
  });
  if (!caps.projects) return null;
  return <label className="session-project-control">{t("projects.taskBelongsTo")}<select className="select" aria-label={t("projects.taskBelongsTo")} value={session.project_id ?? ""} disabled={running || move.isPending || projects.isPending || !!projects.error} onChange={(event) => move.mutate(event.target.value)}><option value="">{t("projects.unassigned")}</option>{projects.data?.map((project) => <option key={project.id} value={project.id} disabled={project.status === "archived" && project.id !== session.project_id}>{project.name}{project.status === "archived" ? ` (${t("projects.archived")})` : ""}</option>)}</select></label>;
}
