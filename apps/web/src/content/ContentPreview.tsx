import { Component, Suspense, useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ApiError } from "../api/client";
import { useApi } from "../state/ConnectionContext";
import { ErrorBanner, Spinner } from "../components/ui";
import { Icon } from "../components/Icon";
import { viewerFor } from "./registry";
import "./preview.css";

interface Props { agentId: string; path: string; onClose: () => void; layoutAvailable?: boolean }
class ViewerBoundary extends Component<{children: ReactNode; fallback: ReactNode}, {failed: boolean}> {
  override state = {failed:false};
  static getDerivedStateFromError() { return {failed:true}; }
  override render() { return this.state.failed ? this.props.fallback : this.props.children; }
}
/** key 限定文件生命周期，切换/关闭会取消请求并卸载原来的展示器。 */
export function ContentPreview(props: Props) { return <PreviewFile key={`${props.agentId}:${props.path}`} {...props}/>; }
function PreviewFile({agentId,path,onClose,layoutAvailable = false}: Props) {
  const {t} = useTranslation(); const api = useApi();
  const [revision,setRevision] = useState(0); const [layout,setLayout] = useState(false);
  const [downloadError,setDownloadError] = useState<unknown>(null); const [downloading,setDownloading] = useState(false);
  const downloadRequest = useRef<AbortController | null>(null);
  useEffect(() => () => { downloadRequest.current?.abort(); }, []);
  const query = useQuery({queryKey:["file-preview",agentId,path,revision],queryFn:({signal}) => api.preview.read(agentId,path,signal),gcTime:0,staleTime:Infinity,retry:(count,error)=>count<2 && error instanceof ApiError && error.details?.reason==="preview_busy",retryDelay:120,refetchOnWindowFocus:false});
  const doc = query.data; const converting = doc?.kind === "conversion" || layout;
  const Viewer = viewerFor(converting ? "pdf" : doc?.kind ?? "");
  const name = doc?.name ?? path.split(/[\\/]/).at(-1) ?? path;
  const download = async () => {
    if (downloadRequest.current) return;
    const controller = new AbortController(); downloadRequest.current=controller;setDownloading(true);setDownloadError(null);
    try {
      const blob = await api.preview.content(agentId,path,doc?.version,controller.signal);
      if (controller.signal.aborted) return;
      const url=URL.createObjectURL(blob);const link=window.document.createElement("a");link.href=url;link.download=name;link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
    } catch(error) {if (!controller.signal.aborted) setDownloadError(error);} finally {if (!controller.signal.aborted) setDownloading(false);downloadRequest.current=null;}
  };
  return <section className="content-preview" aria-label={t("preview.title")}>
    <header className="content-preview-head"><Icon name="document" size={18}/><strong title={path}>{name}</strong><button type="button" className="btn ghost sm" aria-label={t("common.close")} onClick={onClose}><Icon name="close" size={16}/></button></header>
    <div className="content-preview-toolbar">
      {doc ? <span className="preview-format">{(name.split(".").at(-1) || doc.format).toUpperCase()} · {doc.size<1024 ? `${doc.size} B` : `${Math.ceil(doc.size/1024)} KB`}</span> : null}
      <span className="spacer"/>
      {layoutAvailable && doc && ["word","slides","sheets","opendocument"].includes(doc.format) ? <button className="btn ghost sm" aria-pressed={layout} onClick={() => setLayout(!layout)}>{t(layout ? "preview.content" : "preview.layout")}</button> : null}
      <button className="btn ghost sm" onClick={() => {setRevision(v=>v+1);setDownloadError(null);}} disabled={query.isFetching}>{t("preview.refresh")}</button>
      <button className="btn ghost sm" onClick={() => void download()} disabled={downloading}>{t(downloading ? "common.loading" : "preview.download")}</button>
    </div>
    <ErrorBanner error={query.error ?? downloadError}/>
    {query.isPending ? <div className="preview-loading"><Spinner/></div> : null}
    {doc ? <>
      {doc.warnings?.length && !converting ? <details className="preview-notes"><summary>{t("preview.notes")}</summary>{[...new Set(doc.warnings)].map(w=><p key={w}>{t(`preview.warning_${w}`)}</p>)}</details> : null}
      <div className="content-preview-body"><ViewerBoundary key={`${revision}:${converting}`} fallback={<p className="empty" role="alert">{t("preview.renderError")}</p>}><Suspense fallback={<div className="preview-loading"><Spinner/></div>}>
        {Viewer && (!converting || layoutAvailable) ? <Viewer key={`${revision}:${converting}`} document={doc} agentId={agentId} path={path} layout={converting}/> : <div className="preview-empty"><Icon name="document" size={32}/><p>{t(converting ? "preview.converterUnavailable" : "preview.unsupported")}</p></div>}
      </Suspense></ViewerBoundary></div>
    </> : null}
  </section>;
}
