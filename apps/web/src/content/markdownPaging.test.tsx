import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { Markdown, prepareMarkdownPreview } from "../pages/chat/Markdown";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

describe("Markdown阅读分页", () => {
  it("有序列表继续原有编号且完整覆盖所有条目", () => {
    const source = Array.from({ length: 501 }, (_, index) => `${index + 7}. item-${index}`).join("\n");
    const prepared = prepareMarkdownPreview(source);
    const rendered = Array.from({ length: prepared.pageCount }, (_, page) => renderToStaticMarkup(prepared.renderPage(page)));
    const wrapper = document.createElement("div"); wrapper.innerHTML = rendered.join("");
    expect([...wrapper.querySelectorAll("li")].map(item => item.textContent)).toEqual(Array.from({ length: 501 }, (_, index) => `item-${index}`));
    expect([...wrapper.querySelectorAll("ol")].map(item => item.start)).toEqual([7, 107, 207, 307, 407, 507]);
    expect(prepared.pageCount).toBeGreaterThan(1);
    const whole = document.createElement("div"); whole.innerHTML = renderToStaticMarkup(<Markdown text={source}/>);
    expect(whole.querySelectorAll("ol")).toHaveLength(1);
    expect(whole.querySelectorAll("li")).toHaveLength(501);
  });

  it("极宽表格不丢列，围栏不被当作标题或表格拆分", () => {
    const header = Array.from({ length: 120 }, (_, column) => `Heading ${column}`);
    const row = Array.from({ length: 120 }, (_, column) => `value-${column}`);
    const prepared = prepareMarkdownPreview([header.join("|"), header.map(() => "---").join("|"), row.join("|"), "", "```md", "# not a heading", "| not a table |", "```"].join("\n"));
    const wrapper = document.createElement("div"); wrapper.innerHTML = Array.from({ length: prepared.pageCount }, (_, page) => renderToStaticMarkup(prepared.renderPage(page))).join("");
    expect([...wrapper.querySelectorAll("td")].map(cell => cell.textContent)).toEqual(row);
    expect([...wrapper.querySelectorAll("th")].map(cell => cell.textContent)).toEqual(header);
    expect(wrapper.querySelector("pre code")?.textContent).toBe("# not a heading\n| not a table |");
    expect(wrapper.querySelector("h1")).toBeNull();
  });

  it("超过六万字的段落和围栏保留格式与全部字符且每页文字有界", () => {
    const content = "a".repeat(19_999) + "😀" + "b".repeat(80_000);
    for (const [source, selector] of [[`**${content}**`, "strong"], ["```text\n" + content + "\n```", "pre code"]]) {
      const prepared = prepareMarkdownPreview(source);
      const all: string[] = [];
      for (let page = 0; page < prepared.pageCount; page++) {
        const wrapper = document.createElement("div"); wrapper.innerHTML = renderToStaticMarkup(prepared.renderPage(page));
        const shown = wrapper.querySelector(selector)?.textContent ?? "";
        expect(shown.length).toBeLessThanOrEqual(20_000);
        expect(shown.endsWith("\ud83d")).toBe(false);
        all.push(shown);
      }
      expect(all.join("")).toBe(content);
      expect(prepared.pageCount).toBeGreaterThan(5);
    }
  });

  it("每页累计标题字符也有上限，不将多个八千字标题一次布局", () => {
    const heading = "h".repeat(8000);
    const prepared = prepareMarkdownPreview(Array.from({ length: 250 }, () => `# ${heading}`).join("\n"));
    expect(prepared.sourceRequired).toBe(false);
    expect(prepared.pageCount).toBe(125);
    for (const page of [0, 124]) {
      const wrapper = document.createElement("div"); wrapper.innerHTML = renderToStaticMarkup(prepared.renderPage(page));
      expect(wrapper.querySelectorAll("h1")).toHaveLength(2);
      expect(wrapper.textContent?.length).toBe(16_000);
    }
  });

  it("超长标题列表项单元格表头及过密行内格式要求明确原文回退", () => {
    const long = "x".repeat(2_000_000);
    const sources = [
      `# ${long}`,
      `- ${long}`,
      `| Heading | Other |\n| --- | --- |\n| ${long} | value |`,
      `| ${long} | Other |\n| --- | --- |\n| value | value |`,
      "**a** ".repeat(1000),
      "[a](https://example.test) ".repeat(700),
      `[label](https://example.test/${long})`,
    ];
    for (const source of sources) {
      const prepared = prepareMarkdownPreview(source);
      expect(prepared.sourceRequired).toBe(true);
      expect(prepared.renderPage(0)).toBeNull();
    }
  });

  it("多个可分开的格式列表项和表格行继续阅读而不是过早退回原文", () => {
    const list = Array.from({ length: 100 }, (_, index) => `- **${index}:** ` + "x".repeat(1000)).join("\n");
    const table = "| **Item** | **Result** |\n| --- | --- |\n" + Array.from({ length: 300 }, (_, index) => `| **${index}** | **checked** |`).join("\n");
    for (const text of [list, table]) {
      const prepared = prepareMarkdownPreview(text);
      expect(prepared.sourceRequired).toBe(false);
      expect(prepared.pageCount).toBeGreaterThan(1);
      for (let page = 0; page < prepared.pageCount; page++) {
        const wrapper = document.createElement("div"); wrapper.innerHTML = renderToStaticMarkup(prepared.renderPage(page));
        expect(wrapper.textContent!.length).toBeLessThanOrEqual(20_000);
        expect(wrapper.querySelectorAll("*").length).toBeLessThanOrEqual(510);
      }
    }
  });
});
