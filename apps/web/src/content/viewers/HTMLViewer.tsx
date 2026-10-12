import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ViewerProps } from "../types";
// 内容先移除所有能够跳转/执行/嵌套文档的元素，再由无权限 iframe + CSP 隔离。
// iframe 不拥有父页面 origin，也拿不到 API token。远程/相对资源均不加载。
export function isolatedHTML(source:string):string {
 const doc=new DOMParser().parseFromString(source,"text/html");
 doc.querySelectorAll("script,iframe,frame,frameset,object,embed,applet,base,meta,link,form,input,button,textarea,select,foreignObject,animate,animateMotion,animateTransform,set").forEach(el=>el.remove());
 for(const el of doc.querySelectorAll("*")){
  for(const attr of [...el.attributes]){
   const name=attr.name.toLowerCase();const value=attr.value.trim();
   if(name.startsWith("on")||["srcdoc","href","xlink:href","action","formaction","target","ping","srcset","poster","background","autofocus"].includes(name)||(name==="src"&&!/^data:image\/(png|jpeg|gif|webp|avif);base64,/i.test(value)))el.removeAttribute(attr.name);
  }
 }
 const policy="default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'";
 return `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${policy}"><meta charset="utf-8"><style>html{color-scheme:light}body{overflow-wrap:anywhere}*{animation:none!important;transition:none!important}img{max-width:100%}</style>${doc.head.innerHTML}</head><body>${doc.body.innerHTML}</body></html>`;
}
export default function HTMLViewer({document:doc}:ViewerProps) {
 const {t}=useTranslation();const [source,setSource]=useState(false);const html=useMemo(()=>isolatedHTML(doc.text??""),[doc.text]);
 return <div className="preview-html"><div className="preview-mode"><span className="muted">{t("preview.staticHTML")}</span><button className="btn ghost sm" onClick={()=>setSource(!source)}>{t(source?"preview.read":"preview.source")}</button></div>{source?<pre className="preview-code">{doc.text}</pre>:<iframe sandbox="" referrerPolicy="no-referrer" title={doc.name} srcDoc={html}/>}</div>;
}
