import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { FileBrowser, type FileBrowserLocation } from "./FileBrowser";
const list = vi.hoisted(() => vi.fn());
vi.mock("../state/ConnectionContext", () => ({ useApi: () => ({ preview: { list } }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
let host: HTMLDivElement; let root: Root; let client: QueryClient;
function Fixture() {
  const [location, setLocation] = useState<FileBrowserLocation>({ folder: "", filter: "" }); const [reading, setReading] = useState(false);
  return reading ? <button onClick={() => setReading(false)}>return</button> : <FileBrowser agentId="a" onClose={() => undefined} initialLocation={location} onLocationChange={setLocation} onOpenFile={() => setReading(true)} />;
}
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div"); document.body.append(host); root = createRoot(host); client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  list.mockReset(); list.mockImplementation(async (_agent: string, folder: string) => ({ items: folder ? [{ name: "notes.md", path: "docs/notes.md", is_dir: false, size: 10 }] : [{ name: "docs", path: "docs", is_dir: true, size: 0 }], truncated: false }));
});
afterEach(async () => { await act(async () => root.unmount()); client.clear(); host.remove(); });
async function ready(check: () => void) { await vi.waitFor(async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); }); check(); }); }
it("打开共享标签会卸载目录，返回时恢复原目录和筛选，不重新从根开始", async () => {
  await act(async () => root.render(<QueryClientProvider client={client}><Fixture /></QueryClientProvider>));
  await ready(() => expect(host.querySelector(".preview-entry")?.textContent).toContain("docs"));
  await act(async () => host.querySelector<HTMLButtonElement>(".preview-entry")!.click());
  await ready(() => expect(host.querySelector(".preview-entry")?.textContent).toContain("notes.md"));
  const filter = host.querySelector<HTMLInputElement>('input[type="search"]')!;
  await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(filter, "notes"); filter.dispatchEvent(new Event("input", { bubbles: true })); });
  await act(async () => host.querySelector<HTMLButtonElement>(".preview-entry")!.click()); expect(host.querySelector(".content-preview")).toBeNull();
  await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
  await ready(() => expect(host.querySelector(".preview-entry")?.textContent).toContain("notes.md")); expect(host.querySelector<HTMLInputElement>('input[type="search"]')?.value).toBe("notes"); expect(list.mock.calls.at(-1)?.slice(0, 2)).toEqual(["a", "docs"]);
});
