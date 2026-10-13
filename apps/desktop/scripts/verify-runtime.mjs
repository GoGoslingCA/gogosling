// 将产物复制到新路径后验证：空 PATH、无 uv/系统 Python、真实工作进程和本地假模型。
import * as fs from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { createInterface } from "node:readline";
import { once } from "node:events";
import { fileURLToPath } from "node:url";
import { assertContainedLinks, isolatedEnv, nativeTarget, pythonRelative, run, stagedRoot } from "./runtime-lib.mjs";

async function stop(child) {
  if (!child.pid || child.exitCode !== null || child.signalCode !== null) return;
  const closed = once(child, "close");
  child.kill();
  const timer = setTimeout(() => child.kill("SIGKILL"), 2000);
  try { await closed; } finally { clearTimeout(timer); }
}

async function workerSmoke(python, cwd, env) {
  let modelRequests = 0;
  const server = createServer((req, res) => {
    if (req.url !== "/v1/chat/completions") { res.writeHead(404).end(); return; }
    modelRequests++;
    req.resume();
    res.writeHead(200, { "content-type": "text/event-stream" });
    res.end('data: {"choices":[{"delta":{"content":"runtime-ok"},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n');
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const worker = spawn(python, ["-m", "gogosling_worker"], { cwd, env, stdio: ["pipe", "pipe", "ignore"] });
  const closed = once(worker, "close");
  closed.catch(() => {});
  const lines = createInterface({ input: worker.stdout });
  let nextID = 1;
  const pending = new Map();
  let finish, fail;
  const terminal = new Promise((resolve, reject) => { finish = resolve; fail = reject; });
  // 错误可早于 run.start；先安装处理器避免未处理拒绝。
  terminal.catch(() => {});
  lines.on("line", raw => {
    let message;
    try { message = JSON.parse(raw); } catch {
      const error = new Error("Worker returned invalid protocol JSON");
      for (const p of pending.values()) p.reject(error);
      fail(error);
      return;
    }
    if (!message || typeof message !== "object") {
      const error = new Error("Worker returned an invalid protocol message");
      for (const p of pending.values()) p.reject(error);
      fail(error);
      return;
    }
    if (message.id && pending.has(message.id)) {
      const { resolve, reject } = pending.get(message.id); pending.delete(message.id);
      if (message.error) reject(new Error(`Worker RPC failed: ${message.error.code}`)); else resolve(message.result);
    }
    if (message.method === "run.event") {
      const event = message.params?.event;
      if (!event || typeof event !== "object" || typeof event.type !== "string") {
        const error = new Error("Worker returned an invalid run event");
        for (const p of pending.values()) p.reject(error);
        fail(error);
        return;
      }
      if (event.type === "run.completed") finish(event);
      else if (["run.failed", "run.cancelled"].includes(event.type)) fail(new Error(`Worker smoke ended with ${event.type}`));
    }
  });
  const rpc = (method, params) => new Promise((resolve, reject) => {
    const id = nextID++; pending.set(id, { resolve, reject });
    worker.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
  });
  const deadline = setTimeout(() => { for (const p of pending.values()) p.reject(new Error("Worker smoke timed out")); fail(new Error("Worker smoke timed out")); worker.kill("SIGKILL"); }, 15000);
  worker.on("error", error => { for (const p of pending.values()) p.reject(error); fail(error); });
  try {
    const hello = await rpc("worker.hello", { protocol_version: "1", locale: "en" });
    if (hello.protocol_version !== "1") throw new Error("Unexpected worker protocol");
    await rpc("run.start", { run_id: "package-smoke", session_id: "package-smoke", agent: { id: "package-smoke" }, provider: { kind: "openai_compatible", base_url: `http://127.0.0.1:${server.address().port}/v1`, model: "synthetic-package-check" }, messages: [{ role: "user", parts: [{ type: "text", text: "Synthetic packaging check" }] }], tools: [], options: { max_steps: 1 } });
    const event = await terminal;
    if (modelRequests !== 1 || !JSON.stringify(event.message).includes("runtime-ok")) throw new Error("Bundled inference did not complete");
    worker.stdin.end(JSON.stringify({ jsonrpc: "2.0", method: "worker.shutdown" }) + "\n");
    const [code] = await closed;
    if (code !== 0) throw new Error("Worker shutdown failed");
  } finally {
    clearTimeout(deadline); lines.close(); await stop(worker); server.close(); await once(server, "close");
  }
}

async function daemonSmoke(root, cwd, env) {
  const home = path.join(cwd, "daemon-data");
  const daemon = spawn(path.join(root, "bin", process.platform === "win32" ? "gogoslingd.exe" : "gogoslingd"), ["-home", home, "-listen", "127.0.0.1:0"], { cwd, env: { ...env, GOGOSLING_SECRET_BACKEND: "file" }, stdio: "ignore" });
  let childError;
  daemon.on("error", error => { childError = error; });
  try {
    const deadline = Date.now() + 15000;
    while (Date.now() < deadline) {
      if (childError) throw childError;
      if (daemon.exitCode !== null) throw new Error("Bundled daemon exited during startup");
      try {
        const handle = JSON.parse(await fs.readFile(path.join(home, "daemon.json"), "utf8"));
        const response = await fetch(`${handle.endpoint}/api/v1/version`, { headers: { Authorization: `Bearer ${handle.token}` }, signal: AbortSignal.timeout(1000) });
        const version = await response.json();
        if (response.ok && version.worker?.status === "ready") {
          const closed = once(daemon, "close");
          const shutdown = await fetch(`${handle.endpoint}/api/v1/system/shutdown`, { method: "POST", headers: { Authorization: `Bearer ${handle.token}` }, signal: AbortSignal.timeout(2000) });
          if (shutdown.status !== 202) throw new Error("Bundled daemon shutdown refused");
          const timer = setTimeout(() => daemon.kill("SIGKILL"), 7000);
          try { const [code] = await closed; if (code !== 0) throw new Error("Bundled daemon did not exit cleanly"); } finally { clearTimeout(timer); }
          return;
        }
      } catch (error) { if (error.message?.startsWith("Bundled daemon")) throw error; }
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    throw new Error("Bundled daemon did not discover its worker");
  } finally { await stop(daemon); }
}

export async function verifyRuntime(root = stagedRoot) {
  const target = nativeTarget();
  const manifest = JSON.parse(await fs.readFile(path.join(root, "runtime-manifest.json"), "utf8"));
  if (manifest.platform !== target.platform || manifest.arch !== target.arch) throw new Error("Runtime target does not match this host");
  await assertContainedLinks(root);
  const temporary = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), "gogosling relocated runtime ")));
  try {
    const moved = path.join(temporary, "renamed bundle");
    await fs.cp(root, moved, { recursive: true, verbatimSymlinks: true });
    const home = path.join(temporary, "isolated-home"); await fs.mkdir(home);
    const env = isolatedEnv(home);
    const python = path.join(moved, pythonRelative());
    const imports = JSON.parse(run(python, ["-B", "-I", "-c", 'import sys,ssl,sqlite3,httpx,certifi,gogosling_worker,json; print(json.dumps({"prefix":sys.prefix,"module":gogosling_worker.__file__,"tls":bool(ssl.OPENSSL_VERSION)}))'], { cwd: home, env }));
    if (!(await fs.realpath(imports.prefix)).startsWith(moved + path.sep) || !(await fs.realpath(imports.module)).startsWith(moved + path.sep) || !imports.tls) throw new Error("Runtime still depends on an external Python environment");
    await workerSmoke(python, home, env);
    await daemonSmoke(moved, home, env);
    return { platform: target.platform, arch: target.arch, relocated: true, isolated_path: true, mock_inference: true, daemon_worker_ready: true };
  } finally { await fs.rm(temporary, { recursive: true, force: true }); }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  verifyRuntime(process.argv[2] ? path.resolve(process.argv[2]) : stagedRoot).then(result => console.log(JSON.stringify(result))).catch(error => { console.error(error.message); process.exitCode = 1; });
}
