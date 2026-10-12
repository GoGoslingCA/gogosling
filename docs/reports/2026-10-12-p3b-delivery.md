# P3B 与界面统一交付

2026-10-12。本地工作树已实现并验证，未提交、未推送、未发布安装包。后续资源调度属于新的实施范围，本记录不将它们算作已完成。

## 完成的流程

- 已保存成果 → 单文件差异预览 → 原权限引擎判定 → 单次审批 → 条件应用 → 独立撤销预览。撤销新建按 destructive 删除风险处理。
- 操作具有独立持久 Operation，未伪造模型 Run；模型工具清单不暴露人工文件操作。拒绝规则优先，旧 Run 审批保留。
- 原件、提议与真实执行结果由内核保存。先记录文件结果，再清理保留原件，最后确认终态；任一步无法确认进入 recovery_required。
- 外部编辑、目录身份、文件 inode、权限及摘要变化均核对。未知原件不删除，不能只凭内容相同认定成功。
- 前端重试使用固定幂等键；未知响应先重读状态，不自动重新应用。过期撤销可从原操作重新生成预览。
- 原有任务、消息、草稿、成果版本和快捷键继续可用；无 changesets capability 的服务保留旧保存/导出路径。

## 视觉与交互

界面统一灰白层次、细分隔线、圆角和控件间距；主要操作保留品牌蓝。设置使用单一双栏，窄窗口切换顶部标签，支持搜索与键盘导航。

长解释默认折叠，真实步骤改为紧凑轨迹。消息与成果支持安全的 chart JSON 柱状图/折线图，附数据表和源码；最多 50 个有限数值，非法内容保留原代码，不执行脚本或外部资源。默认输出建议更简洁，不覆盖用户明确要求的详细内容。

## 公开接口

```go
workbench.NewChanges(model.ChangeStore, workbench.ChangeFiles, *event.Bus) (*workbench.ChangeService, error)
tools.Executor.ExecuteUserOperation(context.Context, string, model.ToolCall) (model.ToolResult, error)
workspace.Workspace.Snapshot(context.Context, string, int64) (workspace.Snapshot, error)
workspace.Workspace.Apply(context.Context, string, string, workspace.Snapshot, *string) (workspace.Receipt, error)
workspace.Workspace.Recover(context.Context, string, string, workspace.Snapshot, *string) (workspace.Receipt, error)
workspace.Workspace.Finalize(context.Context, string, string) error
```

HTTP 在 `/api/v1` 增加 changesets 的准备、列表、读取、差异、应用、撤销预览、取消、恢复核对。`apply` 返回 202 仅表示受理。完整形状见 OpenAPI；本时点 51 个路径、54 个 schema、228 处引用均可解析。

数据库升至 schema 4：003 增加 Operation 与兼容审批关联，004 保存 ChangeSet 和独立幂等记录。复用升级前一致快照；终态操作留存，待审批/执行/恢复的主体阻止删除所属任务或助手。

## 实际验证

先使用仓库指定 Go/Node PATH（e2e 另加入 `$HOME/.local/bin`），执行：

```bash
make lint
make test
make e2e
make build
npm test
npm run build
go test -race ./internal/tools ./internal/permission ./internal/store
go test -race ./internal/workspace
go test -race ./internal/server -run 'Test文件|Test外部|Test拒绝|Test应用|Test两个|Test清理|Test保留|Test启动恢复|Test显式|Test活跃|Test执行|Test旧工作' -count=1
```

- Go 格式/vet/全仓测试、Python ruff/format/mypy 通过；Python 176 项。
- 最终 Web 313、桌面 231，合计 544 项通过；Web/Electron 与 Go 构建通过。
- 真实 Python worker + 假模型 e2e 通过且未跳过；此次未重新调用付费模型。
- 真实文件测试覆盖创建/替换/撤销、外部改写、规则拒绝、取消、过期、未知响应、单次审批、两次并发恢复、原件清理失败、最终落账失败、重开数据库后的状态恢复和旧能力降级。
- macOS 实际验证 0644/0755 权限保留、ACL/特殊权限/不支持属性拒绝；workspace Linux/Windows 交叉编译通过。
- 浏览器实际完成预览、审批、创建、独立删除撤销，确认文件消失；外部改写后再批准应用得到冲突，外部字节保持不变。图表、设置搜索、真实主题切换、900×760 无横向溢出均已检查。
- 全量前端检查曾因新增过期保护遇到固定旧时间测试数据；已改为有效期内测试预览，随后全量通过。

## 边界

支持单文件、最多 512 KiB 的普通 UTF-8 文本。Linux/macOS 使用描述符路径解析与禁止覆盖的安装；Windows 暂不开放原位修改，保留导出与副本保存。Linux 条件写尚未真机执行；原生安装包未真机验收。

不承诺对任意同 UID 外部写者的原子 CAS。旧文件描述符继续写入、外部移动目录等不确定情况需核对并保留原件。恢复核对必要时会把保留原件恢复至空缺目标，不覆盖外部新文件，不自动重做提议。

本地预览使用隔离测试数据和假模型，不能代表真实模型任务质量。密钥没有写入仓库、事件、测试或日志。原有 team-workflow 文档修改保留。
