// Electron 主进程入口：单实例锁、窗口、app:// 协议（静态页面 + /api/ 代理）、CSP、托盘、自启动、深链、IPC。
//
// 壳只拥有操作系统能力，不拥有业务状态：所有数据都在 gogoslingd。打包后渲染进程只访问同源的
// app://gogosling/api/...，由这里的协议处理器代理到守护进程并注入令牌（api-proxy.ts），令牌不进页面；
// 开发模式（GOGOSLING_WEB_URL）页面直连守护进程，{endpoint, token} 经 IPC 交给页面。

import { app, BrowserWindow, dialog, ipcMain, Menu, nativeImage, net, protocol, screen, session, shell, Tray, type IpcMainEvent, type IpcMainInvokeEvent } from "electron";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { IPC, type DaemonStatus, type ShellVersion } from "../shared/ipc.js";
import { proxyApiRequest, rendererConnection } from "./api-proxy.js";
import { APP_ORIGIN, APP_SCHEME, contentTypeFor, fallbackToIndex, INDEX_FILE, resolveAppFile, resolveWebRoot } from "./app-protocol.js";
import { autostartChecked, loginItemQuery, loginItemWrite, startHiddenAtLaunch, type LoginItemState } from "./autostart.js";
import { buildCsp } from "./csp.js";
import { DaemonManager, canRestart } from "./daemon.js";
import { resolveDataDir } from "./daemon-logic.js";
import { DeepLinkGate } from "./deep-link.js";
import { DesktopDiagnostics } from "./diagnostics.js";
import { installDiagnosticsIpc } from "./diagnostics-ipc.js";
import { releaseDiagnosticsEnabled } from "./diagnostics-policy.js";
import { parentedDiagnosticDialog, reviewNativeDiagnostics } from "./diagnostics-native.js";
import { pickLocale, t, type ShellLocale } from "./i18n.js";
import { createFileLogger, type Logger } from "./log.js";
import { trayIconPng } from "./png.js";
import { QuitFlow } from "./quit-flow.js";
import {
  DEEP_LINK_SCHEME,
  ENV_WEB_URL,
  describeDeepLink,
  findDeepLink,
  isTrustedPageUrl,
  parseDevWebUrl,
  sanitizeExternalUrl,
  type DevWebTarget,
} from "./shell-security.js";
import { autostartSupported, buildTrayMenuTemplate, trayTooltip } from "./tray.js";
import { createWindowStateSaver, readWindowState, restoreWindowState, WINDOW_MIN_HEIGHT, WINDOW_MIN_WIDTH, writeWindowState, type WindowBounds } from "./window-state.js";

/** 渲染进程允许的权限请求；其余（摄像头、通知、地理位置…）一律拒绝。 */
const ALLOWED_PERMISSIONS: ReadonlySet<string> = new Set(["clipboard-sanitized-write"]);

interface ShellContext {
  daemon: DaemonManager;
  diagnostics: DesktopDiagnostics;
  logger: Logger;
  /** 壳日志文件路径，用于在错误对话框里提示用户。 */
  logPath: string;
  dataDir: string;
  webRoot: string;
  dev: DevWebTarget | null;
  platform: NodeJS.Platform;
}

/** 壳的运行时对象：窗口、托盘、IPC 与退出流程。只在 bootstrap 里创建一个实例。 */
class DesktopShell {
  private window: BrowserWindow | null = null;
  private tray: Tray | null = null;
  private locale: ShellLocale = "zh";
  private initialized = false;
  private startHidden = false;
  private readonly quitFlow: QuitFlow;
  private lastReadyToken: string | null = null;
  private failureDialogOpen = false;
  private windowStateSaver: ReturnType<typeof createWindowStateSaver> | null = null;
  private readonly csp: string;

  constructor(private readonly ctx: ShellContext) {
    this.csp = buildCsp({ devOrigin: ctx.dev?.origin ?? null });
    this.quitFlow = new QuitFlow({
      stopDaemon: () => ctx.daemon.stop(),
      quit: () => app.quit(),
      onStopError: (err) => ctx.logger.error("停止守护进程失败", { err }),
    });
  }

  private get quitting(): boolean {
    return this.quitFlow.isQuitting;
  }

  /** app ready 之后调用。 */
  init(): void {
    this.locale = pickLocale(app.getLocale());
    this.startHidden = startHiddenAtLaunch(process.argv, this.ctx.platform, this.readLoginItem());
    this.installProtocol();
    this.installSessionPolicy();
    this.installIpc();
    this.createTray();
    this.createWindow();
    screen.on("display-removed", () => this.ensureWindowVisible());
    screen.on("display-metrics-changed", () => this.ensureWindowVisible());
    this.ctx.daemon.onStatus((status) => this.onDaemonStatus(status));
    this.initialized = true;
  }

  showWindow(): void {
    // 防御：ready 之前（macOS 冷启动的 open-url / activate）不能创建 BrowserWindow。
    if (!this.initialized || !app.isReady() || this.quitting) return;
    if (!this.window) {
      this.createWindow();
      return;
    }
    if (this.window.isMinimized()) this.window.restore();
    this.ensureWindowVisible();
    this.window.show();
    this.window.focus();
  }

  handleDeepLink(url: string): void {
    this.ctx.logger.info("收到深链", { url: describeDeepLink(url) });
    if (!this.initialized || !app.isReady()) {
      this.ctx.logger.warn("壳尚未就绪，忽略深链", { url: describeDeepLink(url) });
      return;
    }
    this.showWindow();
  }

  /** before-quit：先让页面处理未保存内容，服务继续运行到窗口真正关闭。 */
  handleBeforeQuit(): void {
    this.windowStateSaver?.flush();
    this.quitFlow.beforeQuit();
  }

  /** 所有窗口均已完成卸载后，再停止由本壳启动的守护进程。 */
  handleWillQuit(event: { preventDefault(): void }): void {
    this.ctx.diagnostics.dispose();
    this.quitFlow.willQuit(event);
  }

  // ---- 协议与会话策略 ------------------------------------------------------

  private installProtocol(): void {
    const { webRoot, logger, daemon } = this.ctx;
    protocol.handle(APP_SCHEME, async (request) => {
      // app://gogosling/api/* → 守护进程（注入令牌、流式响应）；其余路径是静态页面。
      const proxied = await proxyApiRequest(request, {
        connection: () => daemon.getConnection(),
        fetch: async (url, init) => {
          const response = await net.fetch(url, init);
          if (response.status >= 400) this.ctx.diagnostics.record(`http_${response.status}`);
          return response;
        },
        onError: ({ path: apiPath, err }) => {
          this.ctx.diagnostics.record("api_proxy_failed");
          logger.warn("代理到守护进程失败", { path: apiPath, err });
        },
      });
      if (proxied) return proxied;

      const file = resolveAppFile(request.url, webRoot);
      if (!file) return textResponse(404, "not found");
      let target = file;
      if (fallbackToIndex(file) && !(await exists(file))) target = path.join(webRoot, INDEX_FILE);
      try {
        const data = await fs.promises.readFile(target);
        return new Response(data, {
          status: 200,
          headers: {
            "content-type": contentTypeFor(target),
            "content-security-policy": this.csp,
            "x-content-type-options": "nosniff",
            "cache-control": "no-cache",
          },
        });
      } catch (err) {
        const code = (err as NodeJS.ErrnoException).code;
        if (code === "ENOENT" || code === "EISDIR") return textResponse(404, "not found");
        this.ctx.diagnostics.record("static_resource_failed");
        logger.error("读取静态资源失败", { file: target, err });
        return textResponse(500, "internal error");
      }
    });
  }

  private installSessionPolicy(): void {
    const ses = session.defaultSession;
    const devOrigin = this.ctx.dev?.origin ?? null;

    // 开发服务器的响应没有 CSP；统一在这里补上（app:// 的响应已在协议处理器里带了同样的头）。
    ses.webRequest.onHeadersReceived((details, callback) => {
      const isDocument = details.resourceType === "mainFrame" || details.resourceType === "subFrame";
      if (!isDocument || !isTrustedPageUrl(details.url, { devOrigin })) {
        callback({});
        return;
      }
      const headers: Record<string, string | string[]> = {};
      for (const [key, value] of Object.entries(details.responseHeaders ?? {})) {
        if (key.toLowerCase() !== "content-security-policy") headers[key] = value;
      }
      headers["Content-Security-Policy"] = [this.csp];
      callback({ responseHeaders: headers });
    });

    ses.setPermissionRequestHandler((_contents, permission, callback) => callback(ALLOWED_PERMISSIONS.has(permission)));
    ses.setPermissionCheckHandler((_contents, permission) => ALLOWED_PERMISSIONS.has(permission));
  }

  // ---- IPC ------------------------------------------------------------------

  private installIpc(): void {
    const { daemon } = this.ctx;
    installDiagnosticsIpc<IpcMainInvokeEvent>({
      handle: (channel, listener) => ipcMain.handle(channel, listener),
      assertTrusted: event => this.assertTrusted(event),
      diagnostics: this.ctx.diagnostics,
    });
    ipcMain.handle(IPC.getConnection, (event) => {
      this.assertTrusted(event);
      // 打包后只给同源代理地址，不给令牌；开发模式保持直连。
      return rendererConnection(daemon.getConnection(), { dev: this.ctx.dev !== null });
    });
    ipcMain.handle(IPC.getVersion, (event): ShellVersion => {
      this.assertTrusted(event);
      return {
        app: app.getVersion(),
        electron: process.versions.electron ?? "",
        chromium: process.versions.chrome ?? "",
        node: process.versions.node,
        platform: process.platform,
        arch: process.arch,
      };
    });
    ipcMain.handle(IPC.openExternal, async (event, raw: unknown) => {
      this.assertTrusted(event);
      const url = sanitizeExternalUrl(raw);
      if (!url) throw new Error("url not allowed");
      await shell.openExternal(url);
    });
    ipcMain.on(IPC.daemonStatusSubscribe, (event) => {
      if (!this.isTrusted(event)) return;
      event.sender.send(IPC.daemonStatus, daemon.getStatus());
    });
  }

  /** 只接受主窗口顶层框架、且页面来自 app://gogosling 或开发服务器的消息。 */
  private isTrusted(event: IpcMainInvokeEvent | IpcMainEvent): boolean {
    const frame = event.senderFrame;
    if (!frame) return false;
    if (!this.window || event.sender.id !== this.window.webContents.id) return false;
    // 只接受顶层框架：iframe（即便同源）不能索取令牌。
    if (frame.parent !== null) return false;
    return isTrustedPageUrl(frame.url, { devOrigin: this.ctx.dev?.origin ?? null });
  }

  private assertTrusted(event: IpcMainInvokeEvent | IpcMainEvent): void {
    if (!this.isTrusted(event)) {
      this.ctx.logger.warn("拒绝来自不可信发送方的 IPC", { url: event.senderFrame?.url ?? "" });
      throw new Error("untrusted sender");
    }
  }

  // ---- 窗口 -----------------------------------------------------------------

  private displayWorkAreas(): WindowBounds[] {
    const primary = screen.getPrimaryDisplay();
    return [primary.workArea, ...screen.getAllDisplays().filter((display) => display.id !== primary.id).map((display) => display.workArea)];
  }

  private ensureWindowVisible(): void {
    const win = this.window;
    if (!win || win.isDestroyed() || win.isMinimized() || win.isMaximized() || win.isFullScreen()) return;
    const bounds = win.getNormalBounds();
    const restored = restoreWindowState(bounds, this.displayWorkAreas());
    if (bounds.x !== restored.x || bounds.y !== restored.y || bounds.width !== restored.width || bounds.height !== restored.height) {
      win.setBounds({ x: restored.x, y: restored.y, width: restored.width, height: restored.height });
    }
  }

  private createWindow(): void {
    const devOrigin = this.ctx.dev?.origin ?? null;
    // 界面偏好归 Electron userData，绝不写入守护进程数据目录。
    const statePath = path.join(app.getPath("userData"), "ui", "window-state.json");
    const state = readWindowState(statePath, this.displayWorkAreas());
    const win = new BrowserWindow({
      x: state.x,
      y: state.y,
      width: state.width,
      height: state.height,
      minWidth: Math.min(WINDOW_MIN_WIDTH, state.width),
      minHeight: Math.min(WINDOW_MIN_HEIGHT, state.height),
      show: false,
      autoHideMenuBar: true,
      title: "gogosling",
      webPreferences: {
        preload: path.join(__dirname, "..", "preload", "preload.js"),
        contextIsolation: true,
        sandbox: true,
        nodeIntegration: false,
        webSecurity: true,
        allowRunningInsecureContent: false,
        spellcheck: false,
        devTools: !app.isPackaged,
      },
    });
    this.window = win;
    let maximized = state.maximized;
    const saver = createWindowStateSaver(() => {
      if (win.isDestroyed()) return;
      try {
        writeWindowState(statePath, { ...win.getNormalBounds(), maximized });
      } catch (err) {
        this.ctx.logger.warn("保存窗口偏好失败", { err });
      }
    });
    this.windowStateSaver = saver;
    win.on("resize", saver.schedule);
    win.on("move", saver.schedule);
    win.on("maximize", () => { maximized = true; saver.schedule(); });
    win.on("unmaximize", () => { maximized = false; this.ensureWindowVisible(); saver.schedule(); });
    // maximize 会主动显示窗口；等真正首次显示时再恢复，保留开机隐藏启动行为。
    win.once("show", () => {
      if (state.maximized) win.maximize();
    });

    // 只有启动时创建的第一个窗口遵守"隐藏启动"；之后（托盘打开、深链）创建的窗口总是显示。
    const hidden = this.startHidden;
    this.startHidden = false;
    win.once("ready-to-show", () => {
      if (!hidden) win.show();
    });
    win.on("close", (event) => {
      saver.flush();
      this.ctx.diagnostics.clear();
      if (this.quitting) return;
      // 关闭即最小化到托盘；守护进程继续运行。
      event.preventDefault();
      win.hide();
    });
    win.on("closed", () => {
      this.ctx.diagnostics.clear();
      saver.cancel();
      if (this.windowStateSaver === saver) this.windowStateSaver = null;
      if (this.window === win) this.window = null;
    });

    const contents = win.webContents;
    contents.on("did-start-navigation", (_event, _url, _inPlace, isMainFrame) => {
      if (isMainFrame) this.ctx.diagnostics.clear();
    });
    contents.on("render-process-gone", () => {
      this.ctx.diagnostics.clear();
      this.ctx.diagnostics.record("renderer_gone");
    });
    win.on("unresponsive", () => this.ctx.diagnostics.record("renderer_unresponsive"));
    contents.on("will-prevent-unload", (event) => {
      // 必须同步取得决定；异步返回时 Electron 已取消卸载，会导致反复关闭与确认。
      const response = dialog.showMessageBoxSync(win, {
        type: "warning",
        title: t(this.locale, "dialog.unsavedChanges.title"),
        message: t(this.locale, "dialog.unsavedChanges.title"),
        detail: t(this.locale, "dialog.unsavedChanges.body"),
        buttons: [t(this.locale, "dialog.unsavedChanges.keepEditing"), t(this.locale, "dialog.unsavedChanges.discard")],
        defaultId: 0,
        cancelId: 0,
        noLink: true,
      });
      this.quitFlow.preventedUnload(event, response === 1);
      if (response !== 1) this.showWindow();
    });
    contents.setWindowOpenHandler(({ url }) => {
      const safe = sanitizeExternalUrl(url);
      if (safe) void shell.openExternal(safe);
      return { action: "deny" };
    });
    contents.on("will-navigate", (event, url) => {
      if (isTrustedPageUrl(url, { devOrigin })) return;
      event.preventDefault();
      const safe = sanitizeExternalUrl(url);
      if (safe) void shell.openExternal(safe);
    });
    contents.on("will-attach-webview", (event) => event.preventDefault());

    const target = this.ctx.dev ? this.ctx.dev.url : `${APP_ORIGIN}/`;
    void win.loadURL(target).catch((err: unknown) => {
      this.ctx.diagnostics.record("load_failed");
      this.ctx.logger.error("加载页面失败", { target, err });
    });
  }

  // ---- 托盘与自启动 ----------------------------------------------------------

  private createTray(): void {
    const { platform } = this.ctx;
    const template = platform === "darwin";
    const icon = nativeImage.createFromBuffer(trayIconPng(16, { template }), { scaleFactor: 1 });
    icon.addRepresentation({ scaleFactor: 2, buffer: trayIconPng(32, { template }) });
    if (template) icon.setTemplateImage(true);

    const tray = new Tray(icon);
    this.tray = tray;
    if (platform !== "darwin") {
      tray.on("click", () => this.showWindow());
      tray.on("double-click", () => this.showWindow());
    }
    this.refreshTray(this.ctx.daemon.getStatus());
  }

  /** 读取登录项状态；Windows 上必须带与写入时相同的 args，否则 openAtLogin 永远为 false。 */
  private readLoginItem(): LoginItemState | null {
    if (!autostartSupported(this.ctx.platform)) return null;
    try {
      return app.getLoginItemSettings(loginItemQuery(this.ctx.platform));
    } catch (err) {
      this.ctx.logger.warn("读取开机自启动状态失败", { err });
      return null;
    }
  }

  private refreshTray(status: DaemonStatus): void {
    if (!this.tray) return;
    const { platform } = this.ctx;
    const supported = autostartSupported(platform);
    const loginItem = supported ? this.readLoginItem() : null;
    const model = {
      locale: this.locale,
      status,
      autostart: {
        supported,
        enabled: supported && app.isPackaged,
        checked: loginItem ? autostartChecked(platform, loginItem) : false,
      },
    };
    this.tray.setToolTip(trayTooltip(this.locale, status));
    this.tray.setContextMenu(
      Menu.buildFromTemplate(
        buildTrayMenuTemplate(model, {
          open: () => this.showWindow(),
          restartDaemon: () => void this.restartDaemon(),
          quit: () => app.quit(),
          setAutostart: (enabled) => this.setAutostart(enabled),
        }),
      ),
    );
  }

  private setAutostart(enabled: boolean): void {
    try {
      app.setLoginItemSettings(loginItemWrite(this.ctx.platform, enabled));
      this.ctx.logger.info("更新开机自启动", { enabled });
    } catch (err) {
      this.ctx.logger.error("设置开机自启动失败", { err });
    }
    this.refreshTray(this.ctx.daemon.getStatus());
  }

  private async restartDaemon(): Promise<void> {
    const status = this.ctx.daemon.getStatus();
    if (!canRestart(status.state)) return;
    if (status.state === "ready" && !status.owned) {
      await dialog.showMessageBox({
        type: "info",
        title: t(this.locale, "dialog.restartNotOwned.title"),
        message: t(this.locale, "dialog.restartNotOwned.title"),
        detail: t(this.locale, "dialog.restartNotOwned.body"),
        buttons: [t(this.locale, "dialog.ok")],
      });
      // 仍然重新发现一次，以便连接到它的新实例。
    }
    await this.ctx.daemon.restart();
  }

  // ---- 守护进程状态 ----------------------------------------------------------

  private onDaemonStatus(status: DaemonStatus): void {
    if (status.error) this.ctx.diagnostics.record(status.error === "exited" ? "daemon_exited" : status.error);
    this.refreshTray(status);
    this.window?.webContents.send(IPC.daemonStatus, status);

    if (status.state === "ready") {
      const conn = this.ctx.daemon.getConnection();
      // 令牌变化（首次就绪或守护进程重启）时重载页面，让 web 重新执行连接发现并丢掉旧实例的状态
      // （proxy 模式下令牌虽不在页面里，旧实例的事件 Seq 与缓存同样作废）。
      if (conn && conn.token !== this.lastReadyToken) {
        this.lastReadyToken = conn.token;
        if (this.window && !this.window.webContents.isLoadingMainFrame()) this.window.webContents.reload();
      }
      return;
    }
    if (status.state === "failed") void this.showFailureDialog(status);
  }

  private async showFailureDialog(status: DaemonStatus): Promise<void> {
    if (this.failureDialogOpen || this.quitting) return;
    this.failureDialogOpen = true;
    try {
      const errorKey = `daemon.error.${status.error ?? "exited"}` as const;
      const { response } = await dialog.showMessageBox({
        type: "error",
        title: t(this.locale, "dialog.daemonFailed.title"),
        message: t(this.locale, "dialog.daemonFailed.title"),
        detail: `${t(this.locale, errorKey, { max: status.maxAttempts })}\n${t(this.locale, "dialog.daemonFailed.logHint", { logPath: this.ctx.logPath })}`,
        buttons: [t(this.locale, "dialog.retry"), t(this.locale, "dialog.dismiss"), ...(this.ctx.diagnostics.status().available ? [t(this.locale, "diagnostics.title")] : [])],
        defaultId: 0,
        cancelId: 1,
      });
      if (response === 0) await this.ctx.daemon.start();
      const parent = this.window;
      if (response === 2 && !this.quitting && parent && !parent.isDestroyed()) {
        await reviewNativeDiagnostics(this.ctx.diagnostics, this.locale, parentedDiagnosticDialog(parent, (window, options) => dialog.showMessageBox(window, options)));
      }
    } finally {
      this.failureDialogOpen = false;
    }
  }
}

// ---- 启动 ---------------------------------------------------------------------

function textResponse(status: number, body: string): Response {
  return new Response(body, { status, headers: { "content-type": "text/plain; charset=utf-8" } });
}

async function exists(file: string): Promise<boolean> {
  try {
    await fs.promises.access(file);
    return true;
  } catch {
    return false;
  }
}

function registerDeepLinkScheme(logger: Logger): void {
  let ok: boolean;
  if (process.defaultApp && process.argv.length >= 2) {
    // 开发态（electron .）：必须把脚本路径一起注册，否则系统会用裸 electron 启动。
    ok = app.setAsDefaultProtocolClient(DEEP_LINK_SCHEME, process.execPath, [path.resolve(process.argv[1]!)]);
  } else {
    ok = app.setAsDefaultProtocolClient(DEEP_LINK_SCHEME);
  }
  if (!ok) logger.warn("注册深链协议失败", { scheme: DEEP_LINK_SCHEME });
}

function bootstrap(): void {
  // 必须在 app ready 之前：让 app:// 成为 standard scheme（有 origin、支持 fetch 与相对路径）。
  protocol.registerSchemesAsPrivileged([
    { scheme: APP_SCHEME, privileges: { standard: true, secure: true, supportFetchAPI: true, corsEnabled: true, stream: true } },
  ]);

  const env = process.env;
  const dataDir = resolveDataDir(env, os.homedir());
  const logger = createFileLogger(path.join(dataDir, "logs", "desktop.log"), {
    mirrorToConsole: !app.isPackaged,
    minLevel: app.isPackaged ? "info" : "debug",
  });
  const dev = parseDevWebUrl(env[ENV_WEB_URL]);
  const webRoot = resolveWebRoot({ isPackaged: app.isPackaged, resourcesPath: process.resourcesPath, appPath: app.getAppPath() });
  const daemon = new DaemonManager({ dataDir, resourcesPath: process.resourcesPath, platform: process.platform, env, logger });
  const diagnostics = new DesktopDiagnostics({
    enabled: releaseDiagnosticsEnabled({
      isPackaged: app.isPackaged,
      hasDevOverride: env[ENV_WEB_URL] !== undefined,
      readMetadata: () => JSON.parse(fs.readFileSync(path.join(app.getAppPath(), "package.json"), "utf8")) as unknown,
    }),
    version: app.getVersion(), platform: process.platform, arch: process.arch,
    snapshot: () => {
      const status = daemon.getStatus();
      return { daemonState: status.state, daemonOwned: status.owned, restartAttempts: status.attempt, errorCode: status.error, exitCode: status.lastExit?.code };
    },
  });
  const shellApp = new DesktopShell({
    daemon,
    diagnostics,
    logger,
    logPath: logger.path,
    dataDir,
    webRoot,
    dev,
    platform: process.platform,
  });
  // ready 且壳 init 之前到达的深链先暂存（macOS 冷启动时 open-url 早于 ready）。
  const deepLinks = new DeepLinkGate((url) => shellApp.handleDeepLink(url));

  logger.info("桌面壳启动", { version: app.getVersion(), electron: process.versions.electron, data_dir: dataDir, web_root: webRoot, dev: dev?.url ?? null });

  app.on("second-instance", (_event, argv) => {
    const link = findDeepLink(argv);
    if (link) deepLinks.offer(link);
    else shellApp.showWindow();
  });
  // 必须在 ready 之前注册，才能收到"把应用拉起来的那个 URL"。
  app.on("open-url", (event, url) => {
    event.preventDefault();
    deepLinks.offer(url);
  });
  app.on("activate", () => shellApp.showWindow());
  app.on("window-all-closed", () => {
    // 托盘常驻：窗口全部关闭也不退出。
  });
  app.on("before-quit", () => shellApp.handleBeforeQuit());
  app.on("will-quit", (event) => shellApp.handleWillQuit(event));

  app
    .whenReady()
    .then(() => {
      registerDeepLinkScheme(logger);
      shellApp.init();
      // 先处理 ready 前暂存的 open-url；没有时再看命令行（Windows/Linux 冷启动）。
      deepLinks.markReady(findDeepLink(process.argv));
      return daemon.start();
    })
    .catch((err: unknown) => {
      diagnostics.record("daemon_start_failed");
      logger.error("启动失败", { err });
    });
}

if (app.requestSingleInstanceLock()) {
  bootstrap();
} else {
  // 另一个实例已在运行：它会收到 second-instance 并把窗口带到前台。
  app.quit();
}
