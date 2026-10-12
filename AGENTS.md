# AGENTS.md — 贡献者与 AI 代理工作手册

本文件面向在本仓库中工作的人类与 AI 代理。架构本身见 [docs/architecture.md](docs/architecture.md)；本文只讲"怎么在这里干活"。

## 1. 工作原则

- 先读 `docs/architecture.md` 的依赖规则与安全不变量，再动手；实现不得与之冲突。
- 只改与任务直接相关的文件；不顺手重构、不统一风格、不删除别人的待办注释。
- 不确定的接口形状：先看 `internal/model`（类型、错误码、仓储接口）与 `api/` 下的契约，再看同层包的用法；仍不确定就在交付说明里写明假设，不要静默发明第二套类型。
- 共享文件（`internal/model/*`、`go.mod`、`Makefile`、`AGENTS.md`、`docs/architecture.md`、`api/*`）属于架构负责人；需要改动时在交付说明中提出，不要直接改。例外：`go get` 批准列表内的依赖。
- 任何面向用户的文字走 `internal/i18n`（内核）或 `apps/web/src/locales`（界面）；代码里不硬编码中文或英文提示。
- 密钥、令牌、API key 不进日志、不进数据库、不进事件、不进测试固件。

## 2. 工具链

先安装 Go、Node 和 uv，并确认工具位于 PATH。Go 和 uv 使用用户目录安装时，可加入：

```bash
export PATH="$HOME/.local/go/bin:$HOME/.local/bin:$PATH"
```

| 栈 | 版本 | 验证命令（在仓库根目录） |
|---|---|---|
| Go | 1.26（`go.mod` 声明 1.26.0） | `test -z "$(gofmt -l cmd internal)" && go vet ./... && go test ./...`（`gofmt -l` 有输出即未格式化） |
| Python | 3.12，uv 管理 | `cd worker && uv sync && uv run ruff check . && uv run ruff format --check . && uv run mypy && uv run pytest` |
| Node | 22 LTS，npm workspaces | `npm install && npm run typecheck && npm test && npm run build` |
| 端到端 | 需要 uv | `make e2e`（真实 Python 工作进程 + 假模型服务） |

`make lint`（gofmt/vet/ruff/mypy/tsc）、`make test`（Go + Python + vitest）、`make build`（Go 二进制）、`make e2e` 聚合了上面的命令；`make dev` 启动源码开发态的守护进程（数据目录 `DEV_HOME`，工作进程走 `uv run`，放行 Vite 的 Origin）。交付前必须贴出实际运行的命令与结果；没跑的要说明原因。

## 3. Go 约定

- 包布局与依赖方向：见架构文档 §3、§4。新包必须有 `doc.go` 或在主文件顶部写包注释，说明职责、待实现项与边界。
- 批准的第三方依赖（已在 `go.mod`）：`modernc.org/sqlite`、`github.com/modelcontextprotocol/go-sdk`、`github.com/robfig/cron/v3`、`github.com/zalando/go-keyring`、`golang.org/x/sys`、`golang.org/x/sync`。需要别的依赖先提出，不要先加。
- 不要运行 `go mod tidy`（会与并行工作冲突）；集成时由架构负责人统一执行。
- 错误：领域错误用 `model.NewError / Errorf / Wrap / NotFound` 携带 `model.Code`；向上返回时用 `fmt.Errorf("...: %w", err)` 保留链。不要 `panic`（`model.NewID` 的随机源失败除外）。
- 上下文：所有可能阻塞或做 I/O 的函数第一个参数是 `ctx context.Context`，并尊重取消。
- 并发：导出类型若并发安全要在注释里说明；共享状态用互斥锁或通道，不用全局变量；长生命周期 goroutine 必须能被 `ctx` 停止。
- 日志：用 `*slog.Logger` 由调用方注入；键值对小写下划线；不打印密钥与完整请求体。
- 时间：内部 UTC；格式化给用户时才转时区。
- 文件权限：数据目录内新建文件 `0o600`、目录 `0o700`。
- 测试：`go test` 标准库即可，不引入断言库；用 `t.TempDir()`、`t.Setenv`、`httptest`；不访问真实网络；跨平台（Linux、macOS、Windows）都要能跑，POSIX 专有行为用 `runtime.GOOS` 跳过；测试名与失败信息用中文描述期望。
- 注释：标识符英文，注释中文；导出符号必须有注释。

## 4. Python 约定（worker/）

- 严格类型（`mypy --strict`），`ruff` 的 `E F I B UP` 规则；行宽 100。
- 只用 stdout 发协议消息，日志走 stderr（`logging` 配置到 stderr）。
- 不读写数据目录、不监听端口、不持久化密钥；凭据只在内存中、只在使用点使用。
- 依赖加到 `pyproject.toml` 的 `dependencies`，并说明用途；优先标准库与 `httpx`。
- 测试用 `pytest`，网络用本地假服务器（`http.server` 或 `httpx.MockTransport`）。

## 5. 前端约定（apps/）

- TypeScript `strict`；React 函数组件 + hooks；状态用 `@tanstack/react-query` 管远端数据，本地 UI 状态用 `useState`/`useReducer`。
- 所有网络访问经 `apps/web/src/api/`，类型与 `api/openapi/openapi.yaml` 一致（手写或生成，但要注明来源）。
- 文案放 `apps/web/src/locales/{zh,en}.json`，默认中文。
- Electron：`contextIsolation: true`、`nodeIntegration: false`、`sandbox: true`；preload 只暴露最小 IPC；令牌只经 IPC 传入渲染进程内存，不写 `localStorage`。

## 6. 提交与交付

- 提交信息：`<type>(<scope>): <中文摘要>`，type ∈ feat / fix / refactor / docs / test / chore / build / ci；例：`feat(store): 实现 SQLite 仓储与迁移`。
- 每个任务的交付说明包含：实现了什么、公开接口（签名）、运行过的验证命令与输出摘要、与契约或架构文档的偏差及理由、留下的待办。
- 不提交构建产物、`node_modules`、`.venv`、本地配置、任何密钥。
- 本机发布提交使用 `Gogosling.ca` 项目署名，不自动附加个人资料或工具联名页脚。
