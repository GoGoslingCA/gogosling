# @gogosling/desktop

Electron 桌面壳。壳只拥有操作系统能力（拉起/发现守护进程、窗口、托盘、开机自启动、深链），
不拥有任何业务状态；界面是 `apps/web` 的同一份产物，通过本地 HTTP API 与 `gogoslingd` 通信。
打包后页面只访问同源的 `app://gogosling/api/...`，由主进程代理到守护进程并注入令牌（令牌不进页面）；
开发模式（`GOGOSLING_WEB_URL`）页面直连守护进程。

## 结构

```
src/main/        主进程（Node，CommonJS）
  main.ts          入口：单实例锁、窗口、app:// 协议、CSP、托盘、自启动、深链、IPC
  api-proxy.ts     app://gogosling/api/* → 守护进程的同源代理（注入令牌、流式响应）
  autostart.ts     开机自启动的平台差异（Windows args 读写一致、macOS 登录启动隐藏）
  deep-link.ts     ready 之前到达的深链暂存
  daemon.ts        DaemonManager：发现/拉起/监管 gogoslingd（I/O 全部可注入）
  daemon-logic.ts  纯逻辑：数据目录、可执行文件定位、daemon.json 解析、退避与重启决策
  daemon-stop.ts   停止流程：身份确认 → POST /api/v1/system/shutdown → 回退信号（副作用注入）
  app-protocol.ts  app://gogosling → apps/web/dist 的安全路径映射
  csp.ts           Content-Security-Policy
  shell-security.ts IPC 发送方校验、外链白名单、深链解析、开发地址解析
  tray.ts          托盘菜单模型
  i18n.ts          壳自身的 zh/en/fr 文案
  png.ts           运行时生成托盘图标（无需提交二进制）
  log.ts           脱敏日志（<dataDir>/logs/desktop.log，0600）
src/preload/     预加载脚本（沙箱，单文件）：window.gogosling 四个函数
src/shared/      主进程与 preload 共享的 IPC 契约（preload 只 import type）
test/            vitest（不启动 Electron）
```

## 运行

```bash
npm install
npm run build -w @gogosling/web          # 生成 apps/web/dist（app:// 协议从这里取文件）
npm run build -w @gogosling/desktop
npm run start -w @gogosling/desktop
```

环境变量：

| 变量 | 作用 |
|---|---|
| `GOGOSLING_HOME` | 数据目录（默认 `~/.gogosling`），壳与守护进程共用 |
| `GOGOSLING_DAEMON_PATH` | 守护进程可执行文件；未设置时依次找 `<resources>/bin/gogoslingd[.exe]` 与 `PATH` |
| `GOGOSLING_RESOURCES` | 壳拉起随包分发的守护进程时自动设为 `<resources>/share/gogosling`（内置扩展在其下 `extensions/bundled`）；已设置时不覆盖 |
| `GOGOSLING_WEB_URL` | 开发态指向 vite dev server（只接受回环地址），页面直连守护进程，CSP 相应放宽 |

守护进程的发现规则：先读 `<dataDir>/daemon.json`，`GET /healthz` 为 200 且 `GET /api/v1/version` 接受其中的令牌，
就复用它（退出时不终止）；否则由壳拉起。子进程崩溃按指数退避重启（500ms 起、15s 封顶、最多 5 次；稳定运行 60s 后重新计数）。

停止守护进程（应用退出与托盘"重启守护进程"走同一条路径，见 `daemon-stop.ts`）：

1. **壳拉起的实例**：先确认 daemon.json 的身份——句柄里的 pid 是子进程的 pid，且 `GET /api/v1/version` 用其中的令牌
   返回 200、`started_at` 与句柄一致；确认后 `POST /api/v1/system/shutdown`，返回 202 就等它自己退出（最多 30s，
   守护进程会自己删除 daemon.json），超时直接强制结束（SIGKILL / `taskkill /T /F`）。
2. 身份未确认、关闭端点连不上或不是 202 时才回退到信号：POSIX `SIGTERM`，30s 内不退出再 `SIGKILL`；Windows 上
   无窗口的控制台进程收不到温和信号，直接 `taskkill /PID <pid> /T /F`，不再先白等一轮。信号只发给壳自己 spawn 的子进程。
3. 子进程确认退出后，仍写着它 pid 的 daemon.json 是残留（被强制结束时来不及删），由壳删除，令牌不留在磁盘上。
4. **不是壳拉起的实例**（复用的，或只剩 daemon.json）：只确认身份。确认通过就断开连接、让它继续运行；令牌被拒、
   `started_at` 不一致或端点连不上时，把 daemon.json 当残留文件删除，**不请求关闭、不发任何信号**；超时无法判断时保留原样。

## 渲染进程可用的接口

```ts
// 打包后：{ endpoint: "app://gogosling", token: "", mode: "proxy" }；开发模式：{ endpoint, token, mode: "direct" }
window.gogosling.getConnection(): Promise<{ endpoint: string; token: string; mode: "proxy" | "direct" } | null>
window.gogosling.onDaemonStatus(cb: (status: DaemonStatus) => void): () => void
window.gogosling.openExternal(url: string): Promise<void>   // 仅 http / https / mailto
window.gogosling.getVersion(): Promise<ShellVersion>
```

类型见 `src/shared/ipc.ts`。

## 验证

```bash
npm run typecheck -w @gogosling/desktop
npm run test -w @gogosling/desktop
npm run build -w @gogosling/desktop
npm run package -w @gogosling/desktop     # electron-builder，不签名；需要先构建 web
```
