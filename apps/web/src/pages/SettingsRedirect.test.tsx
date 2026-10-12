import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { legacySettingsRoutes, settingsHref, settingsReturnTarget, settingsSection } from "../lib/settingsNavigation";
import { SettingsRedirect } from "./SettingsRedirect";
let root:Root;let host:HTMLDivElement;
beforeEach(()=>{(globalThis as {IS_REACT_ACT_ENVIRONMENT?:boolean}).IS_REACT_ACT_ENVIRONMENT=true;host=document.createElement("div");document.body.append(host);root=createRoot(host);});
afterEach(async()=>{await act(async()=>root.unmount());host.remove();});
describe("旧管理链接迁移",()=>{
 it.each(Object.entries(legacySettingsRoutes))("%s 直达设置分组 %s，查询和后退连续",async(path,section)=>{
  const router=createMemoryRouter([{path:"/chat/saved",element:<div>saved-task</div>},{path,element:<SettingsRedirect section={section}/>},{path:"/settings",element:<div>settings</div>}],{initialEntries:["/chat/saved",{pathname:path,search:"?filter=enabled&section=bad",state:{from:"task"}}]});
  await act(async()=>root.render(<RouterProvider router={router}/>));
  expect(router.state.location.pathname).toBe("/settings");
  expect(new URLSearchParams(router.state.location.search).get("section")).toBe(section);
  expect(new URLSearchParams(router.state.location.search).get("filter")).toBe("enabled");
  expect(router.state.location.state).toEqual({from:"task"});
  await act(async()=>{await router.navigate(-1);});expect(router.state.location.pathname).toBe("/chat/saved");
 });
 it("非法分组回落，生成链接保留并转义查询值",()=>{
  expect(settingsSection("?section=bad")).toBe("general");
  expect(settingsHref("agents","?q=a%26b")).toBe("/settings?q=a%26b&section=agents");
 });
});

it("返回目标保留原任务定位且拒绝设置循环和外链",()=>{
 expect(settingsReturnTarget({returnTo:"/chat/task?artifact=result"},"last")).toBe("/chat/task?artifact=result");
 for(const returnTo of ["https://example.com","//example.com","/settings?section=agents"]){expect(settingsReturnTarget({returnTo},"last")).toBe("/chat/last");}
 expect(settingsReturnTarget(null,null)).toBe("/chat");
});
