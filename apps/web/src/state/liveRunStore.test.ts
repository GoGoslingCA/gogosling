import { afterEach, describe, expect, it, vi } from "vitest";
import type { ApiEvent, RunEvent, RunEventType } from "../api/types";
import { LiveRunStore } from "./liveRunStore";
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
});
