import { describe, expect, it, vi } from "vitest";
import { discoverConnection, hashCarriesToken, parseConnectionHash } from "./connection";

describe("parseConnectionHash", () => {
  it("解析 #endpoint=&token= 形式", () => {
    expect(parseConnectionHash("#endpoint=http%3A%2F%2F127.0.0.1%3A46750%2F&token=abc")).toEqual({ endpoint: "http://127.0.0.1:46750", token: "abc", source: "hash" });
  });

  it("解析 hash 路由形式 #/connect?endpoint=&token=", () => {
    expect(parseConnectionHash("#/connect?token=abc")).toEqual({ endpoint: "", token: "abc", source: "hash" });
  });

  it("没有 token 时返回 null", () => {
    expect(parseConnectionHash("#/chat")).toBeNull();
    expect(parseConnectionHash("#endpoint=x")).toBeNull();
    expect(parseConnectionHash("")).toBeNull();
    expect(hashCarriesToken("#/agents")).toBe(false);
  });
});

describe("discoverConnection", () => {
  it("优先使用桌面壳注入的连接", async () => {
    const conn = await discoverConnection({ bridge: { getConnection: async () => ({ endpoint: "127.0.0.1:1", token: "t" }) }, hash: "#token=other" });
    expect(conn).toEqual({ endpoint: "http://127.0.0.1:1", token: "t", source: "electron" });
  });

  it("桌面壳不可用时回落到 hash，并抹掉地址栏中的令牌", async () => {
    const scrub = vi.fn();
    const conn = await discoverConnection({
      bridge: {
        getConnection: async () => {
          throw new Error("not ready");
        },
      },
      hash: "#token=h",
      scrubHash: scrub,
    });
    expect(conn).toMatchObject({ token: "h", source: "hash" });
    expect(scrub).toHaveBeenCalledWith("#/");
  });

  it("桌面壳代理模式（mode: proxy）不带令牌也是有效连接，endpoint 为同源的 app://gogosling", async () => {
    const conn = await discoverConnection({
      bridge: { getConnection: async () => ({ endpoint: "app://gogosling", token: "", mode: "proxy" }) },
      hash: "#token=other",
    });
    expect(conn).toEqual({ endpoint: "app://gogosling", token: "", source: "electron" });
  });

  it("没有令牌且不是代理模式的壳连接无效，回落到 hash", async () => {
    const conn = await discoverConnection({
      bridge: { getConnection: async () => ({ endpoint: "http://127.0.0.1:1", token: "", mode: "direct" }) },
      hash: "#token=h",
      scrubHash: () => undefined,
    });
    expect(conn).toMatchObject({ token: "h", source: "hash" });
  });

  it("两者都没有时返回 null", async () => {
    expect(await discoverConnection({ bridge: undefined, hash: "" })).toBeNull();
  });
});
