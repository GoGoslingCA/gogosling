# gogosling 顶层任务入口。
#
# 三套工具链各管一块，互不嵌套：
#   Go 内核（cmd/、internal/）      -> go
#   Python 工作进程（worker/）      -> uv
#   前端与桌面壳（apps/）           -> npm workspaces

GO  ?= go
UV  ?= uv
NPM ?= npm

BIN_DIR := bin
GO_DIRS := cmd internal

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

VERSION_PKG := github.com/GoGoslingCA/gogosling/internal/version
LDFLAGS := -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).Date=$(DATE)

.DEFAULT_GOAL := help

.PHONY: help
help: ## 显示可用目标
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-16s %s\n", $$1, $$2}'

# ---- 聚合目标 ----------------------------------------------------------------

.PHONY: build
build: go-build ## 构建 Go 二进制到 bin/

.PHONY: test
test: go-test worker-test apps-test ## 运行 Go、Python 与前端测试

.PHONY: lint
lint: go-lint worker-lint apps-typecheck ## 运行全部静态检查

.PHONY: fmt
fmt: ## 格式化 Go 与 Python 代码
	gofmt -w $(GO_DIRS)
	cd worker && $(UV) run ruff format .

.PHONY: clean
clean: ## 清理构建产物
	rm -rf $(BIN_DIR) apps/web/dist apps/desktop/dist

# ---- Go 内核 -----------------------------------------------------------------

.PHONY: go-build
go-build: ## 构建 gogoslingd 与 gogosling
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/ ./cmd/...

.PHONY: go-test
go-test: ## 运行 Go 测试
	$(GO) test ./...

.PHONY: go-lint
go-lint: ## gofmt 检查 + go vet
	@test -z "$$(gofmt -l $(GO_DIRS))" || (echo "以下文件需要 gofmt:"; gofmt -l $(GO_DIRS); exit 1)
	$(GO) vet ./...

.PHONY: dev-daemon
dev-daemon: ## 前台运行守护进程
	$(GO) run ./cmd/gogoslingd

# ---- Python 工作进程 ---------------------------------------------------------

.PHONY: worker-sync
worker-sync: ## 安装工作进程依赖（含开发依赖）
	cd worker && $(UV) sync

.PHONY: worker-test
worker-test: ## 运行工作进程测试
	cd worker && $(UV) run pytest

.PHONY: worker-lint
worker-lint: ## ruff + mypy
	cd worker && $(UV) run ruff check . && $(UV) run ruff format --check . && $(UV) run mypy

# ---- 前端与桌面壳 ------------------------------------------------------------

.PHONY: apps-install
apps-install: ## 安装前端与桌面壳依赖
	$(NPM) install

.PHONY: apps-typecheck
apps-typecheck: ## TypeScript 类型检查
	$(NPM) run typecheck

.PHONY: apps-test
apps-test: ## 前端与桌面壳的 vitest
	$(NPM) test

.PHONY: dev-web
dev-web: ## 启动 Web UI 开发服务器（代理到本地守护进程）
	$(NPM) run dev -w @gogosling/web

.PHONY: dev-desktop
dev-desktop: ## 启动 Electron 桌面壳（需先启动守护进程）
	$(NPM) run start -w @gogosling/desktop

# ---- 端到端测试与开发联调 -----------------------------------------------------

# DEV_HOME 是开发联调用的数据目录，与日常使用的 ~/.gogosling 分开。
DEV_HOME ?= $(HOME)/.gogosling-dev
DEV_ORIGINS ?= http://localhost:5173,http://127.0.0.1:5173

.PHONY: e2e
e2e: ## 端到端测试：进程内守护进程 + 真实工作进程（uv）+ 假模型服务；没有 uv 时跳过
	$(GO) test -tags e2e ./tests/e2e/ -count=1 -v

.PHONY: dev
dev: ## 前台运行守护进程：工作进程从源码启动（uv），数据目录 DEV_HOME，允许 Vite 开发服务器的 Origin
	GOGOSLING_DEV=1 GOGOSLING_HOME="$(DEV_HOME)" \
	GOGOSLING_WORKER_CMD="$(UV) run --project \"$(CURDIR)/worker\" gogosling-worker" \
	GOGOSLING_ALLOWED_ORIGINS="$(DEV_ORIGINS)" \
	$(GO) run ./cmd/gogoslingd -log-level debug

.PHONY: dev-cli
dev-cli: ## 用开发数据目录运行 CLI，例如 make dev-cli ARGS="agent list"
	GOGOSLING_HOME="$(DEV_HOME)" $(GO) run ./cmd/gogosling $(ARGS)
