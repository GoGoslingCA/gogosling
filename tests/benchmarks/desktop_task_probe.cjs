// 真实桌面入口的只读测量包装：UI 操作由操作者完成，不修改审批或保存逻辑。
// 在隔离复制的 apps/desktop/package.json 中把 main 指向此文件，再启动 Electron。
// 环境须提供独立 userData、报告文件、阶段文件与原始编译入口；不连接真实用户目录。
const { app, BrowserWindow } = require("electron");
const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");
const { execFileSync } = require("node:child_process");

const profile = process.env.GOGOSLING_BENCH_PROFILE;
const output = process.env.GOGOSLING_BENCH_OUTPUT;
const phasePath = process.env.GOGOSLING_BENCH_PHASE;
const main = process.env.GOGOSLING_BENCH_DESKTOP_MAIN;
const dataHome = process.env.GOGOSLING_HOME;
if (!profile || !output || !phasePath || !main || !dataHome) throw new Error("Isolated benchmark paths are required");
const defaultHome = path.join(os.homedir(), ".gogosling");
const resolvedDefault = fs.existsSync(defaultHome) ? fs.realpathSync(defaultHome) : defaultHome;
if (fs.realpathSync(dataHome) === resolvedDefault) throw new Error("Do not measure against the default user data directory");
app.setPath("userData", path.resolve(profile));
// 测试实例不注册操作系统深链处理器，以免改变已安装应用的归属。
app.setAsDefaultProtocolClient = () => true;
const started = performance.now();
const report = {
  label: process.env.GOGOSLING_BENCH_LABEL ?? "desktop",
  electron: process.versions.electron, chromium: process.versions.chrome,
  platform: process.platform, arch: process.arch,
  method: "Production desktop main, preload, app protocol and built React client; operator-driven UI. No forced GC. RSS sums may double-count shared pages. Read-only telemetry uses the same interval in each build. Cumulative event records and synchronous sampling add main-process overhead; compare equal observation windows and report this limitation.",
  samples: [], phaseChanges: [], failures: [],
};
let phase = "startup", lastPhase = "", sampling = false, tick;
const write = () => fs.writeFileSync(output, JSON.stringify(report, null, 2) + "\n", { mode: 0o600 });
const instrumentation = `(() => {
  if (window.__desktopTaskProbe) return;
  const p = window.__desktopTaskProbe = { inputs: [], longTasks: [], navigation: [] };
  new PerformanceObserver(list => {
    for (const entry of list.getEntries()) p.longTasks.push({ at: entry.startTime, duration: entry.duration });
    if (p.longTasks.length > 2000) p.longTasks.splice(0, p.longTasks.length - 2000);
  }).observe({type: 'longtask', buffered: true});
  document.addEventListener('input', event => {
    if (!(event.target instanceof HTMLTextAreaElement)) return;
    const at=performance.now(), length=event.target.value.length;
    requestAnimationFrame(() => requestAnimationFrame(() => {
      p.inputs.push({at,length,ms:performance.now()-at});
      if(p.inputs.length>200) p.inputs.shift();
    }));
  }, true);
  window.addEventListener('hashchange', () => {
    const at=performance.now();
    requestAnimationFrame(() => requestAnimationFrame(() => p.navigation.push({at,ms:performance.now()-at})));
  });
})()`;
app.on("browser-window-created", (_event, window) => {
  const contents = window.webContents;
  contents.on("did-finish-load", async () => {
    if (!contents.getURL().startsWith("app://gogosling/")) return;
    try {
      if (!contents.debugger.isAttached()) contents.debugger.attach("1.3");
      await contents.executeJavaScript(instrumentation);
    } catch { report.failures.push("telemetry_install_failed"); }
  });
});
async function sample() {
  if (sampling) return;
  sampling = true;
  try {
    try { phase = fs.readFileSync(phasePath, "utf8").trim().slice(0, 80) || "unmarked"; } catch {}
    const elapsedMs = performance.now() - started;
    if (phase !== lastPhase) { report.phaseChanges.push({phase,elapsedMs}); lastPhase = phase; }
    const metrics = app.getAppMetrics();
    const groups = {};
    for (const item of metrics) {
      const group = groups[item.type] ??= { processes: 0, rssKiB: 0, workingSetKiB: 0 };
      group.processes++; group.workingSetKiB += item.memory.workingSetSize;
      try {
        const rss = Number(execFileSync("ps", ["-o", "rss=", "-p", String(item.pid)], { encoding:"utf8",timeout:1000 }).trim());
        if (Number.isFinite(rss)) group.rssKiB += rss;
      } catch { group.rssUnavailable = true; }
    }
    const contents = BrowserWindow.getAllWindows().find(window => !window.isDestroyed())?.webContents;
    let renderer = null;
    if (contents && !contents.isDestroyed() && contents.getURL().startsWith("app://gogosling/")) {
      const heap = contents.debugger.isAttached() ? await contents.debugger.sendCommand("Runtime.getHeapUsage") : null;
      const state = await contents.executeJavaScript(`(() => {
        const p=window.__desktopTaskProbe;
        const input=document.querySelector('textarea');
        return { heapLimit:performance.memory?.jsHeapSizeLimit, nodes:document.querySelectorAll('*').length,
          toolCards:document.querySelectorAll('.tool-card').length, inputLength:input?.value.length??0,
          visibility:document.visibilityState, inputs:p?.inputs??[], longTasks:p?.longTasks??[], navigation:p?.navigation??[] };
      })()`);
      renderer = { heap, ...state };
    }
    report.samples.push({ elapsedMs, phase, processGroups:groups, renderer });
    if (report.samples.length > 3600) report.samples.shift();
    write();
  } catch { report.failures.push("sample_failed"); write(); }
  finally { sampling = false; }
}
app.whenReady().then(() => { tick = setInterval(() => void sample(), 1000); });
app.on("will-quit", () => { clearInterval(tick); write(); });
require(path.resolve(main));
