// 已连接状态下的应用骨架：顶部状态栏、左侧导航、内容区、全局审批弹层。
// 未连接时重定向到 /connect。

import { Suspense, useEffect, useRef, useState, type CSSProperties } from "react";
import { Navigate, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useConnection } from "../state/ConnectionContext";
import { EventStreamProvider, useStreamStatus } from "../state/EventStreamProvider";
import { useSettingsQuery } from "../state/useSettings";
import { applyLocale } from "../i18n";
import { DraftProvider } from "../state/DraftProvider";
import { NavigationSidebar } from "./NavigationSidebar";
import { applyAppearance, readUiPreferences, writeUiPreferences } from "../lib/uiPreferences";
import { StatusBar } from "./StatusBar";
import { ApprovalDialog } from "./ApprovalDialog";
import { useWorkspaceTabs } from "../state/WorkspaceTabsProvider";
import { WorkspacePane } from "../workspace/WorkspacePane";
import "../workspace/workspace.css";
import { Spinner } from "../components/ui";

export function AppShell() {
  const { connection } = useConnection();
  if (!connection) return <Navigate to="/connect" replace />;
  return (
    <EventStreamProvider>
      <DraftProvider><Shell /></DraftProvider>
    </EventStreamProvider>
  );
}

function Shell() {
  const { t } = useTranslation();
  const settings = useSettingsQuery();
  const workspace = useWorkspaceTabs();
  const [workspaceWidth, setWorkspaceWidth] = useState(() => readUiPreferences().artifactWidth);
  const stream = useStreamStatus();
  const [connectionOpen, setConnectionOpen] = useState(false);
  const connectionDetails = useRef<HTMLDetailsElement>(null);
  const navigate = useNavigate();
  const location = useLocation();
  const settingsLayout = location.pathname === "/settings";
  const streamLabel = stream.status === "open" ? t("status.streamOpen") : stream.status === "connecting" ? t("status.streamConnecting") : stream.status === "reconnecting" ? t("status.streamReconnecting", { n: stream.attempt }) : stream.status === "unauthorized" ? t("status.streamUnauthorized") : t("status.streamClosed");
  const streamTone = stream.status === "open" ? "ok" : stream.status === "connecting" || stream.status === "reconnecting" ? "warn" : "bad";
  useEffect(() => { setConnectionOpen(false); }, [location.pathname]);
  useEffect(() => {
    if (!connectionOpen) return;
    const outside = (event: Event) => { if (event.target instanceof Node && !connectionDetails.current?.contains(event.target)) setConnectionOpen(false); };
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      event.preventDefault(); setConnectionOpen(false);
      connectionDetails.current?.querySelector("summary")?.focus();
    };
    const blur = () => setConnectionOpen(false);
    window.addEventListener("pointerdown", outside); window.addEventListener("focusin", outside); window.addEventListener("keydown", escape); window.addEventListener("blur", blur);
    return () => { window.removeEventListener("pointerdown", outside); window.removeEventListener("focusin", outside); window.removeEventListener("keydown", escape); window.removeEventListener("blur", blur); };
  }, [connectionOpen]);
  const openSearch = () => {
    const params = new URLSearchParams(location.pathname.startsWith("/chat") ? location.search : "");
    params.set("history", "1");
    void navigate(`${location.pathname.startsWith("/chat") ? location.pathname : "/chat"}?${params}`);
  };
  useEffect(() => { applyAppearance(readUiPreferences().appearance); }, []);
  useEffect(() => {
    const handle = (event: KeyboardEvent) => {
      if (!(event.metaKey || event.ctrlKey) || event.altKey || event.shiftKey || event.isComposing || document.querySelector("dialog[open]")) return;
      if (event.key.toLowerCase() === "k") { event.preventDefault(); openSearch(); }
      if (event.key.toLowerCase() === "n") { event.preventDefault(); void navigate("/chat"); }
    };
    window.addEventListener("keydown", handle);
    return () => window.removeEventListener("keydown", handle);
  });

  // 服务端设置里的语言是用户偏好的来源；连上后同步到界面。
  useEffect(() => {
    const locale = settings.data?.locale;
    if (locale) void applyLocale(locale);
  }, [settings.data?.locale]);

  useEffect(() => {
    document.title = t("app.title");
  }, [t]);

  const modifier = navigator.platform.toLowerCase().includes("mac") ? "⌘" : "Ctrl";


  return (
    <div className={`app${settingsLayout ? " settings-layout" : ""}`}>
      <header className="app-header">
        <NavLink className="brand" to="/chat"><span>gogosling<span className="brand-dot">.</span></span></NavLink>
        <details ref={connectionDetails} className="connection-details" open={connectionOpen} onToggle={event => setConnectionOpen(event.currentTarget.open)}><summary aria-label={t("work.connectionStatus")} aria-description={streamLabel} title={`${t("work.connectionStatus")}: ${streamLabel}`}><span className={`status-dot ${streamTone}`} /><span className="sr-only">{t("work.connectionStatus")}</span></summary>{connectionOpen ? <div className="connection-popover"><StatusBar /></div> : null}</details>
      </header>
      {!settingsLayout ? <NavigationSidebar openSearch={openSearch} modifier={modifier} /> : null}
      <main className="app-main"><div className={`app-content ${workspace?.visible && !settingsLayout ? "workspace-visible" : ""}`} style={{ "--workspace-width": `${workspaceWidth}px` } as CSSProperties}><div className="app-route"><Suspense fallback={<Spinner />}><Outlet /></Suspense></div>{workspace?.visible && !settingsLayout ? <WorkspacePane width={workspaceWidth} onResize={setWorkspaceWidth} onResizeEnd={width => writeUiPreferences({ artifactWidth: width })} /> : null}</div></main>
      <ApprovalDialog />
    </div>
  );
}
