import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { FileBrowser } from "./FileBrowser";
import TextViewer from "./viewers/TextViewer";
import { findReadingMatches, folderBreadcrumbs, MAX_FIND_MATCHES, MAX_OUTLINE_HEADINGS, readingText, revealReadingMatch, textPage } from "./readingNavigation";
import type { FileEntry, PreviewDocument } from "./types";

const mocks = vi.hoisted(() => ({ list: vi.fn(), read: vi.fn() }));
vi.mock("../state/ConnectionContext", () => ({ useApi: () => ({ preview: mocks }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string, values?: { current?: number; count?: number }) => values ? `${key}:${values.current}/${values.count}` : key }) }));
let host: HTMLDivElement;
let root: Root;
let client: QueryClient;
const doc: PreviewDocument = { name: "guide.md", kind: "markdown", format: "markdown", size: 10, mime: "text/markdown", version: "1", text: "# Heading\nText" };
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mocks.list.mockReset(); mocks.read.mockReset();
});
afterEach(async () => { await act(async () => root.unmount()); client.clear(); host.remove(); window.getSelection()?.removeAllRanges(); });
async function showText(value: PreviewDocument, path = "guide.md") { await act(async () => root.render(<TextViewer document={value} path={path} agentId="a" />)); }
async function ready(check: () => void) { await vi.waitFor(async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); }); check(); }); }
async function openDetails(selector: string) {
  const details = host.querySelector<HTMLDetailsElement>(selector)!;
  await act(async () => { details.open = true; details.dispatchEvent(new Event("toggle")); });
}
async function type(selector: string, value: string) {
  const field = host.querySelector<HTMLInputElement>(selector)!;
  await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, value); field.dispatchEvent(new Event("input", { bubbles: true })); });
}
function button(label: string) { return [...host.querySelectorAll<HTMLButtonElement>("button")].find(item => item.textContent === label)!; }

describe("有界阅读导航", () => {
  it("目录关闭时不索引标题，打开后最多挂载两百个真实锚点并说明范围", async () => {
    await showText({ ...doc, text: "# x\n".repeat(15_000) });
    expect(host.querySelectorAll(".reading-text-body h1")).toHaveLength(15_000);
    expect(host.querySelectorAll(".reading-text-body h1[id]")).toHaveLength(0);
    expect(host.querySelectorAll(".reading-outline a")).toHaveLength(0);
    await openDetails(".reading-outline");
    expect(host.querySelectorAll(".reading-outline a")).toHaveLength(MAX_OUTLINE_HEADINGS);
    expect(host.querySelectorAll(".reading-text-body h1[id]")).toHaveLength(MAX_OUTLINE_HEADINGS);
    expect(host.textContent).toContain("readingNavigation.outlineLimited");
    const details = host.querySelector<HTMLDetailsElement>(".reading-outline")!;
    await act(async () => { details.open = false; details.dispatchEvent(new Event("toggle")); });
    expect(host.querySelectorAll(".reading-outline a")).toHaveLength(0);
  });

  it("没有标题时仍提供折叠目录入口并明确显示空状态", async () => {
    await showText({ ...doc, text: "A plain paragraph." });
    expect(host.querySelector(".reading-outline")).not.toBeNull();
    await openDetails(".reading-outline");
    expect(host.textContent).toContain("readingNavigation.outlineEmpty");
  });

  it("挂载、切页和切换文件不会清除其他区域的文本选择", async () => {
    const outside = document.createElement("p"); outside.textContent = "Selected conversation"; document.body.append(outside);
    const range = document.createRange(); range.selectNodeContents(outside);
    window.getSelection()?.addRange(range);
    try {
      await showText({ ...doc, text: "a".repeat(60_000) + "next" });
      expect(window.getSelection()?.toString()).toBe("Selected conversation");
      await act(async () => button("preview.next").click());
      expect(window.getSelection()?.toString()).toBe("Selected conversation");
      await showText({ ...doc, version: "2" }, "new.md");
      await openDetails(".reading-find-panel");
      expect(window.getSelection()?.toString()).toBe("Selected conversation");
    } finally { outside.remove(); }
  });

  it("按实际标题生成目录，跳过代码围栏并为重复标题生成不同锚点", async () => {
    await showText({ ...doc, text: "# **Repeat**\n\n```md\n# Hidden\n```\n\n## Repeat\n\n> # Quoted\n\n### Last" });
    expect(host.querySelectorAll(".reading-text-body h1,.reading-text-body h2,.reading-text-body h3")).toHaveLength(3);
    expect(host.querySelector(".reading-outline nav")).toBeNull();
    await openDetails(".reading-outline");
    const links = [...host.querySelectorAll<HTMLAnchorElement>(".reading-outline a")];
    expect(links.map(link => link.textContent)).toEqual(["Repeat", "Repeat", "Last"]);
    expect(new Set(links.map(link => link.hash)).size).toBe(3);
    const route = window.location.hash;
    await act(async () => links[1].click());
    expect(document.activeElement).toBe(host.querySelector("h2"));
    expect(window.location.hash).toBe(route);
    const firstIds = [...host.querySelectorAll(".reading-text-body [id]")].map(item => item.id);
    await act(async () => button("preview.source").click());
    expect(host.querySelector(".reading-outline")).toBeNull();
    await act(async () => button("preview.read").click());
    expect([...host.querySelectorAll(".reading-text-body [id]")].map(item => item.id)).toEqual(firstIds);
  });

  it("在渲染文字中查找，排除格式标记与复制按钮，并支持键盘上下跳转", async () => {
    await showText({ ...doc, text: "# Hello **世界**\n\nHello 世界" });
    await openDetails(".reading-find-panel");
    await type(".reading-find input", "HELLO 世界");
    expect(host.querySelector('[role="status"]')?.textContent).toBe("readingNavigation.matches:1/2");
    expect(window.getSelection()?.toString()).toBe("Hello 世界");
    const input = host.querySelector(".reading-find input")!;
    await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(host.querySelector('[role="status"]')?.textContent).toBe("readingNavigation.matches:2/2");
    await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true })));
    expect(host.querySelector('[role="status"]')?.textContent).toBe("readingNavigation.matches:1/2");
    await type(".reading-find input", "**世界**");
    expect(host.querySelector('[role="status"]')?.textContent).toBe("readingNavigation.matches:0/0");
    expect(window.getSelection()?.toString()).toBe("");
  });

  it("长文只查当前页，切页更新结果，新文件重置页码、查找与阅读模式", async () => {
    const long = { ...doc, text: "a".repeat(59_999) + "😀TARGET" };
    await showText(long);
    expect(host.querySelector(".message-markdown")).toBeNull();
    await openDetails(".reading-find-panel");
    await type(".reading-find input", "TARGET");
    expect(host.querySelector('[role="status"]')?.textContent).toBe("readingNavigation.matches:0/0");
    expect(host.querySelector("pre")?.textContent?.endsWith("\ud83d")).toBe(false);
    await act(async () => button("preview.next").click());
    expect(host.querySelector("pre")?.textContent).toBe("😀TARGET");
    expect(host.querySelector('[role="status"]')?.textContent).toBe("readingNavigation.matches:1/1");
    await showText({ ...doc, version: "2", text: "# New document" }, "other.md");
    expect(host.querySelector("h1")?.textContent).toBe("New document");
    expect(host.querySelector<HTMLInputElement>(".reading-find input")?.value).toBe("");
    expect(host.querySelector<HTMLDetailsElement>(".reading-find-panel")?.open).toBe(false);
  });

  it("Unicode大小写展开保持原文位置，符号按字面查找，命中与查询均有上限", () => {
    const content = "İ x😀x [a.*]";
    const unicode = findReadingMatches(content, "i").matches[0];
    expect(content.slice(unicode.start, unicode.end)).toBe("İ");
    const emoji = findReadingMatches(content, "😀").matches[0];
    expect(content.slice(emoji.start, emoji.end)).toBe("😀");
    expect(findReadingMatches(content, "[a.*]").matches).toHaveLength(1);
    expect(findReadingMatches("x".repeat(1000), "x")).toEqual({ matches: Array.from({ length: MAX_FIND_MATCHES }, (_, index) => ({ start: index, end: index + 1 })), truncated: true });
    expect(findReadingMatches("a".repeat(200), "a".repeat(201)).matches).toEqual([{ start: 0, end: 200 }]);
    expect(textPage("x", 99)).toMatchObject({ text: "x", page: 0, pages: 1 });
  });

  it("可读文字索引不包含工具按钮，不跨段落拼接词语", () => {
    const content = document.createElement("div");
    content.innerHTML = "<p>end</p><p>start</p><button>copy</button><pre><code>x😀</code></pre>";
    const snapshot = readingText(content);
    expect(snapshot.text).toBe("end\nstart\nx😀");
    expect(findReadingMatches(snapshot.text, "endstart").matches).toHaveLength(0);
    expect(findReadingMatches(snapshot.text, "copy").matches).toHaveLength(0);
  });

  it("查找命中补入的段落换行时范围仍落在真实文本节点内", () => {
    const content = document.createElement("div");
    content.innerHTML = "<p>end</p><p>start</p>"; host.append(content);
    const snapshot = readingText(content);
    for (const query of ["\nstart", "end\n", "\n"]) {
      const match = findReadingMatches(snapshot.text, query).matches[0];
      expect(() => revealReadingMatch(content, snapshot, match)).not.toThrow();
      expect(window.getSelection()?.rangeCount).toBe(1);
    }
  });
});

const entry = (name: string, path: string, is_dir = false): FileEntry => ({ name, path, is_dir, size: 1, mod_time: "2026-10-13T00:00:00Z" });
async function showFiles(agentId = "a") { await act(async () => root.render(<QueryClientProvider client={client}><FileBrowser agentId={agentId} onClose={() => undefined} /></QueryClientProvider>)); }
describe("当前目录导航", () => {
  it("本地筛选已加载的文件名，不递归请求；面包屑返回父级并清空筛选", async () => {
    mocks.list.mockImplementation(async (_agent: string, path: string) => ({ items: path === "" ? [entry("Notes", "Notes", true), entry("Root.txt", "Root.txt")] : path === "Notes" ? [entry("Drafts", "Notes/Drafts", true)] : [entry("Guide.md", "Notes/Drafts/Guide.md"), entry("Plan.txt", "Notes/Drafts/Plan.txt")], truncated: false }));
    await showFiles();
    await ready(() => expect(button("Notes")).toBeTruthy());
    await type(".reading-file-filter input", "ROOT");
    expect(host.querySelectorAll(".preview-entry")).toHaveLength(1);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    await type(".reading-file-filter input", "Notes");
    await act(async () => button("Notes").click());
    await ready(() => expect(button("Drafts")).toBeTruthy());
    expect(host.querySelector<HTMLInputElement>(".reading-file-filter input")?.value).toBe("");
    await act(async () => button("Drafts").click());
    await ready(() => expect(host.querySelectorAll(".preview-entry")).toHaveLength(2));
    const crumbs = [...host.querySelectorAll<HTMLButtonElement>(".reading-breadcrumbs button")];
    expect(crumbs.map(item => item.textContent)).toEqual(["readingNavigation.root", "Notes", "Drafts"]);
    expect(crumbs[2].getAttribute("aria-current")).toBe("location");
    await type(".reading-file-filter input", "missing");
    expect(host.textContent).toContain("readingNavigation.noFiles");
    const requests = mocks.list.mock.calls.length;
    await act(async () => crumbs[1].click());
    await ready(() => expect(button("Drafts")).toBeTruthy());
    expect(host.querySelector<HTMLInputElement>(".reading-file-filter input")?.value).toBe("");
    expect(mocks.list.mock.calls.length).toBeLessThanOrEqual(requests + 1);
    expect(folderBreadcrumbs("Notes/Drafts")).toEqual([{ name: "Notes", path: "Notes" }, { name: "Drafts", path: "Notes/Drafts" }]);
  });

  it("切换智能体重新从根目录读取并取消旧目录请求", async () => {
    mocks.list.mockImplementationOnce(() => new Promise(() => undefined)).mockResolvedValue({ items: [], truncated: false });
    await showFiles(); await ready(() => expect(mocks.list).toHaveBeenCalledOnce());
    const signal = mocks.list.mock.calls[0][2] as AbortSignal;
    await showFiles("b");
    expect(signal.aborted).toBe(true);
    await ready(() => expect(mocks.list).toHaveBeenLastCalledWith("b", "", expect.any(AbortSignal)));
    expect(host.textContent).toContain("readingNavigation.emptyFolder");
  });
});
