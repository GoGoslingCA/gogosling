import { afterEach, describe, expect, it, vi } from "vitest";
import type { ApiEvent, RunEvent, RunEventType } from "../api/types";
import { LiveRunStore } from "./liveRunStore";
import { initialLiveRuns, isTerminal, liveRunsReducer, selectSessionRuns } from "./liveRuns";
let seq=0;
function event(type:RunEventType,session="a",extra:Partial<RunEvent>={}):ApiEvent {
 const payload:RunEvent={type,session_id:session,agent_id:session,run_id:session,at:"2026-10-12T00:00:00Z",...extra};return {seq:++seq,type,at:payload.at,payload};
}
afterEach(()=>vi.useRealTimers());
describe("按会话批量展示事件",()=>{
 it("交错的 8000 个增量合为 80 次当前会话通知，文本逐字一致",()=>{
  vi.useFakeTimers();const store=new LiveRunStore(()=>80);const a=vi.fn();const b=vi.fn();store.subscribe("a",a);store.subscribe("b",b);
  for(let batch=0;batch<80;batch++){
   for(let i=0;i<100;i++)store.receive(event("text.delta",i%2?"a":"b",{delta:i%2?"甲":"乙"}));
   vi.advanceTimersByTime(80);
  }
  expect(store.snapshot("a").active?.text).toBe("甲".repeat(4000));expect(store.snapshot("b").active?.text).toBe("乙".repeat(4000));
  expect(a).toHaveBeenCalledTimes(80);expect(b).toHaveBeenCalledTimes(80);store.dispatch({type:"reset"});
 });
 it("后台会话变化不通知前台，审批先刷新文字再立即展示",()=>{
  vi.useFakeTimers();const store=new LiveRunStore(()=>80);const listener=vi.fn();store.subscribe("a",listener);const before=store.snapshot("a");
  store.receive(event("text.delta","b",{delta:"后台"}));vi.advanceTimersByTime(80);
  expect(listener).not.toHaveBeenCalled();expect(store.snapshot("a")).toBe(before);
  store.receive(event("text.delta","a",{delta:"等待"}));store.receive(event("approval.requested","a"));
  expect(store.snapshot("a").active?.text).toBe("等待");expect(store.snapshot("a").active?.status).toBe("awaiting_approval");store.dispatch({type:"reset"});
 });
 it("重放去重、两种增量保持顺序；缓冲达到上限立即刷新",()=>{
  vi.useFakeTimers();const store=new LiveRunStore(()=>300);const first=event("text.delta","a",{delta:"一"});store.receive(first);store.receive(first);
  store.receive(event("reasoning.delta","a",{delta:"思"}));store.receive(event("text.delta","a",{delta:"二"}));store.flush();
  expect(store.snapshot("a").active?.text).toBe("一二");expect(store.snapshot("a").active?.reasoning).toBe("思");
  store.receive(event("text.delta","a",{delta:"x".repeat(65536)}));expect(store.snapshot("a").active?.text.length).toBe(65538);store.dispatch({type:"reset"});
 });
 it("只回收已落库的非当前终态；未保存失败内容保留，退订后清理监听",()=>{
  const store=new LiveRunStore();const unsubscribe=store.subscribe("a",()=>undefined);
  store.receive(event("run.completed","a",{message:{id:"m",session_id:"a",run_id:"a",role:"assistant",parts:[{type:"text",text:"已保存"}],seq:2,created_at:"now"}}));
  expect(store.snapshot("a").runs).toHaveLength(1);unsubscribe();expect(store.snapshot("a").runs).toHaveLength(0);
  store.receive(event("text.delta","b",{delta:"未保存"}));store.receive(event("run.failed","b",{error:"失败"}));
  expect(store.snapshot("b").runs[0]?.text).toBe("未保存");store.dispatch({type:"reset"});expect(store.snapshot("b").runs).toHaveLength(0);
 });
 it("用户消息代表排队，内核重启后低序号恢复事件仍可结算",()=>{
  const store=new LiveRunStore();store.subscribe("a",()=>undefined);
  store.receive(event("message.created","a",{message:{id:"m",session_id:"a",run_id:"a",role:"user",parts:[],seq:1,created_at:"now"}}));
  expect(store.snapshot("a").active?.status).toBe("queued");store.restart();
  const restored=event("run.failed","a",{error:"daemon restarted"});store.receive({...restored,seq:1});
  expect(store.snapshot("a").active).toBeNull();expect(store.snapshot("a").runs[0]?.error).toBe("daemon restarted");
 });
 it("卸载清除待执行的计时器，不会把旧连接增量带入新连接",()=>{
  vi.useFakeTimers();const store=new LiveRunStore();store.receive(event("text.delta","a",{delta:"旧内容"}));store.dispatch({type:"reset"});
  expect(vi.getTimerCount()).toBe(0);vi.runAllTimers();expect(store.snapshot("a").runs).toHaveLength(0);
 });
 it("旧退订函数重复调用不会删除同会话的新订阅",()=>{
  const store=new LiveRunStore();const old=vi.fn();const current=vi.fn();
  const stopOld=store.subscribe("a",old);stopOld();
  const stopCurrent=store.subscribe("a",current);stopOld();
  store.receive(event("run.started"));
  expect(current).toHaveBeenCalledTimes(1);expect(old).not.toHaveBeenCalled();
  store.receive(event("run.completed","a",{message:{id:"m",session_id:"a",run_id:"a",role:"assistant",parts:[{type:"text",text:"保存结果"}],seq:2,created_at:"now"}}));
  expect(store.snapshot("a").runs).toHaveLength(1);stopCurrent();expect(store.snapshot("a").runs).toHaveLength(0);
 });
 it("无关会话事件复用后台快照，本会话更新和迟到的落库确认才回收它",()=>{
  const store=new LiveRunStore();store.receive(event("text.delta","b",{delta:"尚未保存"}));store.receive(event("run.failed","b"));
  const retained=store.snapshot("b");store.receive(event("run.started","a"));
  expect(store.snapshot("b")).toBe(retained);
  store.receive(event("message.created","b",{message:{id:"saved-late",session_id:"b",run_id:"b",role:"assistant",parts:[{type:"text",text:"尚未保存"}],seq:2,created_at:"now"}}));
  expect(store.snapshot("b").runs).toHaveLength(0);expect(store.snapshot("a").active?.runId).toBe("a");store.dispatch({type:"reset"});
 });
 it("同会话批量回收已保存终态时保留未保存草稿",()=>{
  const store=new LiveRunStore();const unsubscribe=store.subscribe("a",()=>undefined);
  for(let index=0;index<100;index++){
   const run_id=`saved-${index}`;
   store.receive(event("run.completed","a",{run_id,message:{id:`message-${index}`,session_id:"a",run_id,role:"assistant",parts:[{type:"text",text:"已保存"}],seq:index,created_at:"now"}}));
  }
  store.receive(event("text.delta","a",{run_id:"unsaved",delta:"待恢复草稿"}));store.receive(event("run.cancelled","a",{run_id:"unsaved"}));
  expect(store.snapshot("a").runs).toHaveLength(101);unsubscribe();
  expect(store.snapshot("a").runs.map(run=>[run.runId,run.text])).toEqual([["unsaved","待恢复草稿"]]);store.dispatch({type:"reset"});
 });
 it("按会话分片与全量 reducer 的交错事件结果一致，未落库文本不被回收",()=>{
  const store=new LiveRunStore();let reference=initialLiveRuns;const saved=new Set<string>();
  const subscribed=new Set(["session-1","session-3"]);
  for(const id of subscribed)store.subscribe(id,()=>undefined);
  const receive=(ev:ApiEvent)=>{
   store.receive(ev);reference=liveRunsReducer(reference,{type:"event",event:ev});
   const payload=ev.payload as RunEvent;if(payload.message?.role==="assistant")saved.add(payload.run_id);
   for(const run of Object.values(reference.runs))if(isTerminal(run.status)&&saved.has(run.runId)&&!subscribed.has(run.sessionId)){
    reference=liveRunsReducer(reference,{type:"forget",runId:run.runId});saved.delete(run.runId);
   }
  };
  for(let session=0;session<12;session++)for(let run=0;run<4;run++){
   const id=`session-${session}`;const run_id=`${id}-${run}`;
   receive(event("run.started",id,{run_id}));receive(event("text.delta",id,{run_id,delta:"保留正文"}));
   receive(event("run.failed",id,{run_id,error:"保留失败草稿"}));
  }
  for(let session=0;session<4;session++)receive(event("run.started",`session-${session}`,{run_id:`active-${session}`}));
  for(let batch=0;batch<20;batch++){
   for(let session=0;session<4;session++){
    const id=`session-${session}`;const run_id=`active-${session}`;
    receive(event("text.delta",id,{run_id,delta:`${batch}中文🙂`}));
    receive(event("reasoning.delta",id,{run_id,delta:"思考"}));
    receive(event("text.delta",id,{run_id,delta:"继续"}));
    receive(event("usage",id,{run_id,usage:{input_tokens:2,output_tokens:1}}));
   }
   store.flush();
   for(let session=0;session<12;session++)expect(store.snapshot(`session-${session}`).runs).toEqual(selectSessionRuns(reference,`session-${session}`));
  }
  for(let session=0;session<4;session++){
   const id=`session-${session}`;const run_id=`active-${session}`;
   receive(event("approval.requested",id,{run_id}));receive(event("approval.resolved",id,{run_id}));
   receive(event("run.completed",id,{run_id,message:{id:`message-${session}`,session_id:id,run_id,role:"assistant",parts:[{type:"text",text:"完整已保存"}],seq:10,created_at:"now"}}));
  }
  for(let session=0;session<12;session++)expect(store.snapshot(`session-${session}`).runs).toEqual(selectSessionRuns(reference,`session-${session}`));
  store.dispatch({type:"reset"});
 });
 it("忘记运行、缺口、实例重启和重置覆盖所有会话且保持运行归属",()=>{
  const store=new LiveRunStore();store.subscribe("a",()=>undefined);store.subscribe("b",()=>undefined);
  store.receive(event("run.started","a",{run_id:"shared-run"}));
  store.receive(event("run.started","b",{run_id:"other-run"}));
  store.receive(event("text.delta","b",{run_id:"shared-run",delta:"仍属原会话"}));store.flush();
  expect(store.snapshot("a").active?.text).toBe("仍属原会话");expect(store.snapshot("b").runs.map(run=>run.runId)).toEqual(["other-run"]);
  store.dispatch({type:"gap"});expect(store.snapshot("a").active?.truncated).toBe(true);expect(store.snapshot("b").active?.truncated).toBe(true);
  store.restart();const failed=event("run.failed","b",{run_id:"other-run",error:"重启"});store.receive({...failed,seq:1});
  expect(store.snapshot("b").active).toBeNull();
  store.dispatch({type:"forget",runId:"shared-run"});expect(store.snapshot("a").runs).toHaveLength(0);expect(store.snapshot("b").runs).toHaveLength(1);
  store.dispatch({type:"reset"});expect(store.snapshot("b").runs).toHaveLength(0);
  store.receive({...event("run.started","b",{run_id:"shared-run"}),seq:1});expect(store.snapshot("b").active?.runId).toBe("shared-run");
  expect(store.snapshot("a").runs).toHaveLength(0);store.dispatch({type:"reset"});
 });
});
