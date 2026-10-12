// Package tools 声明并执行内置工具，是"权限 → 工具 → 沙箱"链路的中间一环。
//
// 工作进程只"声明"工具调用（host.tool.call）；真正的文件、命令、网络副作用在这里发生，
// 并且必经权限判定与沙箱（架构文档 §6.3、ADR-005）。
//
// 结构：
//   - Tool：一个内置工具 = 声明（model.ToolSpec，含合法的 JSON Schema 与风险等级）+ 执行。
//     可选实现 Describer，为权限层与审批界面生成"一句话摘要 + 结构化细节"（路径、命令、URL）。
//   - Registry：登记内置工具，并按 model.Agent.Tools 过滤出某个智能体可用的子集；参数 Schema 的
//     唯一校验规则是 ValidateInputSchema（JSON 对象且 type 为 object），扩展工具在 Executor 中按同一规则过滤。
//   - Executor.ToolsFor：工具清单的唯一出处（下发给工作进程的清单与 GET /agents/{id}/tools 都用它）。
//     实现了 Conditional 的工具按智能体决定是否声明（load_skill 只在 SkillLoader.HasSkills 为真时声明）。
//   - Executor：Execute(ctx, runID, call) 的完整流程：解析运行上下文 → 找工具 → 生成摘要/细节 →
//     PermissionChecker.Check（带 Isolated：工具实现 Isolation 时由它报告，run_command 按
//     sandbox.MechanismOf(runner) != MechanismNone 判断）→ 执行 → 结果截断（默认 64 KiB，截断时置
//     ToolResult.Truncated）→ 返回。
//     "工具不存在 / 参数非法 / 权限拒绝 / 工具执行失败"都转成 IsError 的 ToolResult 回填给模型，
//     而不是中断运行；只有运行被取消或基础设施故障（无法解析运行上下文）才返回 error。
//   - 内置工具：read_file、write_file、edit_file、delete_file、list_dir、search_files（读 / 写 / 写 / 破坏 / 读 / 读）、
//     edit_file 精确替换片段、拒绝歧义匹配并可校验预期 SHA-256；delete_file 的目录删除必须显式设置 recursive。
//     run_command（执行，经 sandbox.Runner；Windows 上整条命令行经 Spec.RawCommandLine 原样交给
//     cmd.exe /d /s /c，不经 Go 的参数转义）、fetch_url（网络，带 SSRF 防护）、load_skill（隐式，按需读取技能正文）。
//     read_skill_resource 也是隐式只读工具：仅当加载器支持资源能力且智能体选中了技能时提供；
//     返回资源文本与字节数、SHA-256，权限批准后读取，脚本绝不执行。
//
// 本包对其他领域包的依赖全部收敛为包内定义的最小接口（Workspace、PermissionChecker、
// RunContextResolver、ExternalTools），由组合根 internal/app 做适配；
// 这样本包只导入 model、sandbox 与标准库（架构允许的 permission / workspace / extension 也不直接导入），
// 可以与其他领域包并行开发。
//
// 安全要点：
//   - 文件工具只接受工作区相对路径，越界由 Workspace.Resolve 拒绝；
//   - run_command 的子进程不继承守护进程环境（只有白名单 + 工作区标识），输出按流截断，默认禁网；
//   - fetch_url 只允许 http(s) GET，解析后的地址若为回环 / 私网 / 链路本地 / 保留地址一律拒绝，
//     并在建立连接时再次校验（防 DNS 重绑定），不走环境代理，重定向逐跳校验；
//   - 网页内容回填给模型时标注为不可信外部数据。
//
// 待实现：
//   - 工具结果中的附件 / 媒体（图片）回传；
//   - 面向用户的摘要文案接入 i18n（目前为中文）。
package tools
