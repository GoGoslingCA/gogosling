import { act, Component, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { asyncViewer } from "./asyncViewer";
import type { ViewerProps } from "./types";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
let host: HTMLDivElement; let root: Root;
const props: ViewerProps = { agentId: "test", path: "notes.md", document: { name: "notes.md", kind: "markdown", format: "markdown", size: 4, mime: "text/markdown", version: "test", text: "body" } };
beforeEach(() => { (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true; host = document.createElement("div"); document.body.append(host); root = createRoot(host); });
afterEach(async () => { await act(async () => root.unmount()); host.remove(); vi.restoreAllMocks(); });

it("只在打开格式时加载，完成后直接显示，重开只复用代码并使用新正文", async () => {
  let complete!: (module: { default: (input: ViewerProps) => ReactNode }) => void;
  const loader = vi.fn(() => new Promise<{ default: (input: ViewerProps) => ReactNode }>(resolve => { complete = resolve; }));
  const Viewer = asyncViewer(loader); expect(loader).not.toHaveBeenCalled();
  await act(async () => root.render(<Viewer {...props} />)); expect(host.textContent).toBe("common.loading");
  await act(async () => complete({ default: input => <p>{input.document.text}</p> })); expect(host.textContent).toBe("body");
  await act(async () => root.render(null));
  await act(async () => root.render(<Viewer {...props} document={{ ...props.document, text: "new body" }} />));
  expect(host.textContent).toBe("new body"); expect(loader).toHaveBeenCalledOnce();
});
it("卸载后的完成不会恢复旧文件，也不影响同时加载的另一格式", async () => {
  let complete!: (module: { default: () => ReactNode }) => void;
  const Old = asyncViewer(() => new Promise(resolve => { complete = resolve; }));
  const Current = asyncViewer(async () => ({ default: () => <p>current</p> }));
  await act(async () => root.render(<Old {...props} />));
  await act(async () => root.render(<Current {...props} />));
  await act(async () => complete({ default: () => <p>stale</p> })); expect(host.textContent).toBe("current");
});
it("一个格式模块加载失败进入局部错误边界，不妨碍下一格式", async () => {
  class Boundary extends Component<{ children: ReactNode }, { failed: boolean }> {
    override state = { failed: false };
    static getDerivedStateFromError() { return { failed: true }; }
    override render() { return this.state.failed ? <p>failed</p> : this.props.children; }
  }
  vi.spyOn(console, "error").mockImplementation(() => undefined);
  const Failed = asyncViewer(async () => { throw new Error("synthetic chunk failure"); });
  const Good = asyncViewer(async () => ({ default: () => <p>ready</p> }));
  await act(async () => root.render(<Boundary key="broken"><Failed {...props} /></Boundary>)); expect(host.textContent).toBe("failed");
  await act(async () => root.render(<Boundary key="good"><Good {...props} /></Boundary>)); expect(host.textContent).toBe("ready");
});
