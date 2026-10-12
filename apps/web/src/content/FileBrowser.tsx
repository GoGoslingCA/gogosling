import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../state/ConnectionContext";
import { ErrorBanner, Spinner } from "../components/ui";
import { Icon } from "../components/Icon";
import { ContentPreview } from "./ContentPreview";
import "./preview.css";
export function FileBrowser({agentId,onClose,layoutAvailable}:{agentId:string;onClose:()=>void;layoutAvailable?:boolean}){
 const {t}=useTranslation();const api=useApi();const [folder,setFolder]=useState("");const [file,setFile]=useState("");
 const query=useQuery({queryKey:["preview-files",agentId,folder],queryFn:({signal})=>api.preview.list(agentId,folder,signal),gcTime:0,staleTime:10000,retry:false,refetchOnWindowFocus:false,enabled:!file});
 if(file)return <ContentPreview agentId={agentId} path={file} layoutAvailable={layoutAvailable} onClose={()=>setFile("")}/>;
 return <section className="content-preview" aria-label={t("preview.files")}><header className="content-preview-head"><Icon name="folder"/><strong>{folder||t("preview.files")}</strong><button className="btn ghost sm" onClick={onClose}>{t("common.close")}</button></header><div className="content-preview-toolbar"><button className="btn ghost sm" disabled={!folder} onClick={()=>setFolder(folder.split("/").slice(0,-1).join("/"))}>{t("preview.up")}</button><span className="spacer"/><button className="btn ghost sm" disabled={query.isFetching} onClick={()=>void query.refetch()}>{t("preview.refresh")}</button></div><ErrorBanner error={query.error}/><div className="content-preview-body">{query.isPending?<Spinner/>:null}{query.data?.truncated?<p className="muted">{t("preview.warning_truncated")}</p>:null}{query.data?.items.map(item=><button className="preview-entry preview-file-link" key={item.path} onClick={()=>item.is_dir?setFolder(item.path):setFile(item.path)}><Icon name={item.is_dir?"folder":"document"} size={17}/><span>{item.name}</span>{!item.is_dir?<small>{Math.ceil(item.size/1024)} KB</small>:null}</button>)}</div></section>;
}
