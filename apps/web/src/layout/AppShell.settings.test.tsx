import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AppShell } from "./AppShell";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("../state/ConnectionContext", () => ({ useConnection: () => ({ connection: {} }) }));
vi.mock("../state/EventStreamProvider", () => ({ EventStreamProvider: ({ children }: { children: ReactNode }) => children, useStreamStatus: () => ({ status: "open", attempt: 0, worker: null }) }));
vi.mock("../state/DraftProvider", () => ({ DraftProvider: ({ children }: { children: ReactNode }) => children }));
vi.mock("../state/useSettings", () => ({ useSettingsQuery: () => ({ data: { locale: "zh" } }) }));
vi.mock("../i18n", () => ({ applyLocale: async () => undefined }));
vi.mock("./NavigationSidebar", () => ({ NavigationSidebar: () => <aside className="global-navigation">navigation</aside> }));
vi.mock("./StatusBar", () => ({ StatusBar: () => <span data-testid="active-connection-details">connection checks active</span> }));
vi.mock("./ApprovalDialog", () => ({ ApprovalDialog: () => null }));
let root: Root; let host: HTMLDivElement;
function Page() { const location = useLocation(); return <output>{location.pathname}{location.search}</output>; }
beforeEach(() => { (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true; host = document.createElement("div"); document.body.append(host); root = createRoot(host); });
afterEach(async () => { await act(async () => root.unmount()); host.remove(); delete document.documentElement.dataset.appearance; });
async function mount() { await act(async () => root.render(<MemoryRouter initialEntries={["/settings"]}><Routes><Route element={<AppShell />}><Route path="settings" element={<Page />} /><Route path="chat" element={<Page />} /></Route></Routes></MemoryRouter>)); }
describe("设置页独立双栏", () => {
  it("只在设置页隐藏全局侧栏，品牌返回工作台后恢复原布局", async () => {
    await mount(); expect(host.querySelector(".app.settings-layout")).not.toBeNull(); expect(host.querySelector(".global-navigation")).toBeNull();
    await act(async () => host.querySelector<HTMLAnchorElement>(".brand")!.click());
    expect(host.querySelector("output")?.textContent).toBe("/chat"); expect(host.querySelector(".app.settings-layout")).toBeNull(); expect(host.querySelector(".global-navigation")).not.toBeNull();
  });
  it("设置页仍保留查找任务快捷键", async () => {
    await mount(); await act(async () => window.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true, cancelable: true })));
    expect(host.querySelector("output")?.textContent).toBe("/chat?history=1"); expect(host.querySelector(".global-navigation")).not.toBeNull();
  });
  it("连接详情仅展开时挂载，退出后不保留后台检查组件", async () => {
    await mount();
    expect(host.querySelector('[data-testid="active-connection-details"]')).toBeNull();
    const details = host.querySelector<HTMLDetailsElement>(".connection-details")!;
    await act(async () => { details.open = true; details.dispatchEvent(new Event("toggle")); });
    expect(host.querySelector('[data-testid="active-connection-details"]')).not.toBeNull();
    await act(async () => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", cancelable: true })));
    expect(details.open).toBe(false);
    expect(host.querySelector('[data-testid="active-connection-details"]')).toBeNull();
    expect(document.activeElement).toBe(details.querySelector("summary"));
  });
  it("键盘焦点离开连接详情时关闭且不抢回焦点", async () => {
    await mount();
    const details = host.querySelector<HTMLDetailsElement>(".connection-details")!;
    await act(async () => { details.open = true; details.dispatchEvent(new Event("toggle")); });
    await act(async () => details.querySelector("summary")!.focus());
    expect(details.open).toBe(true);
    const target = host.querySelector<HTMLAnchorElement>(".brand")!;
    await act(async () => target.focus());
    expect(details.open).toBe(false);
    expect(host.querySelector('[data-testid="active-connection-details"]')).toBeNull();
    expect(document.activeElement).toBe(target);
  });
  it("保留其他交互已处理的Escape而不关闭连接详情或移动焦点", async () => {
    await mount();
    const details = host.querySelector<HTMLDetailsElement>(".connection-details")!;
    await act(async () => { details.open = true; details.dispatchEvent(new Event("toggle")); });
    const content = host.querySelector<HTMLElement>('[data-testid="active-connection-details"]')!;
    content.tabIndex = 0;
    await act(async () => content.focus());
    content.addEventListener("keydown", event => event.preventDefault(), { once: true });
    await act(async () => content.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })));
    expect(details.open).toBe(true);
    expect(document.activeElement).toBe(content);
  });
});
