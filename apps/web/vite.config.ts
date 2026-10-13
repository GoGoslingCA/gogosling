/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { pdfAssets } from "./pdf-assets.ts";
import { buildLicenseIndex } from "./build-license-index.ts";
import { readFileSync } from "node:fs";

// 守护进程默认端口（见 api/openapi/openapi.yaml 的 servers）。
const DAEMON_ORIGIN = "http://127.0.0.1:46750";
const releaseVersion = (JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf8")) as { version: string }).version;

export default defineConfig({
  define: { __GOGOSLING_RELEASE__: JSON.stringify(releaseVersion) },
  // 相对路径 base：构建产物既能从 http 服务器加载，也能被 file:// 与桌面壳的 app:// 协议直接加载。
  base: "./",
  plugins: [react(), pdfAssets(), buildLicenseIndex()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    host: "127.0.0.1",
    port: 5173,
    strictPort: false,
    proxy: {
      // 开发时把 /api 转发到本地守护进程；SSE 走同一条代理（http-proxy 不缓冲流式响应）。
      "/api": {
        target: DAEMON_ORIGIN,
        changeOrigin: false,
      },
      "/healthz": {
        target: DAEMON_ORIGIN,
        changeOrigin: false,
      },
    },
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    css: false,
  },
});
