// 按需原生回归：真实 WebContentsView + 内存 HTTPS 固件，不访问公网或用户数据。
// 此脚本需要图形桌面；普通 CI 继续使用无界面单元测试。
const { app, BrowserWindow, session } = require("electron");
const assert = require("node:assert/strict");
const { WorkspaceBrowser } = require("../dist/main/workspace-browser.js");
const { WorkspaceBrowserQuitGate } = require("../dist/main/workspace-browser-quit.js");
const { QuitFlow } = require("../dist/main/quit-flow.js");

const profile = process.env.GOGOSLING_BROWSER_TEST_PROFILE;
if (!profile) throw new Error("Run workspace-browser-runtime.mjs to isolate the profile");
app.setPath("userData", profile);
const publicMode = process.argv.includes("--public");
const quitMode = process.argv.find(arg => arg === "--quit-empty" || arg === "--quit-confirm");
let win;
let manager;
let checks = 0;
let confirmations = 0;
let allowDiscard = false;
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
    if (!publicMode) isolated.protocol.handle("https", () => new Response("<html><title>Synthetic page</title><input id='draft'></html>", { headers: { "content-type": "text/html" } }));
    return isolated;
  };
  win = new BrowserWindow({ show: false, width: 1000, height: 800, webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false } });
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
  assert.equal(manager.current().url, "https://example.com/a");
  let contents = await loaded();
  assert.notEqual(contents.session, session.defaultSession);
  assert.equal(contents.session.storagePath, null);
  assert.deepEqual(await contents.executeJavaScript("[typeof window.require, typeof window.gogosling, typeof window.process]"), ["undefined", "undefined", "undefined"]);
  const preferences = contents.getLastWebPreferences();
  assert.equal(preferences.sandbox, true);
  assert.equal(preferences.contextIsolation, true);
  assert.equal(preferences.nodeIntegration, false);
  assert.equal(preferences.preload, undefined);
  mark("isolated memory session without application bridge");

  await contents.executeJavaScript("document.getElementById('draft').value='synthetic unsaved draft'; window.onbeforeunload=e=>{e.preventDefault();e.returnValue=''}; undefined", true);
  const denied = await manager.show({ tabId: "page_b", url: "https://example.com/b", bounds });
  assert.equal(denied.error, "cancelled");
  assert.equal(denied.tabId, "page_a");
  assert.equal(contents.isDestroyed(), false);
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
  mark("immediate retry respects consent and releases the page");

  await manager.show({ tabId: "page_a", url: "https://example.com/a", bounds });
  contents = await loaded();
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
  manager.dispose();
  assert.equal(manager.current(), null);
  assert.equal(manager.isDisposed, true);
  assert.equal((await manager.show({ tabId: "late", url: "https://example.com/", bounds })).error, "unavailable");
  mark("shutdown prevents late page creation");
  console.log(JSON.stringify({ passed: checks, network: "synthetic local protocol", electron: process.versions.electron }));
  cleanup(0);
}).catch(error => { console.error(error); cleanup(1); });

setTimeout(() => { console.error("native browser test timed out"); cleanup(2); }, publicMode ? 50_000 : 25_000).unref();
