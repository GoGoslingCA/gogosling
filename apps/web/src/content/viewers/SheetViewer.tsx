import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { ViewerProps } from "../types";
const pageSize=100;
function column(n:number):string{let result="";for(n++;n>0;n=Math.floor((n-1)/26))result=String.fromCharCode(65+(n-1)%26)+result;return result;}
export default function SheetViewer({document:doc}:ViewerProps) {
 const {t}=useTranslation();const [sheet,setSheet]=useState(0);const [page,setPage]=useState(0);
 const sheets=doc.sheets ?? [];const rows=sheets[sheet]?.rows ?? [];const columns=Math.max(0,...rows.map(r=>r.length));const shown=rows.slice(page*pageSize,(page+1)*pageSize);
 return <div className="preview-sheet"><div className="preview-pager"><select className="select" aria-label={t("preview.sheet")} value={sheet} onChange={e=>{setSheet(Number(e.target.value));setPage(0);}}>{sheets.map((s,i)=><option key={i} value={i}>{s.name}</option>)}</select><span className="spacer"/><button className="btn ghost sm" disabled={page===0} onClick={()=>setPage(v=>v-1)}>{t("preview.previous")}</button><span>{page+1} / {Math.max(1,Math.ceil(rows.length/pageSize))}</span><button className="btn ghost sm" disabled={(page+1)*pageSize>=rows.length} onClick={()=>setPage(v=>v+1)}>{t("preview.next")}</button></div><div className="preview-table-scroll"><table className="preview-table"><thead><tr><th aria-label={t("preview.row")}/>{Array.from({length:columns},(_,c)=><th key={c} scope="col">{column(c)}</th>)}</tr></thead><tbody>{shown.map((row,r)=><tr key={r}><th scope="row">{page*pageSize+r+1}</th>{Array.from({length:columns},(_,c)=><td key={c}>{row[c] ?? ""}</td>)}</tr>)}</tbody></table></div></div>;
}
