import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Blocks } from "./DocumentViewer";
import type { ViewerProps } from "../types";
export default function SlidesViewer({document:doc}:ViewerProps) {
 const {t}=useTranslation();const [page,setPage]=useState(0);const slides=doc.slides ?? [];
 return <div className="preview-slides"><div className="preview-pager"><button className="btn sm" disabled={page===0} onClick={()=>setPage(v=>v-1)}>{t("preview.previous")}</button><span>{page+1} / {slides.length}</span><button className="btn sm" disabled={page>=slides.length-1} onClick={()=>setPage(v=>v+1)}>{t("preview.next")}</button></div><article className="preview-slide" aria-label={t("preview.slide",{n:page+1})}><Blocks blocks={slides[page]?.blocks ?? []}/></article></div>;
}
