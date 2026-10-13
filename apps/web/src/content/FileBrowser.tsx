import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../state/ConnectionContext";
import { ErrorBanner, Spinner } from "../components/ui";
import { Icon } from "../components/Icon";
import { ContentPreview } from "./ContentPreview";
import { folderBreadcrumbs, MAX_FIND_QUERY } from "./readingNavigation";
import "./preview.css";
import "./viewer-navigation.css";

export interface FileBrowserLocation { folder: string; filter: string }
interface Props { agentId: string; onClose: () => void; layoutAvailable?: boolean; onOpenFile?: (path: string) => void; initialLocation?: FileBrowserLocation; onLocationChange?: (location: FileBrowserLocation) => void }
export function FileBrowser(props: Props) { return <FolderBrowser key={props.agentId} {...props} />; }
function FolderBrowser({ agentId, onClose, layoutAvailable, onOpenFile, initialLocation, onLocationChange }: Props) {
  const { t } = useTranslation();
  const api = useApi();
  const [folder, setFolder] = useState(initialLocation?.folder ?? "");
  const [file, setFile] = useState("");
  const [filter, setFilter] = useState(initialLocation?.filter ?? "");
  useEffect(() => { onLocationChange?.({ folder, filter }); }, [folder, filter, onLocationChange]);
  const query = useQuery({ queryKey: ["preview-files", agentId, folder], queryFn: ({ signal }) => api.preview.list(agentId, folder, signal), gcTime: 0, staleTime: 10000, retry: false, refetchOnWindowFocus: false, enabled: !file });
  const entries = useMemo(() => {
    const needle = filter.toLowerCase();
    return query.data?.items.filter(item => item.name.toLowerCase().includes(needle)) ?? [];
  }, [filter, query.data]);
  const openFolder = (path: string) => { setFolder(path); setFilter(""); };
  if (file) return <ContentPreview agentId={agentId} path={file} layoutAvailable={layoutAvailable} onClose={() => setFile("")} />;
  return <section className="content-preview" aria-label={t("preview.files")}>
    <header className="content-preview-head"><Icon name="folder" /><strong>{t("preview.files")}</strong><button type="button" className="btn ghost sm" onClick={onClose}>{t("common.close")}</button></header>
    <div className="content-preview-toolbar">
      <nav className="reading-breadcrumbs" aria-label={t("readingNavigation.location")}>
        <button type="button" className="btn ghost sm" aria-current={!folder ? "location" : undefined} onClick={() => openFolder("")}>{t("readingNavigation.root")}</button>
        {folderBreadcrumbs(folder).map(part => <span key={part.path}><Icon name="chevron" size={11} /><button type="button" className="btn ghost sm" title={part.name} aria-current={folder === part.path ? "location" : undefined} onClick={() => openFolder(part.path)}>{part.name}</button></span>)}
      </nav>
      <button type="button" className="btn ghost sm" disabled={!folder} onClick={() => openFolder(folder.split("/").slice(0, -1).join("/"))}>{t("preview.up")}</button>
      <button type="button" className="btn ghost sm" disabled={query.isFetching} onClick={() => void query.refetch()}>{t("preview.refresh")}</button>
    </div>
    <label className="reading-file-filter"><Icon name="search" size={16} /><input type="search" maxLength={MAX_FIND_QUERY} aria-label={t("readingNavigation.filter")} placeholder={t("readingNavigation.filter")} value={filter} onChange={event => setFilter(event.target.value.slice(0, MAX_FIND_QUERY))} /></label>
    <ErrorBanner error={query.error} />
    <div className="content-preview-body">
      {query.isPending ? <Spinner /> : null}
      {query.data?.truncated ? <p className="muted">{t("preview.warning_truncated")}</p> : null}
      {query.data && !entries.length ? <p className="reading-empty">{t(filter ? "readingNavigation.noFiles" : "readingNavigation.emptyFolder")}</p> : null}
      {entries.map(item => <button type="button" className="preview-entry preview-file-link" key={item.path} onClick={() => item.is_dir ? openFolder(item.path) : onOpenFile ? onOpenFile(item.path) : setFile(item.path)}><Icon name={item.is_dir ? "folder" : "document"} size={17} /><span>{item.name}</span>{!item.is_dir ? <small>{Math.ceil(item.size / 1024)} KB</small> : null}</button>)}
    </div>
  </section>;
}
