// 设置：页内导航只组织已有功能；语言/时区由内核保存，外观与面板宽度是设备 UI 偏好。
import { lazy, Suspense, useEffect, useRef, useState, type FormEvent, type KeyboardEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";
import { useSettingsQuery, useTimeZone } from "../state/useSettings";
import { ErrorBanner, Spinner } from "../components/ui";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { describeError } from "../lib/errorMessage";
import { applyLocale, normalizeLocale, SUPPORTED_LOCALES } from "../i18n";
import { formatDateTime, resolveTimeZone } from "../lib/time";
import { applyAppearance, clampArtifactWidth, readUiPreferences, writeUiPreferences, type Appearance } from "../lib/uiPreferences";
import type { Locale, Settings } from "../api/types";
import "./settings.css";
import { ResourceSettings } from "./ResourceSettings";
import { settingsHref, settingsReturnTarget, settingsSection, type SettingsSection } from "../lib/settingsNavigation";

// 管理模块仅在选择时载入；侧栏不会同时启动模型、扩展和通道查询。
const managementPages = {
  agents: lazy(() => import("./AgentsPage").then(page => ({ default: page.AgentsPage }))),
  models: lazy(() => import("./ProvidersPage").then(page => ({ default: page.ProvidersPage }))),
  extensions: lazy(() => import("./ExtensionsPage").then(page => ({ default: page.ExtensionsPage }))),
  channels: lazy(() => import("./ChannelsPage").then(page => ({ default: page.ChannelsPage }))),
  audit: lazy(() => import("./AuditPage").then(page => ({ default: page.AuditPage }))),
};

const COMMON_TIMEZONES = ["Asia/Shanghai", "Asia/Hong_Kong", "Asia/Taipei", "Asia/Singapore", "Asia/Tokyo", "Asia/Seoul", "Europe/London", "Europe/Berlin", "Europe/Paris", "America/New_York", "America/Chicago", "America/Los_Angeles", "Australia/Sydney", "UTC"];
const CAPABILITY_LABELS = [
  ["workbench.projects", "settingsLayout.capabilityProjects"],
  ["workbench.materials", "settingsLayout.capabilityMaterials"],
  ["workbench.drafts", "settingsLayout.capabilityDrafts"],
  ["workbench.artifacts", "settingsLayout.capabilityArtifacts"],
  ["workbench.preview", "settingsLayout.capabilityPreview"],
  ["workbench.preview_layout", "settingsLayout.capabilityLayout"],
  ["workbench.changesets", "settingsLayout.capabilityChanges"],
  ["prompt.enhance", "settingsLayout.capabilityEnhance"],
  ["runtime.resources", "settingsLayout.capabilityResources"],
] as const;
type SettingsGroup = "preferences" | "connections" | "access";
interface SectionDefinition { id: SettingsSection; title: string; icon: Parameters<typeof Icon>[0]["name"]; group: SettingsGroup; terms: string[] }

/** 搜索、键盘导航与设置保存各自独立，切换分组不会丢弃未提交的表单。 */
export function SettingsPage() {
  const { t, i18n } = useTranslation(); const api = useApi(); const queryClient = useQueryClient(); const toast = useToast();
  const [params, setParams] = useSearchParams();
  const location = useLocation();
  const active = settingsSection(params.toString());
  const setActive = (section: SettingsSection) => { const next = new URLSearchParams(params); next.set("section", section); setParams(next, { state: location.state }); };
  const returnTo = settingsReturnTarget(location.state, readUiPreferences().lastSessionId);
  const settings = useSettingsQuery();
  const version=useQuery({queryKey:queryKeys.version,queryFn:()=>api.system.version(),staleTime:60_000,retry:false});
  const [search, setSearch] = useState("");
  const [narrow, setNarrow] = useState(() => window.matchMedia?.("(max-width: 980px)").matches ?? false);
  const [systemDark, setSystemDark] = useState(() => window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false);
  const [appearance, setAppearance] = useState<Appearance>(() => readUiPreferences().appearance);
  const [artifactWidth, setArtifactWidth] = useState(() => readUiPreferences().artifactWidth);
  const [timezone, setTimezone] = useState("");
  const [locale, setLocale] = useState<Locale>("zh");
  const [tzError, setTzError] = useState<string | null>(null);
  const formEdited = useRef(false); const saveLock = useRef(false);
  const manualAppearance = useRef<"light" | "dark">(appearance === "system" ? systemDark ? "dark" : "light" : appearance);
  const browserZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const timeZone = useTimeZone();
  const desktop = Boolean(window.gogosling?.getConnection);
  const workerStatus = version.data?.worker?.status;
  const workerStatusRow = <SettingsRow label={t("settingsLayout.workerStatus")} description={[version.data?.worker?.version, version.data?.worker?.python].filter(Boolean).join(" · ") || undefined}><span className="settings-readonly" role={workerStatus === "ready" ? undefined : "status"}>{t(workerStatus === "ready" || workerStatus === "starting" || workerStatus === "unavailable" ? `status.worker_${workerStatus}` : "common.unknown")}</span></SettingsRow>;

  useEffect(() => {
    const media = window.matchMedia?.("(max-width: 980px)");
    const theme = window.matchMedia?.("(prefers-color-scheme: dark)");
    const resize = () => setNarrow(media?.matches ?? false);
    const recolor = () => setSystemDark(theme?.matches ?? false);
    media?.addEventListener("change", resize); theme?.addEventListener("change", recolor);
    return () => { media?.removeEventListener("change", resize); theme?.removeEventListener("change", recolor); };
  }, []);
  useEffect(() => {
    if (!settings.data || formEdited.current) return;
    setTimezone(settings.data.timezone ?? "");
    setLocale(settings.data.locale ?? normalizeLocale(i18n.language));
  }, [settings.data, i18n.language]);

  const save = useMutation({
    mutationFn: (input: Settings) => api.settings.update(input),
    onSuccess: async (saved) => {
      formEdited.current = false;
      setTimezone(saved.timezone ?? ""); setLocale(saved.locale ?? locale);
      queryClient.setQueryData(queryKeys.settings, saved);
      if (saved.locale) await applyLocale(saved.locale);
      toast.show(t("settings.saved", { lng: saved.locale }), "ok");
    },
    onError: (error) => toast.show(describeError(error, t), "error"),
    onSettled: () => { saveLock.current = false; },
  });
  const dirty = !!settings.data && (timezone.trim() !== (settings.data.timezone ?? "") || locale !== (settings.data.locale ?? normalizeLocale(i18n.language)));
  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    if (!settings.data || saveLock.current || !dirty) return;
    const tz = timezone.trim();
    if (tz && !resolveTimeZone(tz)) { setTzError(t("settings.timezoneInvalid")); return; }
    setTzError(null); saveLock.current = true; save.mutate({ timezone: tz, locale });
  };
  const changeAppearance = (next: Appearance) => {
    if (next !== "system") manualAppearance.current = next;
    setAppearance(next); applyAppearance(next); writeUiPreferences({ appearance: next });
  };
  const sections: SectionDefinition[] = [
    { id: "general", title: t("settingsLayout.general"), icon: "settings", group: "preferences", terms: [t("settings.locale"), t("settings.timezone"), t("settingsLayout.windows"), t("settingsLayout.closeWindow"), t("settingsLayout.startup"), t("settingsLayout.service"), t("settingsLayout.appVersion")] },
    { id: "appearance", title: t("preferences.appearance"), icon: "spark", group: "preferences", terms: [t("settingsLayout.followSystem"), t("settingsLayout.theme"), t("preferences.appearance_light"), t("preferences.appearance_dark"), t("settingsLayout.panelWidth")] },
    { id: "shortcuts", title: t("settingsLayout.shortcuts"), icon: "code", group: "preferences", terms: [t("preferences.shortcut_search"), t("preferences.shortcut_newTask"), t("settingsLayout.shortcutSave"), t("preferences.shortcut_resize")] },
    { id: "agents", title: t("nav.agents"), icon: "spark", group: "connections", terms: [t("preferences.manageAgents"), t("settingsLayout.agents"), t("settingsLayout.permissions")] },
    { id: "models", title: t("settingsLayout.models"), icon: "model", group: "connections", terms: [t("preferences.manageModels"), t("nav.providers")] },
    { id: "extensions", title: t("settingsLayout.extensionsTitle"), icon: "code", group: "connections", terms: [t("nav.extensions"), t("settingsLayout.extensionsSearch")] },
    { id: "channels", title: t("settingsLayout.channelsTitle"), icon: "chat", group: "connections", terms: [t("nav.channels"), t("settingsLayout.channelsSearch")] },
    { id: "files", title: t("preferences.fileAccess"), icon: "shield", group: "access", terms: [t("preferences.workspaces"), t("preferences.reviewPermissions")] },
    { id: "audit", title: t("settingsLayout.auditTitle"), icon: "clock", group: "access", terms: [t("nav.audit"), t("settingsLayout.auditSearch")] },
  ];
  if(version.data?.capabilities?.["runtime.resources"]===1)sections.splice(3,0,{id:"resources",title:t("resources.title"),icon:"panel",group:"connections",terms:[t("resources.parallel"),t("resources.queued"),t("resources.light")]});
  const query = search.trim().toLocaleLowerCase();
  const filtered = sections.filter((section) => !query || [section.title, ...section.terms].some((value) => value.toLocaleLowerCase().includes(query)));
  const selected = filtered.find((section) => section.id === active) ?? filtered[0];
  const agents = useQuery({ queryKey: queryKeys.agents, queryFn: () => api.agents.list(), enabled: selected?.id === "files" });
  const ManagementPage = selected && selected.id in managementPages ? managementPages[selected.id as keyof typeof managementPages] : null;
  const navigateSections = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key) || !selected) return;
    event.preventDefault();
    const index = filtered.findIndex((section) => section.id === selected.id);
    const next = event.key === "Home" ? filtered[0]! : event.key === "End" ? filtered.at(-1)! : filtered[(index + (event.key === "ArrowDown" || event.key === "ArrowRight" ? 1 : filtered.length - 1)) % filtered.length]!;
    setActive(next.id); document.getElementById(`settings-nav-${next.id}`)?.focus();
  };
  return <div className="settings-page">
    <aside className="settings-sidebar" aria-label={t("settingsLayout.navigation")}>
      <Link className="settings-back" to={returnTo}><Icon name="chevron" size={14} style={{ transform: "rotate(180deg)" }} />{t("settingsLayout.back")}</Link>
      <div className="settings-search"><Icon name="search" size={15} /><input type="search" value={search} placeholder={t("settingsLayout.search")} aria-label={t("settingsLayout.search")} aria-describedby="settings-search-results" onChange={(event) => setSearch(event.target.value)} onKeyDown={(event) => { if (event.key === "Escape") setSearch(""); }} />{search ? <button type="button" onClick={() => setSearch("")} aria-label={t("settingsLayout.clearSearch")}><Icon name="close" size={13} /></button> : null}</div>
      <h1 className="settings-title">{t("settings.title")}</h1>
      <span id="settings-search-results" className="sr-only" role="status">{t("settingsLayout.resultCount", { n: filtered.length })}</span>
      <div className="settings-nav" role="tablist" aria-label={t("settingsLayout.navigation")} aria-orientation={narrow ? "horizontal" : "vertical"} onKeyDown={navigateSections}>
        {(["preferences", "connections", "access"] as const).map((group) => {
          const items = filtered.filter((section) => section.group === group);
          return items.length ? <div className="settings-nav-group" key={group}><p>{t(`settingsLayout.group_${group}`)}</p>{items.map((section) => <button type="button" role="tab" id={`settings-nav-${section.id}`} aria-controls={`settings-panel-${section.id}`} aria-selected={selected?.id === section.id} tabIndex={selected?.id === section.id ? 0 : -1} key={section.id} onClick={() => setActive(section.id)}><Icon name={section.icon} size={17} /><span>{section.title}</span></button>)}</div> : null;
        })}
      </div>
    </aside>
    <div className="settings-main">
      {!selected ? <div className="settings-empty"><Icon name="search" size={26} /><h2>{t("settingsLayout.noResults")}</h2><button type="button" className="btn" onClick={() => setSearch("")}>{t("settingsLayout.clearSearch")}</button></div> : <div className="settings-content" role="tabpanel" id={`settings-panel-${selected.id}`} aria-labelledby={`settings-nav-${selected.id}`} tabIndex={0}>
        <h2 className="settings-section-title">{selected.title}</h2>
        {ManagementPage ? <div className="settings-management"><Suspense fallback={<Spinner />}><ManagementPage /></Suspense></div> : null}
        {selected.id==="resources"?<ResourceSettings/>:null}
        {selected.id === "general" ? <>
          <ErrorBanner error={settings.error} />{settings.isPending ? <Spinner /> : null}
          <form onSubmit={onSubmit}>
            <section className="settings-card" aria-label={t("preferences.languageAndTime")}>
              <SettingsRow label={t("settings.locale")} htmlFor="settings-language" description={t("settingsLayout.languageHint")}><select id="settings-language" value={locale} disabled={!settings.data || save.isPending} onChange={(event) => { formEdited.current = true; setLocale(event.target.value as Locale); }}>{SUPPORTED_LOCALES.map((value) => <option key={value} value={value}>{t(`settings.locale_${value}`)}</option>)}</select></SettingsRow>
              <SettingsRow label={t("settings.timezone")} htmlFor="settings-timezone" description={t("settings.timezoneHint", { tz: browserZone })}><div className="settings-field"><input id="settings-timezone" list="tz-options" value={timezone} disabled={!settings.data || save.isPending} onChange={(event) => { formEdited.current = true; setTimezone(event.target.value); setTzError(null); }} placeholder={t("settings.systemTimezone")} spellCheck={false} aria-invalid={!!tzError} aria-describedby={tzError ? "settings-timezone-error" : undefined} /><datalist id="tz-options">{COMMON_TIMEZONES.map((zone) => <option key={zone} value={zone} />)}</datalist>{tzError ? <span id="settings-timezone-error" className="settings-error" role="alert">{tzError}</span> : null}</div></SettingsRow>
              <div className="settings-save-row"><span>{!settings.data ? t(settings.isPending ? "common.loading" : "settingsLayout.notLoaded") : dirty ? t("settingsLayout.unsaved") : t("settingsLayout.savedInService")}</span><button type="submit" className="btn primary sm" disabled={!settings.data || save.isPending || !dirty}>{t(save.isPending ? "settingsLayout.saving" : "common.save")}</button></div>
            </section>
          </form>
          <h3 className="settings-group-title">{t("settingsLayout.windows")}</h3>
          <section className="settings-card" aria-label={t("settingsLayout.windows")}>
            <SettingsRow label={t("settingsLayout.closeWindow")} description={t("settingsLayout.closeWindowHint")}><span className="settings-readonly">{t(desktop ? "settingsLayout.hideToTray" : "settingsLayout.desktopOnly")}</span></SettingsRow>
            <SettingsRow label={t("settingsLayout.windowLayout")} description={t("settingsLayout.windowLayoutHint")}><span className="settings-readonly">{t(desktop ? "settingsLayout.restoreAutomatically" : "settingsLayout.desktopOnly")}</span></SettingsRow>
            <SettingsRow label={t("settingsLayout.startup")} description={t("settingsLayout.startupHint")}><span className="settings-readonly">{t("settingsLayout.trayMenu")}</span></SettingsRow>
          </section>
          <h3 className="settings-group-title">{t("settingsLayout.service")}</h3>
          <section className="settings-card" aria-label={t("settingsLayout.service")}>
            <ErrorBanner error={version.error} />{version.isPending ? <Spinner /> : null}
            {version.data ? <>
              <SettingsRow label={t("settingsLayout.appVersion")}><span className="settings-readonly">{version.data.version || t("common.unknown")}</span></SettingsRow>
              {workerStatus !== "ready" ? workerStatusRow : null}
              <details className="settings-service-details">
                <summary>{t("compact.details")}<Icon name="chevron" size={13} /></summary>
                {version.data.commit ? <p className="settings-footnote">{t("settingsLayout.commit", { commit: version.data.commit.slice(0, 12) })}</p> : null}
                <SettingsRow label={t("settingsLayout.startedAt")}><span className="settings-readonly">{version.data.started_at ? formatDateTime(version.data.started_at, { timeZone, locale: i18n.language }) : t("common.unknown")}</span></SettingsRow>
                {workerStatus === "ready" ? workerStatusRow : null}
                <SettingsRow label={t("settingsLayout.capabilities")} description={t("settingsLayout.capabilitiesHint")}><ul className="settings-capabilities">{CAPABILITY_LABELS.filter(([key]) => version.data?.capabilities?.[key] === 1).map(([key, label]) => <li key={key}>{t(label)}</li>)}{CAPABILITY_LABELS.every(([key]) => version.data?.capabilities?.[key] !== 1) ? <li>{t("settingsLayout.capabilitiesNone")}</li> : null}</ul></SettingsRow>
              </details>
            </> : null}
          </section>
        </> : null}
        {selected.id === "appearance" ? <section className="settings-card" aria-label={t("preferences.appearance")}>
          <SettingsRow label={t("settingsLayout.followSystem")} labelId="settings-follow-system-label" description={t("settingsLayout.followSystemHint")}><button type="button" className="settings-switch" role="switch" aria-checked={appearance === "system"} aria-labelledby="settings-follow-system-label" onClick={() => changeAppearance(appearance === "system" ? manualAppearance.current : "system")}><span /></button></SettingsRow>
          <SettingsRow label={t("settingsLayout.theme")} htmlFor="settings-theme" description={t("settingsLayout.localAppearanceHint")}><select id="settings-theme" disabled={appearance === "system"} value={appearance === "system" ? systemDark ? "dark" : "light" : appearance} onChange={(event) => changeAppearance(event.target.value as "light" | "dark")}><option value="light">{t("preferences.appearance_light")}</option><option value="dark">{t("preferences.appearance_dark")}</option></select></SettingsRow>
          <SettingsRow label={t("settingsLayout.panelWidth")} htmlFor="settings-panel-width" description={t("settingsLayout.panelWidthHint")}><div className="settings-slider"><input id="settings-panel-width" type="range" min={420} max={640} step={1} value={artifactWidth} onChange={(event) => { const next = clampArtifactWidth(Number(event.target.value)); setArtifactWidth(next); writeUiPreferences({ artifactWidth: next }); }} /><div><span>420</span><output htmlFor="settings-panel-width">{t("settingsLayout.widthValue", { n: artifactWidth })}</output><span>640</span></div></div></SettingsRow>
        </section> : null}
        {selected.id === "shortcuts" ? <>
          <section className="settings-card" aria-label={t("preferences.shortcuts")}><dl className="settings-shortcuts">{(["search", "newTask", "save", "resize"] as const).map((action) => <div className="settings-row" key={action}><dt className="settings-row-title">{t(action === "save" ? "settingsLayout.shortcutSave" : `preferences.shortcut_${action}`)}</dt><dd><kbd>{t(`preferences.shortcutKeys_${action}`)}</kbd></dd></div>)}</dl></section>
          <p className="settings-footnote">{t("preferences.shortcutsHint")}</p>
        </> : null}
        {selected.id === "files" ? <>
          <ErrorBanner error={agents.error} />{agents.isPending ? <Spinner /> : null}
          <section className="settings-card" aria-label={t("preferences.workspaces")}>
            <ul className="settings-workspaces">{agents.data?.map((agent) => <li className="settings-row" key={agent.id}><div className="settings-row-copy"><span className="settings-row-title">{agent.name}{agent.enabled ? "" : ` · ${t("common.disabled")}`}</span><p>{[agent.model.model, t(`agents.autonomy_${agent.autonomy}`)].filter(Boolean).join(" · ")}</p></div><code className="settings-path" title={agent.workspace_dir}>{agent.workspace_dir || t("preferences.workspaceUnavailable")}</code></li>)}</ul>
            {agents.data?.length === 0 ? <p className="settings-no-workspaces">{t("preferences.noWorkspaces")}</p> : null}
            <SettingsRow label={t("settingsLayout.permissions")} description={t("settingsLayout.permissionHint")}><Link className="settings-link" to={settingsHref("agents")} state={location.state}>{t("settingsLayout.view")}<Icon name="chevron" size={15} /></Link></SettingsRow>
          </section>
          <details className="settings-details"><summary>{t("settingsLayout.fileRules")}<Icon name="chevron" size={13} /></summary><p>{t("preferences.fileAccessHint")}</p><p>{t("preferences.fileAccessLimit")}</p>{typeof version.data?.capabilities?.["workbench.limits"]?.text_bytes === "number" ? <p>{t("settingsLayout.textLimit", { n: Math.round(version.data.capabilities["workbench.limits"].text_bytes / 1024) })}</p> : null}</details>
        </> : null}
      </div>}
    </div>
  </div>;
}

function SettingsRow({ label, htmlFor, labelId, description, children }: { label: string; htmlFor?: string; labelId?: string; description?: string; children: ReactNode }) {
  return <div className="settings-row"><div className="settings-row-copy">{htmlFor ? <label className="settings-row-title" htmlFor={htmlFor}>{label}</label> : <span className="settings-row-title" id={labelId}>{label}</span>}{description ? <p>{description}</p> : null}</div><div className="settings-row-control">{children}</div></div>;
}
