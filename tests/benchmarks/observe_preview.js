// Test-only observer for a local built client with synthetic fixtures.
// Include before the client entrypoint in a temporary test page; it makes no network requests.
// Read #benchmark-results.textContent, then close the test page to release observers.
(() => {
 const samples=[], tasks=[]; let pending=null; let sequence=0;
 const output=document.createElement('output'); output.id='benchmark-results'; output.hidden=true;
 const publish=()=>{output.dataset.count=String(samples.length);output.textContent=JSON.stringify({viewport:{width:innerWidth,height:innerHeight},samples,longTasks:tasks});};
 document.addEventListener('DOMContentLoaded',()=>{document.body.append(output);publish();});
 try { new PerformanceObserver(list=>{for(const e of list.getEntries()) tasks.push({start:e.startTime,duration:e.duration});publish();}).observe({type:'longtask',buffered:true}); }catch{}
 document.addEventListener('click',event=>{
  const b=event.target.closest?.('.workspace-open-link,.preview-entry.preview-file-link');
  if(!b)return;
  const file=b.querySelector('span')?.textContent?.trim();
  if(!/\.(md|docx)$/.test(file??''))return;
  pending={file,start:performance.now(),id:++sequence};
 },true);
 new MutationObserver(()=>{
  const p=pending;if(!p)return;
  const preview=document.querySelector('.workspace-pane .content-preview');
  if(preview?.querySelector('header strong')?.textContent!==p.file)return;
  const body=preview.querySelector('.reading-text-body,.preview-document');
  if(!body || !body.textContent || preview.querySelector('.preview-loading'))return;
  pending=null;
  requestAnimationFrame(()=>requestAnimationFrame(()=>{
   if(p.id!==sequence)return;
   const end=performance.now();samples.push({file:p.file,startMs:p.start,endMs:end,visibility:document.visibilityState,clickToPaintMs:end-p.start,domNodes:body.querySelectorAll('*').length,heapBytes:performance.memory?.usedJSHeapSize??null,longTaskMs:tasks.filter(t=>t.start>=p.start&&t.start<end).reduce((a,t)=>a+t.duration,0)});publish();
  }));
 }).observe(document,{childList:true,subtree:true});
})();
