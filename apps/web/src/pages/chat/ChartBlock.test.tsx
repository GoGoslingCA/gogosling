import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { parseChart } from "./ChartBlock";
import { Markdown } from "./Markdown";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

function render(source: string) {
  const host = document.createElement("div");
  host.innerHTML = renderToStaticMarkup(<Markdown text={`\`\`\`chart\n${source}\n\`\`\``} />);
  return host;
}
function number(element: Element, attribute: string) { return Number(element.getAttribute(attribute)); }

describe("本地安全图表", () => {
  it("正负柱形以零为基线且高度比例准确，零值不伪造高度", () => {
    const source = JSON.stringify({ type: "bar", title: "变化", unit: "项", data: [{ label: "减少", value: -10 }, { label: "不变", value: 0 }, { label: "增加", value: 20 }] });
    const host = render(source);
    const bars = host.querySelectorAll(".chart-bar");
    const zero = number(host.querySelector(".chart-baseline")!, "y1");
    expect(bars).toHaveLength(3);
    expect(number(bars[0]!, "y")).toBeCloseTo(zero);
    expect(number(bars[2]!, "y") + number(bars[2]!, "height")).toBeCloseTo(zero);
    expect(number(bars[2]!, "height") / number(bars[0]!, "height")).toBeCloseTo(2);
    expect(number(bars[1]!, "height")).toBe(0);
    expect(host.querySelectorAll('tbody th[scope="row"]')).toHaveLength(3);
    expect([...host.querySelectorAll("tbody td")].map((cell) => cell.textContent)).toEqual(["-10", "0", "20"]);
    expect(host.querySelector(".chart-source pre code")?.textContent).toBe(source);
  });

  it("折线保留原始数据顺序与点数，表格保留精确小数", () => {
    const host = render(JSON.stringify({ type: "line", data: [{ label: "第一段", value: -2 }, { label: "第二段", value: 4.125 }] }));
    const points = host.querySelectorAll(".chart-point");
    expect(points).toHaveLength(2);
    expect(number(points[0]!, "cx")).toBeLessThan(number(points[1]!, "cx"));
    expect(number(points[0]!, "cy")).toBeGreaterThan(number(points[1]!, "cy"));
    expect(host.querySelector(".chart-line")?.getAttribute("points")?.split(" ")).toHaveLength(2);
    expect([...host.querySelectorAll("tbody td")].map((cell) => cell.textContent)).toEqual(["-2", "4.125"]);
    expect(host.querySelector("svg")?.getAttribute("role")).toBe("img");
    expect(host.querySelector("svg desc")?.textContent).toBe("chart.description");
  });

  it("全部为零及极大有限数值都不会生成 NaN 或 Infinity 坐标", () => {
    for (const values of [[0, 0], [-1e308, 1e308], [-1e-300, 1e-300]]) {
      const host = render(JSON.stringify({ type: "bar", data: values.map((value, index) => ({ label: String(index), value })) }));
      for (const bar of host.querySelectorAll(".chart-bar")) for (const attribute of ["x", "y", "width", "height"]) expect(Number.isFinite(number(bar, attribute))).toBe(true);
      expect(host.querySelectorAll(".chart-bar")).toHaveLength(values.length);
    }
  });

  it.each([null, "", "10", true])("空值或非数字 %j 不会被转换为零，完整原 JSON 仍可阅读", (value) => {
    const source = JSON.stringify({ type: "bar", data: [{ label: "结果", value }] });
    const host = render(source);
    expect(host.querySelector(".chart-block")).toBeNull();
    expect(host.querySelector(".message-code pre code")?.textContent).toBe(source);
  });

  it("空数组、超出50点、不支持的类型与非有限值都返回原代码", () => {
    expect(parseChart(JSON.stringify({ type: "bar", data: Array.from({ length: 50 }, (_, index) => ({ label: String(index), value: index })) }))).not.toBeNull();
    for (const source of [
      JSON.stringify({ type: "line", data: [] }),
      JSON.stringify({ type: "bar", data: Array.from({ length: 51 }, (_, index) => ({ label: String(index), value: index })) }),
      JSON.stringify({ type: "pie", data: [{ label: "结果", value: 1 }] }),
      '{"type":"bar","data":[{"label":"结果","value":1e309}]}',
    ]) {
      const host = render(source);
      expect(host.querySelector(".chart-block")).toBeNull();
      expect(host.querySelector("pre code")?.textContent).toBe(source);
    }
  });

  it("图题和标签中的 HTML/URL 仅作为文本，不产生脚本、链接或外部资源", () => {
    const label = '<img src="https://example.test/pixel" onerror="alert(1)">';
    const source = JSON.stringify({ type: "bar", title: "<script>alert(1)</script>", unit: "<iframe>", data: [{ label, value: 5 }] });
    const host = render(source);
    expect(host.querySelector(".chart-block")).not.toBeNull();
    expect(host.querySelector("script,img,iframe,a,foreignObject")).toBeNull();
    expect(host.querySelector('tbody th[scope="row"]')?.textContent).toBe(label);
    expect(host.querySelector("figcaption")?.textContent).toContain("<script>alert(1)</script>");
  });

  it("未完成或不合法的chart围栏保留原文，后续普通正文不丢失", () => {
    const source = '{"type":"bar","data":[';
    const host = render(source);
    expect(host.querySelector("pre code")?.textContent).toBe(source);
    const full = document.createElement("div");
    full.innerHTML = renderToStaticMarkup(<Markdown text={`\`\`\`chart\n${source}\n\`\`\`\n后续说明`} />);
    expect(full.querySelector("p")?.textContent).toBe("后续说明");
  });
});
