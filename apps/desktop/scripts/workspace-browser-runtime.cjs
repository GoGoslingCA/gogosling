// 按需原生回归：真实 WebContentsView + 内存 HTTPS 固件，不访问公网或用户数据。
// 此脚本需要图形桌面；普通 CI 继续使用无界面单元测试。
const { app, BrowserWindow, session } = require("electron");
const assert = require("node:assert/strict");
const net = require("node:net");
const { WorkspaceBrowser } = require("../dist/main/workspace-browser.js");
const { WorkspaceBrowserQuitGate } = require("../dist/main/workspace-browser-quit.js");
const { QuitFlow } = require("../dist/main/quit-flow.js");

const profile = process.env.GOGOSLING_BROWSER_TEST_PROFILE;
if (!profile) throw new Error("Run workspace-browser-runtime.mjs to isolate the profile");
app.setPath("userData", profile);
const publicMode = process.argv.includes("--public");
const presentationMode = process.argv.includes("--presentation");
const quitMode = process.argv.find(arg => arg === "--quit-empty" || arg === "--quit-confirm");
let win;
let manager;
let checks = 0;
let confirmations = 0;
let allowDiscard = false;
let pendingStarted;
let pendingCancelled;
let pendingRequests = 0;
const mark = name => { ++checks; console.log(JSON.stringify({ check: name, status: "passed" })); };

function view() { return win.contentView.children.find(child => child.webContents); }
async function loaded() {
  const contents = view().webContents;
  if (contents.isLoading()) await new Promise(resolve => contents.once("did-stop-loading", resolve));
  return contents;
}
function cleanup(code) {
  manager?.dispose();
  if (win && !win.isDestroyed()) win.destroy();
  app.exit(code);
}

app.whenReady().then(async () => {
  const original = session.fromPartition.bind(session);
  session.fromPartition = (...args) => {
    const isolated = original(...args);
    if (!publicMode) isolated.protocol.handle("https", request => {
      if (new URL(request.url).pathname === "/pending") {
        return new Response(new ReadableStream({
          start(controller) { ++pendingRequests; controller.enqueue(new TextEncoder().encode("pending")); pendingStarted?.(); },
          cancel() { --pendingRequests; pendingCancelled?.(); },
        }), { headers: { "content-type": "text/plain" } });
      }
      if (new URL(request.url).pathname === "/fail") return Response.error();
      return new Response("<html><title>Synthetic page</title><input id='draft'></html>", { headers: { "content-type": "text/html", "content-security-policy": "worker-src 'none'" } });
    });
    return isolated;
  };
  win = new BrowserWindow({ show: false, width: 1000, height: 800, webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false } });
  if (presentationMode) win.showInactive();
  manager = new WorkspaceBrowser({ window: () => win, state: () => {}, confirmDiscard: () => { ++confirmations; return allowDiscard; } });
  const bounds = { x: 300, y: 100, width: 600, height: 600 };
  if (quitMode) {
    let stops = 0;
    const gate = new WorkspaceBrowserQuitGate({ closeBrowser: () => manager.closeForQuit(), quit: () => app.quit() });
    const flow = new QuitFlow({ stopDaemon: async () => { ++stops; await new Promise(resolve => setTimeout(resolve, 10)); }, quit: () => app.quit(), onStopError: error => { throw error; } });
    app.on("before-quit", event => { if (gate.beforeQuit(event)) flow.beforeQuit(); });
    app.on("will-quit", event => { manager.dispose(); flow.willQuit(event); });
    app.on("quit", () => {
      assert.equal(stops, 1);
      assert.equal(win.isDestroyed(), true);
      if (quitMode === "--quit-confirm") assert.equal(confirmations, 2);
      mark(quitMode === "--quit-empty" ? "empty workspace quits through the real Electron lifecycle" : "cancel then retry quits through the real Electron lifecycle");
    });
    if (quitMode === "--quit-confirm") {
      await manager.show({ tabId: "quit_page", url: "https://example.com/", bounds });
      const contents = await loaded();
      await contents.executeJavaScript("window.onbeforeunload=e=>{e.preventDefault();e.returnValue=''}; undefined", true);
      app.quit();
      await new Promise(resolve => setTimeout(resolve, 150));
      assert.equal(confirmations, 1);
      assert.equal(win.isDestroyed(), false);
      assert.equal(stops, 0);
      allowDiscard = true;
    }
    app.quit();
    return;
  }
  if (publicMode) {
    let previous;
    for (const [index, url] of ["https://gogosling.ca/", "https://example.com/"].entries()) {
      const state = await manager.show({ tabId: `public_${index}`, url, bounds });
      assert.equal(state.error, undefined);
      const contents = await loaded();
      assert.equal(manager.current().error, undefined);
      assert.ok(contents.getTitle().length > 0);
      assert.ok(contents.getURL().startsWith("https://"));
      assert.equal(win.contentView.children.filter(child => child.webContents).length, 1);
      if (previous) assert.equal(previous.isDestroyed(), true);
      previous = contents;
      console.log(JSON.stringify({ check: "public page", origin: new URL(contents.getURL()).origin, hasTitle: true, liveViews: 1 }));
    }
    assert.equal(await manager.closeForQuit(), true);
    mark("public HTTPS navigation and replacement release");
    cleanup(0);
    return;
  }
  assert.equal(manager.current(), null);
  await manager.show({ tabId: "page_a", url: "https://example.com/a", bounds });
  assert.equal(manager.pageReady, false);
  assert.equal(view().getVisible(), false);
  assert.equal(manager.current().url, "https://example.com/a");
  let contents = await loaded();
  assert.equal(manager.pageReady, true);
  if (presentationMode) assert.equal(view().getVisible(), true);
  assert.notEqual(contents.session, session.defaultSession);
  assert.equal(contents.session.storagePath, null);
  assert.deepEqual(await contents.executeJavaScript("[typeof window.require, typeof window.gogosling, typeof window.process]"), ["undefined", "undefined", "undefined"]);
  const preferences = contents.getLastWebPreferences();
  assert.equal(preferences.sandbox, true);
  assert.equal(preferences.contextIsolation, true);
  assert.equal(preferences.nodeIntegration, false);
  assert.equal(preferences.preload, undefined);
  mark("isolated memory session without application bridge");

  const retainedSession = contents.session;
  const downloadHandlers = retainedSession.listenerCount("will-download");
  await retainedSession.cookies.set({ url: "https://example.com/", name: "synthetic_session", value: "local-fixture" });
  await contents.executeJavaScript("localStorage.setItem('synthetic-draft', 'local-fixture'); undefined");
  const endpoint = new URL(await manager.egress.start());
  const idleSocket = net.connect(Number(endpoint.port), endpoint.hostname);
  await new Promise((resolve, reject) => { idleSocket.once("connect", resolve); idleSocket.once("error", reject); });
  const socketClosed = new Promise(resolve => idleSocket.once("close", resolve));

  await contents.executeJavaScript("document.getElementById('draft').value='synthetic unsaved draft'; window.onbeforeunload=e=>{e.preventDefault();e.returnValue=''}; undefined", true);
  const denied = await manager.show({ tabId: "page_b", url: "https://example.com/b", bounds });
  assert.equal(denied.error, "cancelled");
  assert.equal(denied.tabId, "page_a");
  assert.equal(contents.isDestroyed(), false);
  assert.equal(manager.egress.server.listening, true);
  assert.equal(idleSocket.destroyed, false);
  assert.equal(win.contentView.children.filter(child => child.webContents).length, 1);
  assert.equal(await contents.executeJavaScript("document.getElementById('draft').value"), "synthetic unsaved draft");
  mark("switch cancellation preserves the original page and draft");

  manager.hide();
  assert.equal(manager.current().tabId, "page_a");
  assert.equal(view().getVisible(), false);
  assert.equal(contents.isAudioMuted(), true);
  mark("hiding preserves one recoverable page");

  allowDiscard = true;
  assert.equal(await manager.close("page_a"), null);
  assert.equal(contents.isDestroyed(), true);
  assert.equal(win.contentView.children.filter(child => child.webContents).length, 0);
  assert.equal(confirmations, 2);
  await socketClosed;
  assert.equal(manager.egress, null);
  assert.equal(manager.pageSession, retainedSession);
  assert.equal((await retainedSession.cookies.get({ name: "synthetic_session" }))[0].value, "local-fixture");
  mark("immediate retry respects consent and releases the page");

  await manager.show({ tabId: "page_a", url: "https://example.com/a", bounds });
  contents = await loaded();
  assert.equal(contents.session, retainedSession);
  assert.equal(retainedSession.listenerCount("will-download"), downloadHandlers);
  assert.equal(await contents.executeJavaScript("localStorage.getItem('synthetic-draft')"), "local-fixture");
  assert.equal(manager.egress.server.listening, true);
  mark("reopening reuses one session and preserves dormant site data without duplicate listeners");
  const invalid = await manager.show({ tabId: "page_b", url: "http://localhost/", bounds });
  assert.equal(invalid.tabId, "page_b");
  assert.equal(invalid.error, "invalid_url");
  assert.equal(manager.current().tabId, "page_a");
  await manager.navigate({ tabId: "page_b", url: "https://example.com/b" });
  await loaded();
  assert.equal(contents.isDestroyed(), true);
  assert.equal(manager.current().tabId, "page_b");
  assert.equal(win.contentView.children.filter(child => child.webContents).length, 1);
  mark("invalid address recovery keeps the single page limit");

  const started = new Promise(resolve => { pendingStarted = resolve; });
  const cancelled = new Promise(resolve => { pendingCancelled = resolve; });
  await view().webContents.executeJavaScript("window.pendingFixture = fetch('/pending').then(response => response.text()).catch(() => {}); undefined");
  await started;
  assert.equal(pendingRequests, 1);
  await manager.show({ tabId: "pending_replacement", url: "https://example.com/replacement", bounds });
  await loaded();
  await Promise.race([cancelled, new Promise((_resolve, reject) => setTimeout(() => reject(new Error("old_request_not_cancelled")), 2000))]);
  assert.equal(pendingRequests, 0);
  assert.equal(Object.keys(retainedSession.serviceWorkers.getAllRunning()).length, 0);
  mark("replacing a page cancels its pending response while keeping the bounded transport pool");

  await manager.show({ tabId: "failure", url: "https://example.com/fail", bounds });
  await loaded();
  assert.equal(manager.current().error, "load_failed");
  assert.equal(manager.pageReady, false);
  assert.equal(view().getVisible(), false);
  mark("failed native pages leave the application loading and retry interface unobstructed");
  await manager.show({ tabId: "page_b", url: "https://example.com/b", bounds });
  await loaded();

  const crashed = view().webContents;
  const crash = new Promise(resolve => crashed.once("render-process-gone", resolve));
  crashed.forcefullyCrashRenderer();
  await crash;
  await manager.action({ tabId: "page_b", action: "reload" });
  await loaded();
  assert.equal(manager.current().tabId, "page_b");
  assert.equal(win.contentView.children.filter(child => child.webContents).length, 1);
  mark("reload recreates a crashed renderer");

  assert.equal(await manager.closeForQuit(), true);
  assert.equal(manager.egress, null);
  const opening = manager.show({ tabId: "cancel_start", url: "https://example.com/cancel", bounds });
  manager.hide();
  assert.equal((await opening).error, "cancelled");
  assert.equal(manager.egress, null);
  assert.equal(win.contentView.children.filter(child => child.webContents).length, 0);
  mark("canceling startup leaves no proxy listener or guest view");
  await manager.show({ tabId: "dispose_active", url: "https://example.com/dispose", bounds });
  const disposedContents = await loaded();
  const disposed = new Promise(resolve => disposedContents.once("destroyed", resolve));
  manager.dispose();
  assert.equal(win.contentView.children.filter(child => child.webContents).length, 0);
  await disposed;
  assert.equal(disposedContents.isDestroyed(), true);
  assert.equal(manager.egress, null);
  assert.equal(manager.current(), null);
  assert.equal(manager.isDisposed, true);
  assert.equal((await manager.show({ tabId: "late", url: "https://example.com/", bounds })).error, "unavailable");
  mark("shutdown prevents late page creation");
  console.log(JSON.stringify({ passed: checks, network: "synthetic local protocol", electron: process.versions.electron }));
  cleanup(0);
}).catch(error => { console.error(error); cleanup(1); });

setTimeout(() => { console.error("native browser test timed out"); cleanup(2); }, publicMode ? 50_000 : 25_000).unref();
