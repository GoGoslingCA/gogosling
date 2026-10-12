// Package permission 决定"这次操作是否允许"，并记录决定。
//
// 所有会产生副作用的操作（写文件、执行命令、访问网络、发送消息、调用连接器）
// 在执行前都必须经过本包的 Service.Check。
//
// 职责：
//   - 规则引擎：规则文件 <dataDir>/security/rules.json（首次使用时写入内置默认规则），
//     规则 = {tool 通配, risk, path 通配, command 正则, effect allow|ask|deny}。
//   - 内置防护：危险命令硬拒绝（rm -rf /、mkfs、dd of=/dev/、curl|sh、关机、格式化磁盘等）、
//     内核数据文件保护（大小写变体、Windows 设备路径前缀、符号链接都按同一目录判定）、
//     路径越界检测（越出工作区的路径不能被档位默认放行）。
//   - 路径匹配的平台差异：darwin / windows 上路径类规则不区分大小写；deny 规则在所有平台上都不区分大小写；
//     Windows 上已存在路径先展开 8.3 短名再匹配。
//   - 判定顺序：内置防护 → 显式 deny → 显式 allow → 会话级授权 → 显式 ask → 自治档位默认；
//     会话含不可信输入时 execute / network / destructive 至少询问（"不可信输入 + 对外作用"不同时自动放行）。
//   - 档位默认：auto_in_sandbox 只在 Request.Isolated（tools 按 sandbox 机制填写）为真时自动放行
//     execute / network；没有操作系统级隔离（Linux / Windows 上的 sandbox.Direct）时退回 ask_risky 的行为。
//   - 审批代理：创建 model.ApprovalRequest、发布 approval.requested / approval.resolved 事件、
//     阻塞等待用户决定；Decide 幂等，ScopeSession 记入内存授权表；ScopeAlways 在确认审批仍待决后
//     写入规则文件并审计（permission.rules.update），写入失败时决定降级为 once 并返回错误；
//     Run 周期性把到期请求标记为过期，并清理上一个进程遗留的孤儿记录。
//   - 审计：每次判定写 tool.check（allowed / denied / asked），每次决定写 approval.decide；
//     Details 只含路径、命令、规则等描述信息，凭据与内容类字段在写出前被移除。
//
// 待实现：
//   - 企业策略文件：由管理员下发、用户不可覆盖的上限。
//   - "敏感数据"标记：目前"三者取二"只实现了不可信输入与对外通信两个维度。
//   - 可回滚操作的事后确认（依赖 workspace 包的检查点能力）。
//
// 边界：只做决策与记录，不读写文件内容、不解析 shell 语法；为规范化路径只做只读探测
// （Lstat / EvalSymlinks / Windows GetLongPathName）；
// 实际的隔离执行由 sandbox 包完成，真正的路径约束由 workspace 包完成。
// 本包只导入 model、event 与标准库。
package permission
