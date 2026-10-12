// Package agent 管理智能体的定义与生命周期。
//
// 智能体是一份可持久化的配置：人格与系统提示、所用模型、可用工具与扩展、
// 工作区、权限档位。真正的推理循环不在这里，而在 engine 包监管的工作进程中。
//
// 职责：
//   - Service 提供增删改查：校验名称、模型引用（经 ProviderChecker）、自主等级与工具名；
//     创建时打开工作区并写入 SOUL.md（系统提示的可编辑镜像），发布 agent.created / updated / deleted。
//     删除时先经可选的 SessionCleaner（WithSessionCleaner 注入）删除会话并发布 session.deleted，
//     有在途运行时返回 conflict 而不删除。
//   - Template / Library 提供模板库：LoadTemplates 从任意 fs.FS 读取 <id>/template.json + SOUL.md，
//     BuiltinTemplates 加载随二进制嵌入的 general-assistant 与 ops-engineer。
//   - DefaultTools 定义新建智能体的默认内置工具集。
//
// 待实现：
//   - 子智能体库与多智能体协作（协调者与成员）。
//   - 模板版本管理：记录创建时的模板版本，支持提示升级。
//   - 从外部目录（extensions/bundled）加载用户自定义模板。
//
// 边界：本包只依赖标准库、model、event 与 config；不直接调用模型，不直接访问 IM 通道，
// 不删除工作区文件。工作区、供应商与会话清理能力通过本包定义的最小接口（WorkspaceOpener、
// ProviderChecker、SessionCleaner）注入，由组合根适配。
package agent
