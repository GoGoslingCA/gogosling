import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../../state/ConnectionContext";
import { ErrorBanner, Spinner } from "../../components/ui";
import type { ViewerProps } from "../types";
export default function MediaViewer({document:doc,agentId,path,layout=false}:ViewerProps) {
 const {t}=useTranslation();const api=useApi();const [url,setURL]=useState("");const [failed,setFailed]=useState(false);
 const activeMedia=useRef<HTMLMediaElement | null>(null);
 const mediaRef=useCallback((node:HTMLMediaElement | null)=>{if(!node && activeMedia.current){activeMedia.current.pause();activeMedia.current.removeAttribute("src");activeMedia.current.load();}activeMedia.current=node;},[]);
 const query=useQuery({queryKey:["preview-blob",agentId,path,doc.version,layout],queryFn:({signal})=>api.preview.content(agentId,path,doc.version,signal,layout),gcTime:0,staleTime:Infinity,retry:false,refetchOnWindowFocus:false});
 useEffect(()=>{if(!query.data)return;const blob=layout||doc.kind==="pdf"?new Blob([query.data],{type:"application/pdf"}):query.data;const next=URL.createObjectURL(blob);setURL(next);return()=>{URL.revokeObjectURL(next);};},[query.data,layout,doc.kind]);
 if(query.isPending)return <div className="preview-loading"><Spinner/></div>;
 if(query.error)return <ErrorBanner error={query.error}/>;
 if(failed)return <p className="empty" role="alert">{t("preview.mediaError")}</p>;
 if(!url)return null;
 if(doc.kind==="image")return <div className="preview-image"><img src={url} alt={doc.name} onError={()=>setFailed(true)}/></div>;
 return <div className="preview-media">{doc.kind==="video"?<video ref={mediaRef} src={url} controls preload="metadata" onError={()=>setFailed(true)}/>:<audio ref={mediaRef} src={url} controls preload="metadata" onError={()=>setFailed(true)}/>}</div>;
}
