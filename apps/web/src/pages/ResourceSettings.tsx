// 只在资源页挂载时读取统计；配置由内核保存，降低并行数不杀死在途任务。
import { useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useApi } from "../state/ConnectionContext";
import { queryKeys } from "../state/queryKeys";
import { ErrorBanner, Spinner } from "../components/ui";

export function ResourceSettings(){
  const {t}=useTranslation();const api=useApi();const client=useQueryClient();const lock=useRef(false);
  const state=useQuery({queryKey:queryKeys.resources,queryFn:()=>api.system.resources(),refetchInterval:5000,refetchIntervalInBackground:false});
  const update=useMutation({mutationFn:(limit:number)=>api.system.setResources(limit),onSuccess:value=>client.setQueryData(queryKeys.resources,value),onSettled:()=>{lock.current=false;}});
  const data=state.data;
  return <>
    <ErrorBanner error={update.error??state.error}/>
    {!data&&state.isPending?<Spinner/>:null}
    {data?<section className="settings-card" aria-label={t("resources.title")}>
      <div className="settings-row"><div className="settings-row-copy"><label className="settings-row-title" htmlFor="parallel-limit">{t("resources.parallel")}</label><p>{t("resources.parallelHint")}</p></div><div className="settings-row-control"><select id="parallel-limit" disabled={update.isPending} value={data.max_parallel} onChange={event=>{if(!lock.current){lock.current=true;update.mutate(Number(event.target.value));}}}>{[1,2,3,4,5,6,7,8].map(value=><option key={value} value={value}>{t(value===1?"resources.light":value===2?"resources.balanced":value===4?"resources.parallelFour":"resources.count",{n:value})}</option>)}</select></div></div>
      <div className="settings-row"><div className="settings-row-copy"><span className="settings-row-title">{t("resources.running")}</span><p>{t("resources.approvalCount",{n:data.awaiting_approval})}</p></div><div className="resource-occupancy"><strong>{data.running} / {data.max_parallel}</strong><div className="resource-slots" aria-label={t("resources.slotLabel",{running:data.running,max:data.max_parallel})}>{Array.from({length:Math.max(data.running,data.max_parallel)},(_,i)=><span key={i} data-occupied={i<data.running}/>)}</div></div></div>
      <div className="settings-row"><div className="settings-row-copy"><span className="settings-row-title">{t("resources.queued")}</span><p>{t("resources.queueHint",{n:data.max_queued})}</p></div><strong>{data.queued}</strong></div>
    </section>:null}
    <details className="settings-details"><summary>{t("resources.rules")}</summary><p>{t("resources.rulesHint")}</p></details>
  </>;
}
