import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { getDocument, GlobalWorkerOptions, type PDFDocumentProxy, type PDFPageProxy, type RenderTask } from "pdfjs-dist";
import workerURL from "pdfjs-dist/build/pdf.worker.min.mjs?url";
import { useApi } from "../../state/ConnectionContext";
import { ErrorBanner, Spinner } from "../../components/ui";
import type { ViewerProps } from "../types";
GlobalWorkerOptions.workerSrc=workerURL;
/** 独立 worker 解析；只渲染当前页，不启用脚本、表单、附件或可执行批注。 */
export default function PDFViewer({document:doc,agentId,path,layout=false}:ViewerProps) {
 const {t}=useTranslation();const api=useApi();const [pdf,setPDF]=useState<PDFDocumentProxy | null>(null);const [page,setPage]=useState(1);const [error,setError]=useState<string | null>(null);const [textMode,setTextMode]=useState(false);
 const query=useQuery({queryKey:["preview-pdf",agentId,path,doc.version,layout],queryFn:async({signal})=>new Uint8Array(await (await api.preview.content(agentId,path,doc.version,signal,layout)).arrayBuffer()),gcTime:0,staleTime:Infinity,retry:false,refetchOnWindowFocus:false});
 useEffect(()=>{
  if(!query.data)return;let disposed=false;
  const assets=new URL("pdf-assets/",window.document.baseURI).href;
  const task=getDocument({data:query.data,cMapUrl:assets+"cmaps/",standardFontDataUrl:assets+"standard_fonts/",wasmUrl:assets+"wasm/",useWorkerFetch:true,stopAtErrors:true,enableXfa:false,maxImageSize:16000000,canvasMaxAreaInBytes:32000000,verbosity:0});
  const deadline=setTimeout(()=>{if(!disposed)setError("renderError");void task.destroy().catch(()=>undefined);},30000);
  task.onPassword=()=>{if(!disposed)setError("encrypted");void task.destroy().catch(()=>undefined);};
  void task.promise.then(value=>{clearTimeout(deadline);if(!disposed)setPDF(value);}).catch(()=>{clearTimeout(deadline);if(!disposed)setError(previous=>previous ?? "renderError");void task.destroy().catch(()=>undefined);});
  return()=>{disposed=true;clearTimeout(deadline);void task.destroy().catch(()=>undefined);};
 },[query.data]);
 if(query.error)return <ErrorBanner error={query.error}/>;
 if(error)return <p className="empty" role="alert">{t(`preview.${error}`)}</p>;
 if(!pdf)return <div className="preview-loading"><Spinner/></div>;
 return <div className="preview-pdf-reader"><div className="preview-pager"><button className="btn ghost sm" disabled={page===1} onClick={()=>setPage(v=>v-1)}>{t("preview.previous")}</button><span>{page} / {pdf.numPages}</span><button className="btn ghost sm" disabled={page>=pdf.numPages} onClick={()=>setPage(v=>v+1)}>{t("preview.next")}</button><span className="spacer"/><button className="btn ghost sm" aria-pressed={textMode} onClick={()=>setTextMode(!textMode)}>{t(textMode?"preview.layout":"preview.text")}</button></div><PDFPage key={`${page}:${textMode}`} pdf={pdf} number={page} textMode={textMode}/></div>;
}
function PDFPage({pdf,number,textMode}:{pdf:PDFDocumentProxy;number:number;textMode:boolean}){
 const {t}=useTranslation();const canvas=useRef<HTMLCanvasElement>(null);const container=useRef<HTMLDivElement>(null);const [width,setWidth]=useState(500);const [error,setError]=useState(false);const [text,setText]=useState("");
 useEffect(()=>{const el=container.current;if(!el)return;const observer=new ResizeObserver(entries=>{const next=Math.round(entries[0]?.contentRect.width ?? 500);setWidth(Math.max(240,Math.min(1200,next-24)));});observer.observe(el);return()=>observer.disconnect();},[]);
 useEffect(()=>{
  let disposed=false;let render:RenderTask | undefined;let page:PDFPageProxy | undefined;
  const deadline=setTimeout(()=>{if(!disposed){setError(true);render?.cancel();void pdf.loadingTask.destroy().catch(()=>undefined);}},15000);
  const work=async()=>{
   page=await pdf.getPage(number);if(disposed){page.cleanup();return;}
   if(textMode){const content=await page.getTextContent();if(!disposed)setText(content.items.map(item=>"str" in item?item.str+(item.hasEOL?"\n":" "):"").join("").slice(0,200000));return;}
   const el=canvas.current;if(!el)return;const unscaled=page.getViewport({scale:1});const density=Math.min(2,window.devicePixelRatio||1);let scale=width/unscaled.width*density;
   // 纵向或异常长页面也有像素上限，避免画布巨大分配。
   scale=Math.min(scale,Math.sqrt(4000000/(unscaled.width*unscaled.height)));
   const viewport=page.getViewport({scale});el.width=Math.ceil(viewport.width);el.height=Math.ceil(viewport.height);el.style.width=`${Math.round(viewport.width/density)}px`;el.style.height=`${Math.round(viewport.height/density)}px`;
   render=page.render({canvas:el,viewport});await render.promise;
  };
  void work().catch(()=>{if(!disposed)setError(true);}).finally(()=>clearTimeout(deadline));
  return()=>{disposed=true;clearTimeout(deadline);render?.cancel();if(render)void render.promise.catch(()=>undefined).then(()=>page?.cleanup());else page?.cleanup();};
 },[pdf,number,width,textMode]);
 return <div ref={container} className="preview-pdf-page">{error?<p role="alert">{t("preview.renderError")}</p>:textMode?<pre className="preview-code">{text}</pre>:<canvas ref={canvas} role="img" aria-label={t("preview.pdfPage",{n:number})}/>}</div>;
}
