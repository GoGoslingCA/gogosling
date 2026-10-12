import { describe, expect, it } from "vitest";
import { DeepLinkGate } from "../src/main/deep-link.js";

describe("DeepLinkGate", () => {
  it("ready 之前到达的 open-url 被暂存，就绪后才处理（不会在 ready 前创建窗口）", () => {
    const handled: string[] = [];
    const gate = new DeepLinkGate((url) => handled.push(url));
    gate.offer("gogosling://open/a");
    expect(handled).toEqual([]);
    gate.markReady(null);
    expect(handled).toEqual(["gogosling://open/a"]);
    expect(gate.isReady).toBe(true);
  });

  it("ready 前多次到达只保留最近一个；暂存的优先于命令行里的深链", () => {
    const handled: string[] = [];
    const gate = new DeepLinkGate((url) => handled.push(url));
    gate.offer("gogosling://open/a");
    gate.offer("gogosling://open/b");
    gate.markReady("gogosling://argv");
    expect(handled).toEqual(["gogosling://open/b"]);
  });

  it("没有暂存时处理命令行里的深链；都没有时不处理", () => {
    const handled: string[] = [];
    const gate = new DeepLinkGate((url) => handled.push(url));
    gate.markReady("gogosling://argv");
    expect(handled).toEqual(["gogosling://argv"]);

    const empty: string[] = [];
    new DeepLinkGate((url) => empty.push(url)).markReady(null);
    expect(empty).toEqual([]);
  });

  it("就绪后立即处理；重复 markReady 不会重放", () => {
    const handled: string[] = [];
    const gate = new DeepLinkGate((url) => handled.push(url));
    gate.markReady(null);
    gate.offer("gogosling://x");
    gate.markReady("gogosling://argv");
    expect(handled).toEqual(["gogosling://x"]);
  });
});
