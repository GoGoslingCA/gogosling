// 顶部状态：守护进程版本（/api/v1/version 轮询）、工作进程状态（worker.status 事件优先）、
// 事件流连接状态、待审批数量、断开按钮。

import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi, useConnection } from "../state/ConnectionContext";
import { useStreamStatus } from "../state/EventStreamProvider";
import { queryKeys } from "../state/queryKeys";
import type { WorkerState } from "../api/types";

function workerTone(state: WorkerState | undefined): "ok" | "warn" | "bad" {
  if (state === "ready") return "ok";
  if (state === "starting") return "warn";
  return "bad";
}

export function StatusBar() {
  const { t } = useTranslation();
  const api = useApi();
  const { disconnect } = useConnection();
  const stream = useStreamStatus();

  const version = useQuery({
    queryKey: queryKeys.version,
    queryFn: () => api.system.version(),
    refetchInterval: 30_000,
    staleTime: 10_000,
  });
  const pending = useQuery({
    queryKey: queryKeys.approvals("pending"),
    queryFn: () => api.approvals.list("pending"),
    refetchInterval: 60_000,
  });

  const worker = stream.worker ?? version.data?.worker ?? null;
  const workerState = worker?.status;
  const daemonTone: "ok" | "bad" = version.isError ? "bad" : "ok";

  const streamLabel = (() => {
    switch (stream.status) {
      case "open":
        return t("status.streamOpen");
      case "connecting":
        return t("status.streamConnecting");
      case "reconnecting":
        return t("status.streamReconnecting", { n: stream.attempt });
      case "unauthorized":
        return t("status.streamUnauthorized");
      default:
        return t("status.streamClosed");
    }
  })();
  const streamTone: "ok" | "warn" | "bad" = stream.status === "open" ? "ok" : stream.status === "connecting" || stream.status === "reconnecting" ? "warn" : "bad";

  return (
    <>
      <span className="status-item" title={version.data?.commit ?? ""}>
        <span className={`status-dot ${daemonTone}`} />
        {t("status.daemon")} {version.data?.version ?? t("common.unknown")}
      </span>
      <span className="status-item" title={worker?.python ? `python ${worker.python}` : ""}>
        <span className={`status-dot ${workerTone(workerState)}`} />
        {t("status.worker")} {workerState ? t(`status.worker_${workerState}`) : t("common.unknown")}
        {worker?.version ? ` ${worker.version}` : ""}
      </span>
      <span className="status-item">
        <span className={`status-dot ${streamTone}`} />
        {streamLabel}
      </span>
      {pending.data && pending.data.length > 0 ? (
        <span className="badge warn">{t("status.pendingApprovals", { n: pending.data.length })}</span>
      ) : null}
      <button type="button" className="btn ghost sm" onClick={() => disconnect("manual")}>
        {t("status.disconnect")}
      </button>
    </>
  );
}
