// 全局审批弹层：以 GET /approvals?status=pending 为真相（页面刷新后仍能看到），
// SSE 的 approval.* 事件只负责让它及时刷新。每次只展示最早的一条。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";
import { useTimeZone } from "../state/useSettings";
import { Badge, Dialog } from "../components/ui";
import { useToast } from "../components/Toast";
import { describeError } from "../lib/errorMessage";
import { formatDateTime } from "../lib/time";
import { prettyJson } from "../lib/json";
import type { ApprovalScope } from "../api/types";

export function ApprovalDialog() {
  const { t, i18n } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const timeZone = useTimeZone();

  const pending = useQuery({
    queryKey: queryKeys.approvals("pending"),
    queryFn: () => api.approvals.list("pending"),
  });

  const decide = useMutation({
    mutationFn: ({ id, approve, scope }: { id: string; approve: boolean; scope: ApprovalScope }) =>
      api.approvals.decide(id, { approve, scope }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.approvalsRoot });
    },
    onError: (err) => {
      toast.show(describeError(err, t), "error");
      // 可能已经被别处处理（过期 / 其他客户端决定）：刷新列表。
      void queryClient.invalidateQueries({ queryKey: queryKeys.approvalsRoot });
    },
  });

  const first = pending.data?.[0];
  const approvalSession = useQuery({ queryKey: queryKeys.session(first?.session_id ?? ""), queryFn: () => api.sessions.get(first?.session_id ?? ""), enabled: !!first?.session_id });
  if (!first) return null;
  const taskReady = approvalSession.isSuccess;
  const taskLabel = taskReady ? approvalSession.data?.title || t("chat.untitled") : approvalSession.isError ? t("approval.taskUnknown") : t("approval.taskLoading");

  const riskTone = first.risk === "destructive" || first.risk === "execute" ? "bad" : first.risk === "network" || first.risk === "write" ? "warn" : "accent";

  return (
    <Dialog
      open
      dismissible={false}
      title={t("approval.title")}
      onClose={() => undefined}
      footer={
        <>
          <button type="button" className="btn danger" disabled={decide.isPending} onClick={() => decide.mutate({ id: first.id, approve: false, scope: "once" })}>
            {t("approval.deny")}
          </button>
          {!first.operation_id ? <button type="button" className="btn" disabled={decide.isPending || !taskReady} onClick={() => decide.mutate({ id: first.id, approve: true, scope: "session" })}>
            {t("approval.approveSession")}
          </button> : null}
          <button type="button" className="btn primary" disabled={decide.isPending || !taskReady} onClick={() => decide.mutate({ id: first.id, approve: true, scope: "once" })}>
            {t("approval.approveOnce")}
          </button>
        </>
      }
    >
      <div className="approval-context"><span>{t("phase2.approvalTask")}</span><strong>{taskLabel}</strong><span className="muted">{t("phase2.approvalHint")}</span></div>
      <p style={{ marginTop: 0 }}>{first.operation_id ? t(`changes.operation_${String(first.details?.operation_kind ?? "replace")}`) : first.summary}</p>
      {first.operation_id ? <div className="change-approval-target"><code>{String(first.details?.path ?? "")}</code><span>{t("approval.fileOnce")}</span></div> : null}
      <div className="row" style={{ marginBottom: 10 }}>
        <Badge tone={riskTone}>{t(`risk.${first.risk}`)}</Badge>
        {!first.operation_id ? <Badge>
          {t("approval.tool")}: {t(`tools.${first.tool_call.name}`, { defaultValue: first.tool_call.name })} <code>{first.tool_call.name}</code>
        </Badge> : null}
        <span className="status-item">
          {t("approval.expires")} {formatDateTime(first.expires_at, { timeZone, locale: i18n.language })}
        </span>
      </div>
      <details className="field">
        <summary>{t("approval.details")}</summary>
        <pre className="approval-details">{prettyJson(first.details && Object.keys(first.details).length > 0 ? first.details : first.tool_call.arguments)}</pre>
      </details>
      {pending.data && pending.data.length > 1 ? <p className="status-item">{t("approval.pendingCount", { n: pending.data.length })}</p> : null}
    </Dialog>
  );
}
