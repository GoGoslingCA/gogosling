# 提示词优化：真实网关验收与交互简化

## 已完成

默认点击即优化并回填，提供“还原”。需要进一步修改时才打开单输入弹窗，填写意见后直接更新输入。保持原始文字、既有附件、草稿保存和发送方式；优化不会发送聊天消息。请求绑定草稿版本、任务和助手，手动修改、切换、发送或取消后，迟到结果不能回填。

接口仍为 `POST /api/v1/agents/{id}/prompt-enhancement`，新增可选 `draft`、`feedback`；旧的 `{text}` 调用兼容。Go 接口为：

```go
EnhancePrompt(ctx context.Context, agentID, input string) (string, error)
EnhancePromptWithFeedback(ctx context.Context, agentID, input, draft, feedback string) (string, error)
```

内部指导词留在 Go 内核。输入使用结构化边界并增加中英混合示例，禁止直接回答、编造邮件/代码或丢弃原约束。回归保护检查已知答题前缀、中文/英文明显错位与数字约束；这不是完整的语义证明，用户仍可还原和调整。

## 真实调用结果

使用用户已授权的 `https://ai.gogosling.ca/v1`、`auto:fast`，通过实际本地 HTTP → Go app/session → Python worker → 网关调用。不是只测 curl，也不是用假模型代替质量结果。凭据只通过 stdin 进入临时进程的内存 secret store，未写入配置、数据库、日志、事件或固件；临时实例结束后清理。

首次测试发现直接答题/虚构邮件、一次超时，以及 TLS 握手取消后遗留连接。保留[基线](2026-10-12-live-prompt-baseline.json)，没有把首次 HTTP 200 当作验收通过。基线第一条取消样本没有观察到上游连接，不能计为有效取消测试。

修正后的[8个样例](2026-10-12-live-prompt-results.json)：

- 中文代码说明：2.73秒，保留中文、200字、不改代码。
- 中文周报：15.27秒，保留两部分、不编造、待补充。
- 英文代码说明：11.90秒，保留英文、120 words、不修改和边界情况。
- 英文邮件：1.12秒，保留英文、礼貌直接、姓名日期数字承诺不变，返回的是优化要求。
- 中英混合代码：15.84秒，保留自然混合、根因、最小修复、不改接口/不加依赖。
- 中英混合清单：12.05秒，保留术语、6条上限、不虚构负责人/日期。
- 中文补充意见：16.63秒，按意见将200字改为100字，只讲主要功能且不改代码。
- 混合补充意见：3.38秒，保留混合表达、不增依赖，加入最小回归测试。

这是一轮样例结果，不代表所有模型和未来请求都能保证语义等价。耗时是请求到完整候选的端到端时间；没有把它称为首字延迟或 p95。

## 取消及资源释放

定位到已安装的 httpcore 1.0.9：AnyIO `start_tls` 仅捕获 Exception，Python 任务取消属于 BaseException。握手中的底层流尚未移交连接池，单纯关闭客户端可能留有 socket。通过 HTTPX 公开 trace 扩展追踪本次建立的流，并在异常/取消时有界关闭；未修改第三方包或使用全局猴子补丁。

参考 [httpcore trace 扩展](https://github.com/encode/httpcore/blob/master/docs/extensions.md)，相关取消讨论包括 [#1053](https://github.com/encode/httpcore/discussions/1053) 和已关闭的 [#961](https://github.com/encode/httpcore/issues/961)。这些讨论与取消后的连接状态有关，不等于它们与本次 TLS 缺口完全相同。

[修复后真实取消记录](2026-10-12-live-prompt-cancellation.json)：

- 两次均先观察到实际上游 TCP 连接，再取消；请求没有提前结束。
- 连接回收约26ms、48ms；执行槽回收约79ms、100ms。
- 最终0个残留连接、0个业务会话；取消后的下一次请求正常完成（1.41秒）。

以上证明本机连接和执行槽释放，不能证明远端供应商的计算或计费已经停止。

## 验证命令

```bash
go test ./internal/session ./internal/server
go test -race ./internal/session ./internal/workbench/preview ./internal/server
cd worker && uv run pytest tests/test_stream_lifetime.py tests/test_provider_resilience.py -q
cd worker && uv run ruff check . && uv run ruff format --check . && uv run mypy && uv run pytest
npm test --workspace @gogosling/web -- src/pages/chat/PromptEnhancement.test.tsx
```

全部通过。取消回归使用本地真实 TCP 服务器，分别覆盖等待响应头和 TLS 握手；不访问真实网络。完整 Python 测试178项通过。前端7项覆盖点击调用、可选意见、原始还原、取消、切换、手改后改回、发送后迟到响应、失败保留和旧能力回退。

真实网关另用临时验收程序运行，已保存脱敏输入、输出、耗时与资源观测；密钥扫描通过。该程序与临时配置不作为产品代码提交。

此记录取代此前[提示词优化记录](2026-10-12-prompt-enhancement.md)中“先预览再替换”的旧交互描述；旧记录中的当时测试数字不作为当前版本数字。
