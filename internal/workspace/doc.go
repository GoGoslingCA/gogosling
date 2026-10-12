// Package workspace 管理智能体的工作区：它能读写的文件从哪里来、存到哪里去。
//
// 职责：
//   - Manager 把 model.Agent.WorkspaceDir 翻译成磁盘目录并创建布局（根目录与 inbound/，权限 0700）。
//   - Workspace 提供以工作区相对路径表达的文件接口：Resolve / ReadFile / WriteFile / List / Stat /
//     Exists / Remove / SaveInbound。所有路径规范化规则只在 cleanRel 与 checkEscape 两处实现，
//     调用方不需要自己处理 "." 与 "/" 的语义。
//   - 路径约束：拒绝绝对路径与任何 ".." 片段，并对已存在的路径片段做符号链接解析，
//     防止经链接逃出根目录；根目录与 inbound/ 本身不可删除（darwin / windows 上名字不区分大小写，
//     Windows 上忽略末尾的点与空格，删除前再用 os.SameFile 与真实的 inbound/ 比较）。
//   - 普通写入采用"临时文件 + fsync + rename"，附件先占名再落盘，读者不会看到半写的文件。
//   - 条件文件操作 Snapshot / Apply / Recover / Finalize 在 Linux/macOS 绑定目录与文件身份，
//     用禁止覆盖的原子 rename 保留原件、安装候选和恢复；其他平台明确拒绝。原件在落账后才清理。
//     已存在文件的普通 rwx 权限与组归属从快照保留；新建为0600。不接受前端 mode。
//     特殊权限位、ACL和不能保留的扩展元数据会被拒绝；macOS的系统provenance由新inode自行管理。
//     保留目录对本包的文件 API 不可见、不可访问，含保留目录的父目录也不能经 Remove 删除。
//   - 同 Manager 的写入串行化，但不等于与任意外部进程的 CAS；持有旧文件描述符的写者、
//     同 UID 的外部命令和父目录重命名仍有明确边界。执行层必须协调该工作区的工具/命令运行。
//
// 待实现：
//   - 用户授权的外部目录挂载（只读或读写）以及对象存储等远程后端。
//   - 条件操作在 Windows 等其他平台的安全实现。
//   - 附件目录的配额与清理策略（当前只限制单个附件大小）。
//
// 边界：本包只依赖标准库、model/i18n 叶子包与批准的 x/sys 系统调用；领域包不得绕过本包直接读写工作区内容文件。
// 权限判定不在这里：本包只保证"不越出根目录"，是否允许某个操作由 permission 包决定。
package workspace
