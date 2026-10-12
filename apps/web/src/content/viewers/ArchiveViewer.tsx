import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/Icon";
import type { ViewerProps } from "../types";
export default function ArchiveViewer({document:doc}:ViewerProps) {
 const {t}=useTranslation();const [page,setPage]=useState(0);const entries=doc.entries??[];
 return <div className="preview-archive"><div className="preview-pager"><span>{t("preview.entries",{n:entries.length})}</span><span className="spacer"/><button className="btn ghost sm" disabled={!page} onClick={()=>setPage(v=>v-1)}>{t("preview.previous")}</button><button className="btn ghost sm" disabled={(page+1)*100>=entries.length} onClick={()=>setPage(v=>v+1)}>{t("preview.next")}</button></div>{entries.slice(page*100,(page+1)*100).map((item,i)=><div className="preview-entry" key={i}><Icon name={item.directory?"folder":"document"} size={15}/><span>{item.name}</span><small>{Math.ceil(item.size/1024)} KB</small></div>)}</div>;
}
