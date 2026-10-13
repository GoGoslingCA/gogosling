// 网页工作台宿主：至多一个原生网页，不接触业务数据、应用协议或凭据。
// 所有网页使用独立内存会话和受控出口；切换时释放旧页面，未保存内容交由用户确认。
import { WebContentsView, session, type BrowserWindow, type Session } from "electron";
import { randomUUID } from "node:crypto";
import type { WorkspaceBrowserBounds, WorkspaceBrowserError, WorkspaceBrowserState } from "../shared/workspace-browser.js";
import { WorkspaceBrowserEgress } from "./workspace-browser-egress.js";
import { browserBounds, browserTabId, browserURL } from "./workspace-browser-policy.js";

interface BrowserHost {
  window(): BrowserWindow | null;
  state(value: WorkspaceBrowserState): void;
  confirmDiscard(): boolean;
}

/** 原生网页生命周期由此类独占；切换不会积累后台渲染进程。 */
export class WorkspaceBrowser {
  private view: WebContentsView | null = null;
  private tabId = "";
  private intendedURL = "";
  private pageSession: Session | null = null;
  private egress: WorkspaceBrowserEgress | null = null;
  private readonly resolutionBudget = { active: 0 };
  private starting: Promise<void> | null = null;
  private idleCleanup: Promise<void> | null = null;
  private busy = false;
  private disposed = false;
  private visibilityGeneration = 0;
  private wantedVisible = false;
  private pageReady = false;
  private bounds: WorkspaceBrowserBounds | null = null;
  private error: WorkspaceBrowserError | undefined;

  constructor(private readonly host: BrowserHost) {}

  /** 真正关闭窗口后，新窗口需要一个新的宿主。 */
  get isDisposed(): boolean { return this.disposed; }

  /** 刷新主界面后恢复唯一网页入口；不自动显示，也不从磁盘读取。 */
  current(): WorkspaceBrowserState | null {
    const state = this.snapshot();
    return browserTabId(state.tabId) && browserURL(state.url) ? state : null;
  }

  /** 显示已有页或更换活动页；布局变化不触发重新加载。 */
  async show(raw: unknown): Promise<WorkspaceBrowserState> {
    const input = raw && typeof raw === "object" ? raw as Record<string, unknown> : {};
    if (!browserTabId(input.tabId)) return this.snapshot("invalid_url");
    const win = this.host.window();
    if (this.disposed || !win || win.isDestroyed()) return this.snapshot("unavailable", input.tabId);
    const [width, height] = win.getContentSize();
    const bounds = browserBounds(input.bounds, { width, height });
    if (!bounds) return this.snapshot("unavailable", input.tabId);
    this.bounds = bounds;
    const url = browserURL(input.url);
    if (!url) return this.snapshot("invalid_url", input.tabId);
    if (this.busy) return this.snapshot("busy", input.tabId);
    this.wantedVisible = true;
    const generation = ++this.visibilityGeneration;
    if (this.view && this.tabId === input.tabId) { this.present(); return this.snapshot(); }
    this.busy = true;
    try {
      if (!await this.release()) { this.error = "cancelled"; this.emit(); return this.snapshot(); }
      await this.prepareSession();
      if (this.disposed || generation !== this.visibilityGeneration || win.isDestroyed()) {
        await this.releaseIdleResources();
        return this.snapshot("cancelled", input.tabId);
      }
      this.tabId = input.tabId;
      this.intendedURL = url.href;
      this.error = undefined;
      this.pageReady = false;
      const view = new WebContentsView({ webPreferences: {
        session: this.pageSession!, contextIsolation: true, sandbox: true, nodeIntegration: false,
        nodeIntegrationInSubFrames: false, nodeIntegrationInWorker: false, webSecurity: true,
        allowRunningInsecureContent: false, navigateOnDragDrop: false, spellcheck: false,
        devTools: false, backgroundThrottling: true, disableDialogs: true,
        autoplayPolicy: "user-gesture-required",
      } });
      this.view = view;
      win.contentView.addChildView(view);
      this.installPagePolicy(view);
      this.present();
      void view.webContents.loadURL(url.href).catch(error => this.failLoad(view, error));
      return { ...this.snapshot(), url: url.href, loading: true };
    } catch {
      if (!this.view) await this.releaseIdleResources();
      return this.snapshot("unavailable", input.tabId);
    }
    finally { this.busy = false; }
  }

  /** 文件预览、遮挡或离开工作台时隐藏唯一网页；不会创建额外实例。 */
  hide(): void {
    ++this.visibilityGeneration;
    this.wantedVisible = false;
    this.present();
  }

  /** 关闭活动标签；未保存内容拒绝丢弃时保留原页面。 */
  async close(raw: unknown): Promise<WorkspaceBrowserState | null> {
    if (!browserTabId(raw) || !this.view || raw !== this.tabId) return null;
    if (this.busy) return this.snapshot("busy");
    this.busy = true;
    try {
      if (await this.release(true)) return null;
      this.error = "cancelled";
      this.emit();
      return this.snapshot();
    }
    finally { this.busy = false; }
  }

  /** 应用退出前也尊重网页的未保存内容。 */
  async closeForQuit(): Promise<boolean> {
    if (this.busy) return false;
    this.busy = true;
    try { return await this.release(true); } catch { return false; } finally { this.busy = false; }
  }

  /** 在当前标签导航，不允许控制后台或未知标签。 */
  async navigate(raw: unknown): Promise<WorkspaceBrowserState> {
    const input = raw && typeof raw === "object" ? raw as Record<string, unknown> : {};
    if (!browserTabId(input.tabId)) return this.snapshot("invalid_url");
    if (!this.view || input.tabId !== this.tabId) return this.show({ ...input, bounds: this.bounds });
    if (this.busy) return this.snapshot("busy");
    const url = browserURL(input.url);
    if (!url) return this.snapshot("invalid_url");
    this.error = undefined;
    this.intendedURL = url.href;
    const view = this.view;
    void view.webContents.loadURL(url.href).catch(error => this.failLoad(view, error));
    return this.snapshot();
  }

  /** 执行有限导航操作，不接受脚本或任意 Electron 方法。 */
  async action(raw: unknown): Promise<WorkspaceBrowserState> {
    const input = raw && typeof raw === "object" ? raw as Record<string, unknown> : {};
    if (!this.view && input.action === "reload" && input.tabId === this.tabId && browserURL(this.intendedURL)) {
      return this.show({ tabId: this.tabId, url: this.intendedURL, bounds: this.bounds });
    }
    if (!this.view || input.tabId !== this.tabId || this.busy) return this.snapshot("unavailable");
    const contents = this.view.webContents;
    this.error = undefined;
    switch (input.action) {
      case "back": if (contents.navigationHistory.canGoBack()) contents.navigationHistory.goBack(); break;
      case "forward": if (contents.navigationHistory.canGoForward()) contents.navigationHistory.goForward(); break;
      case "reload": contents.reload(); break;
      case "stop": contents.stop(); break;
      default: return this.snapshot("unavailable");
    }
    return this.snapshot();
  }

  /** 窗口状态变化后重新限制可见区域。 */
  present(): void {
    const win = this.host.window();
    const view = this.view;
    if (!win || win.isDestroyed() || !view || view.webContents.isDestroyed()) return;
    const [width, height] = win.getContentSize();
    const bounds = browserBounds(this.bounds, { width, height });
    const visible = this.wantedVisible && this.pageReady && !!bounds && win.isVisible() && !win.isMinimized();
    if (bounds) view.setBounds(bounds);
    view.setVisible(visible);
    view.webContents.setAudioMuted(!visible);
  }

  /** 主窗口已销毁或应用真正退出时强制释放，不能再启动。 */
  dispose(): void {
    this.disposed = true;
    this.hide();
    const view = this.view;
    this.view = null;
    this.pageReady = false;
    const win = this.host.window();
    if (view && win && !win.isDestroyed()) win.contentView.removeChildView(view);
    if (view && !view.webContents.isDestroyed()) view.webContents.close({ waitForBeforeUnload: false });
    this.egress?.dispose();
    this.egress = null;
    if (this.pageSession) {
      void this.pageSession.closeAllConnections().catch(() => {});
      void this.pageSession.clearStorageData().catch(() => {});
      void this.pageSession.clearCache().catch(() => {});
    }
  }

  /** 主进程证书事件只拒绝此来宾的请求。 */
  ownsWebContents(id: number): boolean { return this.view?.webContents.id === id; }

  private snapshot(error = this.error, fallbackId = ""): WorkspaceBrowserState {
    const contents = this.view?.webContents;
    if (!contents || contents.isDestroyed() || fallbackId && fallbackId !== this.tabId) return { tabId: fallbackId, url: "", title: "", loading: false, canGoBack: false, canGoForward: false, ...(error ? { error } : {}) };
    return { tabId: this.tabId, url: contents.getURL() || this.intendedURL, title: contents.getTitle().replace(/[\u0000-\u001f\u007f]/g, "").slice(0, 256), loading: contents.isLoading(), canGoBack: contents.navigationHistory.canGoBack(), canGoForward: contents.navigationHistory.canGoForward(), ...(error ? { error } : {}) };
  }

  private emit(): void { this.host.state(this.snapshot()); }

  private failLoad(view: WebContentsView, error: unknown): void {
    // 停止、替换导航及拒绝离开均可产生 ERR_ABORTED，不能把这些用户操作变成失败。
    if (this.view !== view || view.webContents.isDestroyed() || this.error === "cancelled") return;
    if (error instanceof Error && "code" in error && error.code === "ERR_ABORTED") return;
    this.error = "load_failed";
    this.pageReady = false;
    this.present();
    this.emit();
  }

  private async prepareSession(): Promise<void> {
    // 崩溃后的异步清理必须先结束，否则它可能关闭新标签刚建立的连接。
    if (this.idleCleanup) await this.idleCleanup;
    if (this.pageSession && this.egress) return;
    this.starting ??= (async () => {
      const egress = new WorkspaceBrowserEgress(undefined, this.resolutionBudget);
      this.egress = egress;
      const proxy = await egress.start();
      if (this.disposed) { egress.dispose(); throw new Error("disposed"); }
      // Electron 的内存分区容器无法单独销毁；复用唯一容器，避免关闭再打开累积 Session。
      const ses = this.pageSession ?? session.fromPartition(`workspace-${randomUUID()}`, { cache: false });
      await ses.setProxy({ mode: "fixed_servers", proxyRules: proxy, proxyBypassRules: "<-loopback>" });
      if (this.disposed) { egress.dispose(); throw new Error("disposed"); }
      if (this.pageSession) return;
      ses.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
      ses.setPermissionCheckHandler(() => false);
      ses.setDevicePermissionHandler(() => false);
      ses.setDisplayMediaRequestHandler((_request, callback) => callback({}));
      ses.on("will-download", event => event.preventDefault());
      ses.webRequest.onBeforeRequest((details, callback) => {
        const localResource = /^(?:blob:|data:)/.test(details.url) && ["image", "font", "media"].includes(details.resourceType);
        const url = details.url.replace(/^wss:/, "https:").replace(/^ws:/, "http:");
        callback({ cancel: !localResource && !browserURL(url) });
      });
      ses.webRequest.onHeadersReceived((details, callback) => {
        const headers = { ...details.responseHeaders };
        for (const key of Object.keys(headers)) if (key.toLowerCase() === "alt-svc") delete headers[key];
        if (details.resourceType === "mainFrame" || details.resourceType === "subFrame") {
          const key = Object.keys(headers).find(name => name.toLowerCase() === "content-security-policy") ?? "Content-Security-Policy";
          headers[key] = [...(headers[key] ?? []), "worker-src 'none'; object-src 'none'; frame-src https: http:"];
        }
        callback({ responseHeaders: headers });
      });
      this.pageSession = ses;
    })();
    try { await this.starting; }
    catch (error) { this.egress?.dispose(); this.egress = null; this.starting = null; throw error; }
    finally { this.starting = null; }
  }

  private installPagePolicy(view: WebContentsView): void {
    const contents = view.webContents;
    const tabId = this.tabId;
    contents.once("destroyed", () => {
      if (this.view !== view) return;
      const win = this.host.window();
      if (win && !win.isDestroyed()) win.contentView.removeChildView(view);
      this.view = null;
      this.pageReady = false;
      if (!this.busy && !this.disposed) void this.releaseIdleResources();
      this.host.state({ tabId, url: "", title: "", loading: false, canGoBack: false, canGoForward: false, error: this.error === "load_failed" ? "load_failed" : "unavailable" });
    });
    contents.setWebRTCIPHandlingPolicy("disable_non_proxied_udp");
    contents.setWindowOpenHandler(() => ({ action: "deny" }));
    contents.on("will-attach-webview", event => event.preventDefault());
    contents.on("will-navigate", (event, url) => {
      if (!browserURL(url)) { event.preventDefault(); this.error = "blocked_url"; this.emit(); }
    });
    contents.on("will-redirect", (event, url) => {
      if (!browserURL(url)) { event.preventDefault(); this.error = "blocked_url"; this.emit(); }
    });
    contents.on("will-prevent-unload", event => {
      view.setVisible(false);
      if (this.host.confirmDiscard()) event.preventDefault();
      else this.error = "cancelled";
      this.present();
    });
    contents.on("login", (event, _details, _authInfo, callback) => { event.preventDefault(); callback(); });
    contents.on("did-start-loading", () => { this.error = undefined; this.emit(); });
    contents.on("dom-ready", () => {
      if (this.view !== view || this.error === "load_failed" || !browserURL(contents.getURL())) return;
      this.pageReady = true;
      this.present();
    });
    contents.on("did-stop-loading", () => this.emit());
    const navigated = () => { if (browserURL(contents.getURL())) this.intendedURL = contents.getURL(); this.emit(); };
    contents.on("did-navigate", navigated);
    contents.on("did-navigate-in-page", navigated);
    contents.on("page-title-updated", () => this.emit());
    contents.on("did-fail-load", (_event, code, _description, _url, mainFrame) => {
      if (mainFrame && code !== -3) {
        this.error = "load_failed";
        this.pageReady = false;
        this.present();
        this.emit();
      }
    });
    contents.on("render-process-gone", () => {
      this.error = "load_failed";
      this.emit();
      if (this.view === view) {
        const win = this.host.window();
        if (win && !win.isDestroyed()) win.contentView.removeChildView(view);
        this.view = null;
        this.pageReady = false;
        if (!contents.isDestroyed()) contents.close({ waitForBeforeUnload: false });
        void this.releaseIdleResources();
      }
    });
  }

  private async release(stopNetwork = false): Promise<boolean> {
    const view = this.view;
    if (!view || view.webContents.isDestroyed()) {
      this.view = null;
      if (stopNetwork) await this.releaseIdleResources();
      return true;
    }
    const contents = view.webContents;
    const released = await new Promise<boolean>(resolve => {
      let settled = false;
      const finish = (ok: boolean) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        clearInterval(retry);
        contents.removeListener("destroyed", destroyed);
        contents.removeListener("will-prevent-unload", prevented);
        resolve(ok);
      };
      const destroyed = () => finish(true);
      // 等原生 beforeunload 回调退出再允许重试，避免同一事件循环内再次 close 被 Chromium 吞掉。
      const prevented = (event: Electron.Event) => { if (!event.defaultPrevented) setImmediate(() => finish(false)); };
      const timer = setTimeout(() => finish(false), 5000);
      // Chromium 在上次取消后短暂忽略立即重试；有界重发，不绕过 beforeunload。
      const retry = setInterval(() => { if (!contents.isDestroyed()) contents.close({ waitForBeforeUnload: true }); }, 250);
      contents.once("destroyed", destroyed);
      contents.on("will-prevent-unload", prevented);
      contents.close({ waitForBeforeUnload: true });
    });
    if (!released) return false;
    const win = this.host.window();
    if (this.view === view) {
      if (win && !win.isDestroyed()) win.contentView.removeChildView(view);
      this.view = null;
    }
    if (stopNetwork) await this.releaseIdleResources();
    else if (this.pageSession) {
      await this.pageSession.clearStorageData({ storages: ["serviceworkers", "cachestorage"] }).catch(() => {});
      // 旧页面请求随 WebContents 销毁而取消；保留有界连接池，避免同站切页重复握手。
    }
    return true;
  }

  private async releaseIdleResources(): Promise<void> {
    if (this.idleCleanup) return this.idleCleanup;
    // 仅在活动原生页关闭后停止出口；保留休眠标签回到站点时所需的身份和本地存储。
    this.egress?.dispose();
    this.egress = null;
    const ses = this.pageSession;
    if (!ses) return;
    this.idleCleanup = (async () => {
      await ses.closeAllConnections().catch(() => {});
      await ses.clearStorageData({ storages: ["serviceworkers", "cachestorage"] }).catch(() => {});
      await ses.clearCache().catch(() => {});
    })();
    try { await this.idleCleanup; } finally { this.idleCleanup = null; }
  }
}
