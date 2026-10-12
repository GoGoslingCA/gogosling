# 内容预览与发送区交付

## 实现结果

先完成[架构设计](../design/content-preview-architecture.md)，再按同一文件访问、格式分发、解析和展示接口实现。沿用 Go 守护进程、React 客户端及现有权限边界，没有新增数据库迁移或推理协议。

- 聊天附件、项目资料、工作区文件进入同一成果区。原来的成果编辑器保留挂载和草稿；查看文件时暂停其保存快捷键，关闭后仍可继续编辑。
- Markdown、文本、代码、HTML/SVG、DOCX、PPTX、XLSX、ODT/ODS/ODP、CSV/TSV、PDF、图片、音视频和 ZIP/TAR/GZIP 目录具有独立展示模块。新增同类格式只增加解析器和注册项，不修改聊天页面。
- Word 展示正文、标题、段内强调、表格和包内图片；演示文稿按实际幻灯片关系排序；表格保留坐标、共享字符串和缓存值。公式、宏及外部链接不执行。
- DOC/PPT/XLS/RTF 和 Office 版式使用可选的本地 LibreOffice 转换。macOS 下使用受限读取、仅临时目录可写、禁止网络的 Seatbelt 进程；优先级调低，单并发，30秒超时，取消清理临时材料与执行槽。
- PDF 使用按需加载的 PDF.js 独立 worker；只绘制当前页，单页画布最多400万像素，关闭终止任务。字体、CMap、图像解码器随客户端提供，不依赖 CDN；不打包脚本执行引擎。
- 长文本每页最多60,000字符，超长 Markdown 以分页源码阅读；文档每页100个内容块，文档表格及工作表每页100行。避免一次构造整份长文档的页面节点。
- 解析单并发、无等待队列；前端仅对短暂的 `preview_busy` 最多重试两次，每次120ms，不后台轮询。关闭/切换取消请求、释放 Blob URL；损坏文件只影响当前面板。

## 发送区设计与弹层修复

采用用户参考图的布局更合理：正文占据主要区域，附件、目录、审批、模型和发送集中在底部，减少上下两排条件与重复说明。附件、优化和发送使用既有图标，保留悬停说明、读屏名称和原快捷键；用量及历史资料归入“更多”。保留保存状态，审批规则沿用现有机制。

根因是工作目录、权限、模型等各自使用独立的原生 `details`，没有互斥状态，也没有外部点击/焦点移出处理。材料选择还直接删除 DOM 的 `open` 属性，关闭行为不统一。

改为一个顶层弹层状态；点击另一入口关闭旧弹层，再次点击同一入口收起。外部点击、焦点移出、Esc、窗口失焦、进入设置或切换任务/助手时关闭。Esc 将焦点还给触发入口，材料选择不会提交外层消息表单。嵌套说明、文件阅读器和审批对话框保持独立。

实际界面审查步骤：

1. 打开工作目录：旧版能展示目录说明，但占据输入区域。
2. 点击全部审批：旧版同时保留两个展开项，截图确认重叠；新版只保留权限弹层，展开数为1。
3. 按 Esc / 点击输入框：新版展开数均回到0。单元测试同时验证导航、材料引用、焦点及其他对话框。

公开展示截图见[界面素材](../assets/README.md)；其他临时验收截图不随仓库发布。

- `28-composer-directory-before.jpg`：工作目录。
- `29-composer-overlap-before.jpg`：两个弹层重叠。
- `30-composer-exclusive-after.jpg`：修复后互斥。
- `31-content-word-composer.jpg`：Word 表格/图片和新版底栏。
- `32-slides-compact-composer.jpg`：幻灯片翻页和窄内容列内的发送按钮对齐。
- `33-prompt-refinement-simple.jpg`：按需打开的修改意见弹窗。

截图不等于完整无障碍认证。本轮核验了键盘退出和焦点恢复；浏览器 viewport override 未实际改变1320px视口，未将该尝试计为1024px窗口验收。已实际检查成果区并排时约500px宽的聊天输入区。

## 接口

契约已写入 `api/openapi/openapi.yaml`。可选能力为 `workbench.preview: 1` 与 `workbench.preview_layout: 1`，旧内核不声明则隐藏入口。

```text
GET /api/v1/agents/{id}/files?path=...
GET /api/v1/agents/{id}/file-preview?path=...
GET /api/v1/agents/{id}/file-content?path=...&version=...
GET /api/v1/agents/{id}/file-layout?path=...&version=...

preview.Source.Open(context.Context, agentID, path string) (preview.File, error)
preview.Parser.Parse(context.Context, preview.Input) (preview.Document, error)
preview.Service.Preview(context.Context, agentID, path string) (preview.Document, error)
workspace.Workspace.OpenRead(context.Context, path string) (*os.File, error)
```

原文件走鉴权 API、禁止缓存、支持 Range，不把令牌放入 URL。工作区根句柄阻止路径穿越和越界符号链接，拒绝非普通文件。文件版本检查使用大小和修改时间；这是只读一致性检测，不替代 P3B 写入时的安全检查。

统一 `preview_*` 错误复用领域错误码及 zh/en 文案。底层异常路径和正文不回显；日志记录请求标识、格式、阶段、状态、耗时与大小。响应提供 `X-Preview-Id` 供排查。

## 验证结果

```bash
test -z "$(gofmt -l cmd internal)" && go vet ./... && go test ./...
npm run typecheck
npm test
npm run build
cd worker && uv run ruff check . && uv run ruff format --check . && uv run mypy && uv run pytest
make e2e
go test -race ./internal/session ./internal/workbench/preview ./internal/server
```

实际运行通过：全量 Go、桌面231项、Web360项（47个文件）、Python178项；真实 Python 工作进程与假模型的审批端到端通过。长文本分页、PDF任务终止和音视频卸载已纳入测试。最后生产构建成功，整个 Web 输出约5.55MB，其中 PDF 阅读模块约433KB、worker约1.26MB（均为未压缩大小），按需加载；不能把分包等同于已测得的节能百分比。

OpenAPI 57个路径、245处引用解析通过；生产资源中未发现内部提示词标记，未包含 QuickJS 执行引擎。仓库扫描未发现本次授权的真实 API 密钥，临时真实调用程序已清理。收尾修正了状态徽标可选 title 透传与草稿测试 mock 可选查询参数的类型兼容问题。

格式和安全测试包括：DOCX标题/表格/宏忽略、PPTX逻辑顺序、XLSX共享字符串/稀疏坐标/缓存值、ODF混合文字/超大重复数、UTF-16、CSV多行字段、ZIP目录、损坏/穿越/解压超限/XML指令、取消与并发槽、路径与符号链接、Range、文件变化、旧内核、HTML隔离、迟到响应、Blob释放、PDF终止、表格分页、幻灯片翻页。

真实本地文件调用记录在 [内容预览结果](2026-10-12-content-preview-api.json)。修复真实 Office 文件发现的 XML 文档外空白、关系命名空间和包内绝对路径兼容问题后，DOCX/PPTX/XLSX 均通过。一般小样本解析 API 约3–10ms，不代表大型文档或端到端界面耗时。

[Office版式结果](2026-10-12-content-layout-results.json)：DOC、DOCX、ODT、PPT、PPTX、ODP、XLS、XLSX、ODS 共9个真实文件均输出合法PDF，约0.98–1.23秒。另有真实 RTF 转换专项通过（约1.85秒）及取消时临时材料/槽位清理测试。

浏览器已实际查看 Markdown、HTML、Word、PPTX、XLSX 和 PDF；看到 Word 表格及内嵌图片，PPTX 可翻页，XLSX 可切换工作表。损坏 DOCX 报错后能继续打开正常表格。MP4实际加载640×360画面、时长1秒，WAV时长0.5秒，两者 readyState=4 且保持暂停；卸载时明确暂停、移除src、卸载媒体并回收Blob。归档任务仍能阅读文件，发送继续禁用。

提示词优化的一键回填、补充意见自动关闭、还原在隔离验收模型中通过，本次检查未点击发送；验收任务中用户此前已有消息未删除。真实网关质量测试另见[记录](2026-10-12-live-prompt-validation.md)。

## 明确的边界及待办

- 此次实现查看与展示，既有文本成果编辑、版本和 P3B 应用/撤销继续使用原接口；不宣称已实现 Office 全功能编辑。
- Office 内容阅读不保证复杂母版、图表对象、修订、域和数值样式与原应用一致；可用本地转换器查看版式。
- 当前旧 Office/版式转换适配器限 macOS，并要求安装或显式配置 LibreOffice。测试用转换器仅通过环境变量配置，未将本机运行时路径写进产品。Windows/Linux 保留内置格式阅读和下载，需后续增加等价隔离适配器。
- HTML/SVG 是静态隔离预览：脚本、远程/相对资源、表单和嵌套页面不会执行或加载。依赖它们的网页不是完整应用预览。
- RAR/7z、HEIC/TIFF、Apple iWork 等仅识别并保留下载；媒体是否可播放取决于系统编码器。不把附件可上传误写成所有格式都能完整渲染。
- 输入上限：文本2MiB、结构化内容32MiB、原文件64MiB；压缩成员/展开量、页面与单元格有额外预算。没有进行跨设备能耗或峰值内存比较。

本次内容预览与交互调整没有改变默认审批等级、用户密钥、数据库模式或已有任务身份。后续公开版本的验证见[发布记录](2026-10-12-public-release.md)。
