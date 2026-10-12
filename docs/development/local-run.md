# 本地开发运行

需要 Go 1.26、Node 22（至少 22.12）、Python 3.12、uv 和 make。以下命令在仓库根目录用 Bash 执行，适用于 macOS / Linux；Windows 可在 WSL2 中运行 Web 开发环境。仓库不提供预构建二进制，请使用本机新构建的 `bin/` 文件。English: [Quickstart](quickstart.md)。

## 安装与构建

```bash
npm ci
uv sync --project worker --frozen --python 3.12
make build
npm run build
```

## 启动 Web 开发环境

终端一：

```bash
make dev
```

默认数据目录为 `$HOME/.gogosling-dev`，与日常使用的 `.gogosling` 分开。守护进程绑定 `127.0.0.1:46750`，真实 Python worker 从源码启动。终端应出现 worker ready 状态；uv 必须在 PATH 中。

终端二：

```bash
npm run dev -w @gogosling/web -- --strictPort
```

在本机浏览器打开 `http://127.0.0.1:5173`。开发态连接页的地址留空会使用 Vite 代理；令牌取自本机 `$HOME/.gogosling-dev/daemon.json` 的 `token` 字段，只填入本机连接页。界面不会把令牌写入浏览器持久存储。

启动服务不需要模型 API 密钥。实际对话前，在“模型供应商”中配置可用模型，再创建或配置智能体，然后新建会话。

## 启动 Electron 桌面壳

保持终端一的守护进程运行，构建完成后在另一终端执行：

```bash
GOGOSLING_HOME="$HOME/.gogosling-dev" npm run start -w @gogosling/desktop
```

桌面壳会复用同一数据目录里的守护进程，通过内置代理注入认证，使用已构建的 Web 界面。Web 代码变更后重新执行 `npm run build -w @gogosling/web`。Electron 图形界面和安装包仍需在你的本机验证；云端已验证的是桌面管理逻辑与 Chromium Web 交互。

## 检查与停止

```bash
bin/gogosling --home "$HOME/.gogosling-dev" daemon status
bin/gogosling --home "$HOME/.gogosling-dev" daemon stop
```

Vite 使用其运行终端的 Ctrl-C 停止。若自定义 `DEV_HOME`，桌面壳的 `GOGOSLING_HOME` 和 CLI 的 `--home` 也使用同一目录。

验证命令：

```bash
make lint
make test
make build
npm run build
make e2e
```

已完成功能、实际测试结果和未完成范围见 [个人工作台说明](../user/personal-workbench.md)。Linux bwrap 需要宿主允许 user namespace；辅助程序存在不等于隔离可用，启动日志会显示实际选择的机制。
