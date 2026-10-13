// 真实 Electron 宿主基准。合成 HTTPS 由内存协议返回，不改变生产出口策略。
// show 到加载完成、两帧确认和操作返回分别计时，不能当作人工输入到屏幕光子的延迟。
const { app, BrowserWindow, session, webContents } = require("electron");
const assert = require("node:assert/strict");
const { readFileSync, writeFileSync } = require("node:fs");
const { createHash } = require("node:crypto");
const { totalmem, release } = require("node:os");
const { performance } = require("node:perf_hooks");
const { execFileSync } = require("node:child_process");
const hostModule = process.env.GOGOSLING_BROWSER_BENCHMARK_MODULE || require.resolve("../dist/main/workspace-browser.js");
const { WorkspaceBrowser } = require(hostModule);

const profile = process.env.GOGOSLING_BROWSER_TEST_PROFILE;
const output = process.env.GOGOSLING_BROWSER_PERFORMANCE_OUTPUT;
if (!profile || !output) throw new Error("Use workspace-browser-benchmark.mjs");
app.setPath("userData", profile);
const sameOriginMode = process.argv.includes("--public-same-origin");
const publicMode = process.argv.includes("--public") || sameOriginMode;
const publicPages = sameOriginMode
  ? ["https://gogosling.ca/", "https://gogosling.ca/desktop.html", "https://gogosling.ca/how.html", "https://gogosling.ca/faq.html"]
  : ["https://gogosling.ca/", "https://example.com/"];
const label = process.argv.includes("--label") ? process.argv[process.argv.indexOf("--label") + 1] : "measurement";
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const round = value => Math.round(value * 100) / 100;
let win;
let browser;
let uniqueSessions = new Set();
let counters = { responseCount: 0 };
let failures = [];
let events = 0;
let activeTrace = null;
const report = { label, measured_at: new Date().toISOString(), electron: process.versions.electron, chrome: process.versions.chrome,
  compiled_host_sha256: createHash("sha256").update(readFileSync(hostModule)).digest("hex"),
  platform: process.platform, arch: process.arch, os_release: release(), physical_memory_mib: round(totalmem() / 1048576),
  workload: publicMode ? "public HTTPS, network dependent, not a controlled comparison" : "local HTTPS protocol fixture, 1600 paragraphs and 8 MiB retained typed array, four tab descriptors, 30 switches",
  measurements: [], operations: [], samples: [], failures };

const percentile = (items, fraction) => [...items].sort((a, b) => a - b)[Math.ceil(items.length * fraction) - 1];
const summarize = items => items.length ? ({ count: items.length, p50_ms: round(percentile(items, .5)), p95_ms: round(percentile(items, .95)), max_ms: round(Math.max(...items)) }) : ({ count: 0 });
const activeView = () => win.contentView.children.find(child => child.webContents);

// 分段仅用于模块基准，不给产品注入计时器或遥测。视图构造段含原生构造、挂载和续体调度。
function instrument(host) {
  for (const method of ["release", "prepareSession"]) {
    const original = host[method].bind(host);
    host[method] = async (...args) => {
      const trace = activeTrace;
      if (trace) trace[`${method}_started`] = performance.now();
      try { return await original(...args); }
      finally { if (trace) trace[`${method}_finished`] = performance.now(); }
    };
  }
  const install = host.installPagePolicy.bind(host);
  host.installPagePolicy = view => {
    if (activeTrace) activeTrace.policy_started = performance.now();
    install(view);
    if (activeTrace) activeTrace.policy_finished = performance.now();
    view.webContents.on("did-start-loading", () => { if (activeTrace) activeTrace.loading_started = performance.now(); });
    view.webContents.on("did-navigate", () => { if (activeTrace) activeTrace.navigation_committed = performance.now(); });
    view.webContents.on("dom-ready", () => { if (activeTrace) activeTrace.dom_ready = performance.now(); });
  };
}

function stages(trace, loaded, rendered, frames) {
  const elapsed = (from, to) => typeof from === "number" && typeof to === "number" ? round(to - from) : null;
  return {
    release_ms: elapsed(trace.release_started, trace.release_finished),
    prepare_session_ms: elapsed(trace.prepareSession_started, trace.prepareSession_finished),
    native_view_and_attach_ms: elapsed(trace.prepareSession_finished, trace.policy_started),
    install_policy_ms: elapsed(trace.policy_started, trace.policy_finished),
    start_to_navigation_commit_ms: elapsed(trace.started, trace.navigation_committed),
    loading_to_commit_ms: elapsed(trace.loading_started, trace.navigation_committed),
    commit_to_dom_ready_ms: elapsed(trace.navigation_committed, trace.dom_ready),
    dom_ready_to_load_ms: elapsed(trace.dom_ready, loaded),
    load_to_two_frames_ms: frames ? elapsed(loaded, rendered) : null,
  };
}

async function snapshot(phase) {
  const metrics = app.getAppMetrics();
  const grouped = {};
  let osRSSKiB = 0;
  let osRSSAvailable = true;
  for (const metric of metrics) {
    const group = grouped[metric.type] ??= { processes: 0, working_set_mib: 0, os_rss_mib: 0 };
    ++group.processes;
    group.working_set_mib += metric.memory.workingSetSize / 1024;
    if (process.platform === "darwin" || process.platform === "linux") {
      try {
        const value = Number(execFileSync("/bin/ps", ["-o", "rss=", "-p", String(metric.pid)], { encoding: "utf8" }).trim());
        if (!Number.isFinite(value)) throw new Error("missing rss");
        osRSSKiB += value;
        group.os_rss_mib += value / 1024;
      } catch { osRSSAvailable = false; }
    } else osRSSAvailable = false;
  }
  for (const group of Object.values(grouped)) { group.working_set_mib = round(group.working_set_mib); group.os_rss_mib = round(group.os_rss_mib); }
  const egress = browser?.egress;
  const pageSession = browser?.pageSession;
  report.samples.push({ phase, main_rss_mib: round(process.memoryUsage().rss / 1048576), main_heap_used_mib: round(process.memoryUsage().heapUsed / 1048576),
    electron_working_set_mib: round(metrics.reduce((sum, metric) => sum + metric.memory.workingSetSize / 1024, 0)),
    os_process_rss_sum_mib: osRSSAvailable ? round(osRSSKiB / 1024) : null,
    metric_processes: metrics.length, process_groups: grouped,
    live_web_contents: webContents.getAllWebContents().filter(contents => !contents.isDestroyed()).length,
    guest_views: win.contentView.children.filter(child => child.webContents).length,
    proxy_listening: Boolean(egress?.server?.listening), proxy_sockets: egress?.sockets?.size ?? 0,
    proxy_requests: egress?.requests ?? 0, proxy_dns_pending: browser?.resolutionBudget?.active ?? egress?.resolutions ?? 0,
    created_memory_sessions: uniqueSessions.size, retained_session: Boolean(pageSession),
    storage_on_disk: pageSession ? pageSession.storagePath !== null : false,
    session_cache_bytes: pageSession ? await pageSession.getCacheSize() : 0,
    session_workers: pageSession ? Object.keys(pageSession.serviceWorkers.getAllRunning()).length : 0,
    active_tcp_resources: process.getActiveResourcesInfo().filter(type => /TCP|TCPSERVER/.test(type)).length,
    host_state_events: events, ...counters });
  writeFileSync(output, JSON.stringify(report, null, 2) + "\n", { mode: 0o600 });
}

async function switchPage(index, url) {
  const started = performance.now();
  const trace = activeTrace = { started };
  const previous = activeView()?.webContents;
  const state = await browser.show({ tabId: `page_${index % 4}`, url, bounds: { x: 10, y: 60, width: 950, height: 690 } });
  const shown = performance.now();
  assert.equal(state.error, undefined);
  const contents = activeView().webContents;
  if (previous) assert.equal(previous.isDestroyed(), true);
  assert.equal(win.contentView.children.filter(child => child.webContents).length, 1);
  await new Promise((resolve, reject) => {
    if (!contents.isLoading()) return resolve();
    const timer = setTimeout(() => done(new Error("load_timeout")), 25000);
    function done(error) {
      clearTimeout(timer);
      contents.removeListener("did-finish-load", finish);
      contents.removeListener("did-fail-load", fail);
      error ? reject(error) : resolve();
    }
    const finish = () => done();
    const fail = (_event, code, _description, _url, mainFrame) => { if (mainFrame && code !== -3) done(new Error(`load_failed_${code}`)); };
    contents.once("did-finish-load", finish);
    contents.on("did-fail-load", fail);
  });
  const loaded = performance.now();
  if (browser.current()?.error) throw new Error(browser.current().error);
  const presentation = await contents.executeJavaScript("new Promise(resolve => { let done=false; const finish=frames=>{if(done)return;done=true;const nav=performance.getEntriesByType('navigation')[0];resolve({frames,visibility:document.visibilityState,paints:performance.getEntriesByType('paint').map(entry=>({name:entry.name,startTime:entry.startTime})),navigation:nav?Object.fromEntries(['fetchStart','requestStart','responseStart','responseEnd','domInteractive','domContentLoadedEventEnd','domComplete','loadEventEnd'].map(key=>[key,nav[key]])):null})};setTimeout(()=>finish(false),1000);requestAnimationFrame(()=>requestAnimationFrame(()=>finish(true))) })");
  const rendered = performance.now();
  activeTrace = null;
  const item = { index, ...(publicMode ? { origin: new URL(url).origin, page: new URL(url).pathname } : {}), show_return_ms: round(shown - started), did_finish_load_ms: round(loaded - started), two_frame_ack_ms: presentation.frames ? round(rendered - started) : null, stages: stages(trace, loaded, rendered, presentation.frames), visibility: presentation.visibility, navigation_paints: presentation.paints, navigation_timing: presentation.navigation };
  report.measurements.push(item);
  return item;
}

function cleanup(code) {
  browser?.dispose();
  if (win && !win.isDestroyed()) win.destroy();
  app.exit(code);
}

app.whenReady().then(async () => {
  const fromPartition = session.fromPartition.bind(session);
  session.fromPartition = (...args) => {
    const isolated = fromPartition(...args);
    if (!uniqueSessions.has(isolated)) {
      uniqueSessions.add(isolated);
      if (!publicMode) isolated.protocol.handle("https", () => {
        ++counters.responseCount;
        const rows = Array.from({ length: 1600 }, (_, i) => `<p>Local synthetic reading fixture ${i}: a bounded paragraph for layout and lifecycle measurement.</p>`).join("");
        return new Response(`<!doctype html><html><head><title>Browser performance fixture</title><style>body{font:14px system-ui;max-width:760px;margin:32px auto;color:#253445}p{line-height:1.5}</style></head><body><h1>Local reading fixture</h1>${rows}<script>window.fixtureBytes=new Uint8Array(8*1024*1024);window.fixtureBytes.fill(7)</script></body></html>`, { headers: { "content-type": "text/html" } });
      });
    }
    return isolated;
  };
  win = new BrowserWindow({ show: false, width: 1000, height: 800, title: "Gogosling isolated browser measurement", webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false } });
  await win.loadURL("data:text/html,<title>Gogosling isolated browser measurement</title><p>Local browser resource measurement</p>");
  win.showInactive();
  browser = new WorkspaceBrowser({ window: () => win, state: () => { ++events; }, confirmDiscard: () => false });
  instrument(browser);
  await pause(1500);
  await snapshot("empty_before");
  const count = publicMode ? publicPages.length : 30;
  for (let index = 0; index < count; ++index) {
    const url = publicMode ? publicPages[index] : `https://example.com/fixture/${index % 4}`;
    try { await switchPage(index, url); }
    catch (error) {
      if (!publicMode) throw error;
      failures.push({ index, origin: new URL(url).origin, code: String(error.message) });
    }
    if (index === 0 || (index + 1) % 5 === 0 || publicMode) { await snapshot(`loaded_${index + 1}`); console.log(JSON.stringify({ phase: `loaded_${index + 1}`, mode: publicMode ? "public" : "synthetic" })); }
    await pause(75);
  }
  if (!publicMode) {
    const current = browser.current();
    const samePage = [];
    for (let index = 0; index < 30; ++index) {
      const started = performance.now();
      await browser.show({ tabId: current.tabId, url: current.url, bounds: { x: 10, y: 60, width: 950, height: 690 } });
      samePage.push(performance.now() - started);
    }
    report.operations.push({ operation: "same page layout refresh, no navigation", ...summarize(samePage) });
    browser.hide();
    await snapshot("hidden_retained_for_unsaved_content");
  }
  const closeStarted = performance.now();
  assert.equal(await browser.closeForQuit(), true);
  report.operations.push({ operation: "close last page", duration_ms: round(performance.now() - closeStarted) });
  await snapshot("closed_0s");
  await pause(5000);
  await snapshot("closed_5s");
  await pause(10000);
  await snapshot("closed_15s");
  if (!publicMode) {
    for (let index = 0; index < 5; ++index) {
      await switchPage(index, `https://example.com/reopen/${index}`);
      assert.equal(await browser.closeForQuit(), true);
    }
    await pause(1000);
    await snapshot("after_five_reopen_close_cycles");
  }
  browser.dispose();
  await pause(250);
  await snapshot("disposed_250ms");
  const visibleFrames = report.measurements.slice(0, count).map(item => item.two_frame_ack_ms).filter(value => value !== null);
  report.summary = { switches: summarize(report.measurements.slice(0, count).map(item => item.did_finish_load_ms)),
    two_frame_ack: visibleFrames.length ? summarize(visibleFrames) : null,
    show_return: summarize(report.measurements.slice(0, count).map(item => item.show_return_ms)) };
  writeFileSync(output, JSON.stringify(report, null, 2) + "\n", { mode: 0o600 });
  console.log(JSON.stringify({ label, mode: publicMode ? "public" : "synthetic", summary: report.summary, operations: report.operations, failures }));
  cleanup(failures.length ? 1 : 0);
}).catch(error => { console.error(error); cleanup(1); });
setTimeout(() => { console.error("performance_timeout"); cleanup(2); }, 160000).unref();
