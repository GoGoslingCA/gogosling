// Package sandbox 提供操作系统级的隔离执行环境。
//
// 审批只能约束"是否执行"，沙箱约束"执行时能碰到什么"。两者叠加使用。
//
// 职责：
//   - 统一的受限执行接口 Runner：Spec 描述命令、工作目录、环境、超时、可写目录白名单与网络策略，
//     Result 带回输出、退出码、是否截断、是否超时与耗时。
//   - 平台能力探测 Detect：报告当前平台可用的隔离机制（darwin: sandbox-exec；linux: bwrap；windows: 无），
//     探测不到时明确告知而不是静默降级。
//   - Direct：无隔离的直接执行，只做工作目录、环境变量白名单、超时、输出截断与进程组杀死；
//     它是所有平台的兜底实现，也是其他 Runner 复用的进程管理核心。
//   - Seatbelt：macOS 上基于 sandbox-exec 的实现。profile 模板随二进制嵌入（seatbelt.sb），
//     只允许写入 Spec.WritableDirs 与一个每次运行私有的临时目录（作为 TMPDIR），默认禁止网络。
//   - Bwrap：Linux 上基于 bubblewrap 的实现。宿主根只读，可写目录经真实路径校验并固定文件描述符，
//     /tmp、proc 与 dev 为私有挂载，隔离 user/PID/IPC/UTS 命名空间并移除 capabilities，默认隔离网络。
//     NewBwrap 会实际执行探测；宿主禁用命名空间时 New 明确记录原因并选择 Direct。
//     已构造的 Bwrap.Run 永远不因执行失败改为无隔离重试。
//
// 环境变量：子进程不继承守护进程的环境，只传递全仓唯一的白名单 config.ChildEnvAllowlist()
// （PATH、HOME、TMPDIR、代理与证书变量等）加上执行 shell 所需的增量（LC_ALL 与 Windows 的
// SystemDrive、TEMP、TMP、ComSpec、PATHEXT），再加 Spec.Env；守护进程里可能存在的密钥类变量不会进入任何命令。
// Bwrap 的 Spec.Env 通过选项配置最终命令，不影响辅助程序的初始动态加载；目标命令只在隔离完成后启动。
// 配置参数经匿名管道传递，
// 环境值不出现在宿主进程参数或临时文件里。TMPDIR/TEMP/TMP 固定为沙箱的 /tmp。
// Bwrap 同时通过 seccomp 禁止宿主路径的 Unix socket；AllowNetwork 只控制 IP 网络。
// 为兼容 Python asyncio 等，允许已连接的匿名 Unix STREAM socketpair；DGRAM socketpair 与旧 socketcall 禁止。
// io_uring 入口同时禁止，避免异步 socket 操作绕过 syscall 过滤。
// 兼容 ABI 不允许跨越过滤器，未实现过滤器的 Linux 架构会明确拒绝构造 Bwrap 并由 New 记录回退。
//
// Windows：子进程带 CREATE_NO_WINDOW（守护进程没有控制台时不弹出可见窗口）与 CREATE_NEW_PROCESS_GROUP；
// cmd.exe 不遵循 C 运行时的引号规则，因此 Spec.RawCommandLine 非空时整条命令行原样交给 CreateProcess，
// 不经 Go 的参数转义（tools 的 run_command 用它传 cmd.exe /d /s /c "<command>"）。
//
// 待实现：
//   - Windows 的 Job Object（限制子进程树与资源）；目前用 taskkill /T 结束进程树。
//   - 可选的容器与虚拟机后端。
//   - 网络出口代理：域名白名单与凭据注入，密钥不进入沙箱。
//
// 边界：不判断操作是否应该被允许；只负责按给定策略执行；是否"已隔离"由调用方用 MechanismOf 读取后
// 交给权限层（permission.Request.Isolated）。本包只导入标准库、internal/model、internal/config 与 internal/i18n。
package sandbox
