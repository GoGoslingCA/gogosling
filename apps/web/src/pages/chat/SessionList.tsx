import { useTranslation } from "react-i18next";
import { ErrorBanner, Spinner } from "../../components/ui";
import { formatRelative } from "../../lib/time";
import type { Session } from "../../api/types";

export function SessionList({
  sessions,
  loading,
  error,
  activeId,
  onSelect,
  onRename,
  agentName,
  timeZone,
}: {
  sessions: Session[];
  loading: boolean;
  error: unknown;
  activeId: string | null;
  onSelect: (id: string) => void;
  onRename?: (session: Session) => void;
  agentName: (id: string) => string;
  timeZone: string | undefined;
}) {
  const { t } = useTranslation();
  const labels = {
    justNow: t("time.justNow"),
    minutes: (n: number) => t("time.minutesAgo", { n }),
    hours: (n: number) => t("time.hoursAgo", { n }),
    days: (n: number) => t("time.daysAgo", { n }),
  };
  // 最近有消息的排前面；无消息会话按创建时间排序，重命名不会改变顺序。
  const sorted = [...sessions].sort((a, b) => {
    const ta = Date.parse(a.last_message_at ?? a.created_at);
    const tb = Date.parse(b.last_message_at ?? b.created_at);
    return tb - ta;
  });
  return (
    <div className="session-list">
      <div style={{ padding: "0 4px" }}>
        <ErrorBanner error={error} />
      </div>
      {loading ? <Spinner /> : null}
      {!loading && sorted.length === 0 ? <div className="status-item" style={{ padding: 8 }}>{t("chat.noSessions")}</div> : null}
      {sorted.map((s) => (
        <button type="button" key={s.id} aria-current={s.id === activeId ? "page" : undefined} className={s.id === activeId ? "session-item active" : "session-item"} onClick={() => onSelect(s.id)} onContextMenu={(event) => { if (onRename) { event.preventDefault(); onRename(s); } }}>
          <span className="title">{s.title || t("chat.untitled")}</span>
          <span className="meta">
            {agentName(s.agent_id)} · {t(`origin.${s.origin}`)} · {formatRelative(s.last_message_at ?? s.updated_at, labels, { timeZone })}
          </span>
        </button>
      ))}
    </div>
  );
}
