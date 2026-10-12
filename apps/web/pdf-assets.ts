// PDF 的字体、CMap 和解码模块随发行包提供，只在打开对应 PDF 时请求。
// 开发和构建使用同一资源目录；不依赖 CDN，也不复制整个 pdfjs-dist 到客户端。
import { createRequire } from "node:module";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import type { Plugin } from "vite";
export function pdfAssets():Plugin {
 const require=createRequire(import.meta.url);const root=dirname(require.resolve("pdfjs-dist/package.json"));
 const groups=["cmaps","standard_fonts","wasm"];
 return {name:"local-pdf-assets",configureServer(server){server.middlewares.use((req,res,next)=>{
  const match=/^\/pdf-assets\/(cmaps|standard_fonts|wasm)\/([a-zA-Z0-9_.-]+)$/.exec(req.url??"");if(!match)return next();
  try{const data=readFileSync(join(root,match[1]!,match[2]!));res.setHeader("Content-Type",match[2]!.endsWith(".wasm")?"application/wasm":"application/octet-stream");res.end(data);}catch{res.statusCode=404;res.end();}
 });},generateBundle(){for(const group of groups)for(const name of readdirSync(join(root,group))){if(name.endsWith(".map") || name.startsWith("quickjs"))continue;this.emitFile({type:"asset",fileName:`pdf-assets/${group}/${name}`,source:readFileSync(join(root,group,name))});}this.emitFile({type:"asset",fileName:"pdf-assets/LICENSE",source:readFileSync(join(root,"LICENSE"))});}};
}
