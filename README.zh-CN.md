# gogosling

**本地工作，轻装前行。** 开源 AI 工作台，把文件、任务和可检查的修改放在一起。

[English](README.md) · 简体中文 · [快速开始](docs/development/local-run.md) · [贡献指南](CONTRIBUTING.md) · [路线图](ROADMAP.md)

由加拿大公司 [Gogosling.ca](https://gogosling.ca) 研发。欢迎全球开发者提交 PR，一起推动加拿大开源 AI 发展。

> 当前为开发预览版，从源码运行。正式安装包和签名发布仍在规划中。

![gogosling 英文开始页](docs/assets/start-en.jpg)

## 从想法到成果

1. 加入文件，选择本次任务要参考的资料。
2. 一边对话，一边查看文档、代码或演示文稿；发送前可优化提示词。
3. 检查草稿、保存版本或导出副本；修改原文件前先预览，再按权限审批应用，也可撤销。

项目、草稿、任务历史和成果版本保存在本地。Go 守护进程管理状态、权限与调度；独立 Python 工作进程负责推理；桌面壳、Web 和 CLI 共用本地 API。

## 当前能力与边界

- 默认最多 2 个任务运行、32 个任务排队；并发可调，同工作区串行，取消后回收本地资源。
- 文档模块按需加载，解析有大小、时间与并发限制，关闭后释放资源。
- 阅读 Markdown、代码、静态 HTML/SVG、PDF、图片、常见音视频、CSV/TSV、现代 Office、OpenDocument 和压缩包目录。
- 草稿冲突检查、不可变成果版本，以及文本文件修改预览、应用和撤销。
- OpenAI 兼容和 Anthropic 模型服务、兼容的本地模型、MCP 与扩展。
- 中文、英文和法文界面；工具、模型和连接统一放在设置中。

Office 预览以内容阅读为主。旧 Office 与版式转换目前需要 macOS 上可选的 LibreOffice 沙箱转换；不支持的格式保留下载。原位文件修改目前支持 Linux/macOS，Windows 可预览和导出。

“本地优先”指数据保存与任务编排；使用远程模型时，本次上下文仍会交给模型服务商。性能优势需要同负载实测，当前不宣称相较其他产品节省了多少内存或电量。

## 开始使用

需要 Go 1.26、Node 22.12+、Python 3.12、uv、Git 与 make。macOS/Linux 使用 Bash；Windows 可用 WSL2 运行开发环境。

```bash
git clone https://github.com/GoGoslingCA/gogosling.git
cd gogosling
npm ci
uv sync --project worker --frozen --python 3.12
make dev
```

另一个终端在仓库根目录运行：

```bash
npm run dev -w @gogosling/web -- --strictPort
```

打开 `http://127.0.0.1:5173`，连接地址留空，令牌取自本机 `~/.gogosling-dev/daemon.json`，只填写在本机连接页，不要分享该文件。进入「设置 → 模型」配置服务，再配置智能体并开始任务。

完整步骤见 [本地运行指南](docs/development/local-run.md)；使用流程见 [个人工作台说明](docs/user/personal-workbench.md)。

## 一起维护

欢迎 PR、复现报告、翻译和文档修订。第一次参与可以从 [贡献指南](CONTRIBUTING.md#good-places-to-start) 选择小任务；愿意长期维护模块，也欢迎在 [Discussions](https://github.com/GoGoslingCA/gogosling/discussions) 介绍方向。

[架构](docs/architecture.md) · [API 契约](api/openapi/openapi.yaml) · [文档索引](docs/README.md) · [安全报告](SECURITY.md) · [MIT 许可证](LICENSE)
