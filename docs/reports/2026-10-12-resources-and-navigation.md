# 资源调度、工具规范与导航更新

日期：2026-10-12。本轮为本地未提交改动；没有提交、推送或替换用户的正式数据目录。P3B 的外部冲突、恢复及旧模式迁移证据沿用 [P3B 交付记录](2026-10-12-p3b-delivery.md)，本轮全量 Go 检查再次通过。

## 完成内容

- 主导航聚焦新建、查找任务、项目、定时任务和最近任务。移除重复的开始链接，品牌和新建按钮仍到达开始页。
- 智能体、模型连接、技能与扩展、消息连接、操作记录收进设置，原管理组件直接在页内操作；保留表单、权限和审批行为。组件及查询按需载入，未打开的管理模块不常驻。
- 设置分组写入 URL，支持搜索、方向键、刷新和前进后退；五个旧路由保留并自动定位新分组。返回工作台保留原任务及成果参数。老的设备偏好继续可读，不删除历史、草稿和成果。
- 全局默认2个并行运行，可调1–8；最多32个等待项，同一解析后的工作区串行。用户请求优先于定时任务，久候任务提升优先级；等待审批占槽，调低并行数不终止在途工作。
- 等待中的运行不装载完整历史、不创建 worker 任务、不保留凭据副本；派发时重新检查可用性。排队取消、启动失败、关闭服务、同工作区竞争、动态上限、重启配置读取均有验证。
- 实时状态按会话订阅，文本/思考增量合批；关键状态立即处理，Seq 重放去重，内核重启后重置序号。只回收确认落库的非当前终态缓存，未保存内容保留。
- 历史消息与工具结果索引复用，折叠内容按展开挂载，历史抽屉打开后再查询。
- 工具提示词样本改写为 gogosling 实际工具和权限约束，中英文运行时追加，保留已保存的用户人格。不注入外部身份、固定模型、外部目录或不存在的工具。原始样本16171字节，不整份注入；新增指导文本受2200字节上限测试约束。
- 产品短句更新为“本地工作，轻装前行。”；技术架构、鉴权链和成果存储位置不变。

## 接口与兼容

新增可选能力 `runtime.resources: 1`，旧内核不展示入口：

```text
GET /api/v1/resources
PUT /api/v1/resources  {"max_parallel": 2}
```

响应为 `max_parallel / max_queued / running / queued / awaiting_approval`。鉴权沿用本地 API；上限保存到现有 settings 表，无新增数据库迁移。领域层公开 `Resources() ResourceState` 和 `SetParallelLimit(int) error`。领域零配置保留原调用行为，桌面组合根启用默认上限。

旧 `/agents /providers /extensions /channels /audit` 分别重定向至 `/settings?section=agents|models|extensions|channels|audit`，保留其他查询参数和返回上下文，以 replace 避免后退中转循环。用户不需要重新创建助手、模型或授权。

## 实际验证命令与结果

按 AGENTS 的 PATH，端到端另可使用本机 uv：

```bash
make lint
make test
make e2e
make build
npm run build
go test -race ./internal/session
npm run typecheck
npm test --workspace @gogosling/web
npm test --workspace @gogosling/web -- src/pages/SettingsPage.test.tsx src/pages/SettingsRedirect.test.tsx src/layout/NavigationSidebar.test.tsx
```

- Go vet、全包测试、架构检查通过；队列 race 检查通过。
- Python Ruff、格式、Mypy 通过，176项测试通过。
- 桌面231项测试通过；Web 在入口收拢后全量333项通过，后续返回上下文补充及按需查询调整对应30项专项通过（新增2项）。
- 真实 Python worker + 本地假模型 + 工具审批的端到端通过。Go与桌面/Web构建通过，管理模块继续分包。
- `liveRunStore.test.ts` 合成8000条双会话交错增量：两边各4000字符完整保留，每个会话80次通知；单独后台增量不通知当前会话。另覆盖批量去重、审批即时刷新、缓冲上限、未保存失败文本保留、重启序号和定时器清理。
- 浏览器验证：资源上限2→1→2真实保存；设置内模型列表、新建表单与取消正常；900×760下新导航可滚动切换，页面无横向溢出。预览守护进程已用新构建启动。
- 文档检查最终使用仓库已有 `js-yaml`，52个路径、55个schema、232处引用解析通过；系统 Python 缺 PyYAML，macOS 自带旧 Ruby YAML 不接受既有 flow scalar 写法，未把这两次环境/parser失败算作契约通过。

## 研究与边界

[开源调研](../research/2026-10-12-open-source-reference.md)逐条记录架构、Issue/PR当前状态、影响、具体建议及已落地部分；[证据快照](../research/opensource-evidence.json)保留查询日期。阅读公开源码与讨论，没有向项目发 Issue、PR 或评论。

本轮未引入新运行依赖，没有复制完整竞品架构。显示通知次数下降不等于能耗/RSS下降比例；尚未做同设备同任务的长时间硬件能耗对比。未来的多 Agent 自动委派须共享预算，并避免父任务等待子任务时占槽死锁；本轮没有新增委派工具。磁盘统计、成果去重、真实挂流连接验收列入研究中的下一批建议。

界面截图在当前聊天的可视化目录：`18-resource-settings.jpg`、`19-settings-connections.jpg`、`20-settings-navigation-narrow.jpg`、`21-settings-final.jpg`。预览使用隔离数据和本地假模型，不能把预览结果等同于用户正式 API 的全面兼容验证。

## 聊天焦点与字体跟进

收到聊天区蓝框与字体反馈后，定位到全局 `.textarea:focus` 和输入容器 `:focus-within` 同时生效，形成内外两层蓝框。调整仅涉及 CSS：输入文本域不再叠加外框，容器使用一层细灰边；滚动阅读区和输入区内按钮保留中性键盘焦点。全局字体统一为本机系统 UI 字体栈，中文使用系统中文字体回退，不下载远程字体；聊天正文和输入15px，正文行高27px，标题600字重，段落及列表增加间距，代码继续使用等宽字体。

实际执行 `npm run typecheck`、`npm run build --workspace @gogosling/web`、相关文件 `git diff --check`，均通过。浏览器点击文本域确认 outline 与 shadow 均为 none、外框为中性灰；按 Tab 到附件按钮确认仍有2px灰色焦点。只改展示，不新增 API 或数据迁移；按低影响样式变更处理，未新增或重跑与此无关的单元测试。截图：`22-chat-typography-focus.jpg`。
