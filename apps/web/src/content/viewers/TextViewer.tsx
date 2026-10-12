import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Markdown } from "../../pages/chat/Markdown";
import { highlightArtifactCode } from "../../lib/artifacts";
import type { ViewerProps } from "../types";
const pageChars=60000;
export default function TextViewer({document:doc}: ViewerProps) {
 const {t}=useTranslation();const [source,setSource]=useState(false);const [page,setPage]=useState(0);
 const all=doc.text ?? "";const pages=Math.max(1,Math.ceil(all.length/pageChars));const large=pages>1;
 let start=page*pageChars,end=Math.min(all.length,(page+1)*pageChars);
 // UTF-16 代理对不能拆在分页边界。
 if(start>0&&all.charCodeAt(start)>=0xdc00&&all.charCodeAt(start)<=0xdfff)start--;
 if(end<all.length&&all.charCodeAt(end)>=0xdc00&&all.charCodeAt(end)<=0xdfff)end--;
 const text=all.slice(start,end);const ext=doc.name.split(".").at(-1) ?? "text";
 const tokens=useMemo(()=>highlightArtifactCode(text,ext),[text,ext]);
 return <div className="preview-text">
  {large ? <div className="preview-pager"><span className="muted">{t("preview.source")}</span><span className="spacer"/><button className="btn ghost sm" disabled={!page} onClick={()=>setPage(v=>v-1)}>{t("preview.previous")}</button><span>{page+1} / {pages}</span><button className="btn ghost sm" disabled={page+1>=pages} onClick={()=>setPage(v=>v+1)}>{t("preview.next")}</button></div> : doc.kind==="markdown" ? <div className="preview-mode"><button className="btn ghost sm" aria-pressed={source} onClick={()=>setSource(!source)}>{t(source?"preview.read":"preview.source")}</button></div>:null}
  {doc.kind==="markdown"&&!source&&!large ? <Markdown text={text}/> : <pre className="preview-code"><code>{tokens.map((token,i)=><span key={i} className={`syntax-${token.kind}`}>{token.text}</span>)}</code></pre>}
 </div>;
}
