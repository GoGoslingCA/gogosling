import { defineConfig } from "vitest/config";

// 桌面壳的测试只覆盖纯逻辑（定位、句柄解析、退避、协议路径、CSP、PNG 编码、守护进程状态机），
// 不启动 Electron；凡是触碰 electron 模块的文件都不在测试里 import。
export default defineConfig({
  test: {
    environment: "node",
    include: ["test/**/*.test.ts"],
  },
});
