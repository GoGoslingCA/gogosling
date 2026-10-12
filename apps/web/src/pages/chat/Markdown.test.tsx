import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { Markdown, safeMarkdownUrl } from "./Markdown";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

function render(text: string) {
  const wrapper = document.createElement("div");
  wrapper.innerHTML = renderToStaticMarkup(<Markdown text={text} />);
  return wrapper;
}

describe("安全 Markdown", () => {
  it("HTML 和事件属性只能作为文本显示，危险协议不能变成链接", () => {
    const malicious = '<img src=x onerror="alert(1)"><script>alert(1)</script>\n[click](javascript:alert) [data](data:text/html,evil)';
    const result = render(malicious);
    expect(result.querySelector("img,script,a")).toBeNull();
    expect(result.textContent).toContain('<img src=x onerror="alert(1)">');
    expect(result.textContent).toContain("[click](javascript:alert)");
    expect(safeMarkdownUrl("java\tscript:alert(1)")).toBeNull();
    expect(safeMarkdownUrl("//example.com")).toBeNull();
  });

  it("可信协议的 Markdown 链接含新窗口隔离，图片不会触发外部加载", () => {
    const result = render("[Docs](https://example.com/help) ![hidden](https://example.com/image.png)");
    expect(result.querySelector("a")?.getAttribute("href")).toBe("https://example.com/help");
    expect(result.querySelector("a")?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(result.querySelector("img")).toBeNull();
    expect(safeMarkdownUrl("mailto:hello@example.com")).toBe("mailto:hello@example.com");
  });

  it("代码块保留代码和 HTML 字面内容，关闭围栏后正常渲染后续段落", () => {
    const result = render("```html\n<img onerror=evil>\nconst n = 1;\n```\nAfter **bold** and `code`.");
    expect(result.querySelector("pre code")?.textContent).toBe("<img onerror=evil>\nconst n = 1;");
    expect(result.querySelector("img")).toBeNull();
    expect(result.querySelector("strong")?.textContent).toBe("bold");
    expect(result.querySelector("p")?.textContent).toContain("After bold and code.");
    expect(result.querySelector("button")?.textContent).toBe("chat.copyCode");
  });

  it("流式未闭合围栏、标题、列表、引用和表格可读取", () => {
    const result = render("# Title\n\n- One\n- Two\n\n> quoted\n\n| A | B |\n| --- | --- |\n| 1 | **two** |\n\n~~~python\nprint('stream')");
    expect(result.querySelector("h1")?.textContent).toBe("Title");
    expect(result.querySelectorAll("li")).toHaveLength(2);
    expect(result.querySelector("blockquote")?.textContent).toBe("quoted");
    expect(result.querySelectorAll("th")).toHaveLength(2);
    expect(result.querySelectorAll("td")).toHaveLength(2);
    expect(result.querySelector("pre code")?.textContent).toBe("print('stream')");
  });

  it("空列表条目能结束解析而不会挂起", () => {
    expect(render("- \n\n1. \n\nDone").querySelectorAll("li")).toHaveLength(2);
  });
});
