// 已连接状态下的应用骨架：顶部状态栏、左侧导航、内容区、全局审批弹层。
// 未连接时重定向到 /connect。

import { Suspense, useEffect } from "react";
import { Navigate, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useConnection } from "../state/ConnectionContext";
import { EventStreamProvider } from "../state/EventStreamProvider";
import { useSettingsQuery } from "../state/useSettings";
import { applyLocale } from "../i18n";
import { DraftProvider } from "../state/DraftProvider";
import { NavigationSidebar } from "./NavigationSidebar";
import { applyAppearance, readUiPreferences } from "../lib/uiPreferences";
import { StatusBar } from "./StatusBar";
import { ApprovalDialog } from "./ApprovalDialog";
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
  const navigate = useNavigate();
  const location = useLocation();
  const settingsLayout = location.pathname === "/settings";
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
        <NavLink className="brand" to="/chat"><img src={`${import.meta.env.BASE_URL}brand/gosli.png`} alt="" /><span>gogosling<span className="brand-dot">.</span></span></NavLink>
        <span className="header-tagline">{t("work.brandLine")}</span>
        <details className="connection-details"><summary><span className="status-dot" />{t("work.connectionStatus")}</summary><div className="connection-popover"><StatusBar /></div></details>
      </header>
      {!settingsLayout ? <NavigationSidebar openSearch={openSearch} modifier={modifier} /> : null}
      <main className="app-main"><Suspense fallback={<Spinner />}><Outlet /></Suspense></main>
      <ApprovalDialog />
    </div>
  );
}
