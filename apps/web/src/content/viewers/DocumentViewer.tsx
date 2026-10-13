import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { PreviewBlock, ViewerProps } from "../types";
import { documentPages, tablePages } from "../documentPaging";
export function Blocks({blocks}: {blocks: PreviewBlock[]}) {
 return <>{blocks.map((block,i)=>{
  if(block.kind==="image"&&/^data:image\/(png|jpeg|gif|webp);base64,/.test(block.image ?? ""))return <img className="preview-doc-image" key={i} src={block.image} alt="" loading="lazy"/>;
  if(block.kind==="table")return <DocumentTable key={i} rows={block.rows ?? []}/>;
  const contents=block.runs?.length ? block.runs.map((run,j)=><span key={j} style={{fontWeight:run.bold?600:undefined,fontStyle:run.italic?"italic":undefined}}>{run.text}</span>) : block.text;
  if(block.kind==="heading")return <div role="heading" aria-level={Math.max(1,Math.min(6,block.level ?? 1))} className={`preview-doc-heading level-${block.level ?? 1}`} key={i}>{contents}</div>;
  return <p key={i}>{contents || <br/>}</p>;
 })}</>;
}
function Pager({page,total,onChange}:{page:number;total:number;onChange:(page:number)=>void}){const {t}=useTranslation();return total>1?<div className="preview-pager"><button className="btn ghost sm" disabled={!page} onClick={()=>onChange(page-1)}>{t("preview.previous")}</button><span>{page+1} / {total}</span><button className="btn ghost sm" disabled={page+1>=total} onClick={()=>onChange(page+1)}>{t("preview.next")}</button></div>:null;}
function DocumentTable({ rows }: { rows: string[][] }) {
  const [requestedPage, setPage] = useState(0);
  const pages = useMemo(() => tablePages(rows), [rows]);
  const page = Math.min(requestedPage, pages.length - 1);
  const range = pages[page];
  return <><Pager page={page} total={pages.length} onChange={setPage}/><div className="preview-table-scroll"><table className="preview-table"><tbody>{rows.slice(range.start, range.end).map((row, r) => <tr key={r}>{row.slice(range.columnStart, range.columnEnd).map((cell, c) => <td key={c}>{cell}</td>)}</tr>)}</tbody></table></div></>;
}
export default function DocumentViewer({ document: doc }: ViewerProps) {
  const [requestedPage, setPage] = useState(0);
  const blocks = doc.blocks ?? [];
  const pages = useMemo(() => documentPages(blocks), [blocks]);
  const page = Math.min(requestedPage, pages.length - 1);
  const range = pages[page];
  return <><Pager page={page} total={pages.length} onChange={setPage}/><article className="preview-document"><Blocks key={page} blocks={blocks.slice(range.start, range.end)}/></article></>;
}
