// Package identity 管理"谁在访问内核"：本机访问令牌、运行时句柄文件与调用方身份（Principal）。
//
// 桌面场景默认只有一个本机用户，但本地 API 仍然必须鉴权，
// 否则任意网页或本机其他进程都能驱动智能体执行操作（回环地址不是安全边界）。
//
// 组成：
//   - NewToken：32 字节随机数的 base64url 令牌，每次守护进程启动重新生成；
//   - Authenticator：校验 Authorization: Bearer <token>，用常量时间比较，成功返回本机 Principal；
//     令牌只接受请求头，不接受查询参数（会进访问日志、浏览器历史与 Referer）；
//   - HandleFile：<dataDir>/daemon.json（{pid, endpoint, token, version, started_at}），
//     0600 权限、原子写入，桌面壳与 CLI 靠它发现守护进程；守护进程退出时应 Remove；
//   - WithPrincipal / FromContext / RequireRole：Principal 随 context 传递，
//     RequireRole 在缺少身份时返回 unauthorized、角色不足时返回 forbidden。
//
// 单用户桌面模式：本机 Principal 的 ID 固定为 LocalPrincipalID（"local"）、角色 admin。
// IM 通道的对端用 ChannelPeerPrincipal 构造，角色 member。
//
// 待实现：
//   - 可选的多用户模式（家庭、小团队共享一台主机）：用户、角色、登录会话；
//   - 目录服务与单点登录的接入点（延后，按需求评估）。
//
// 边界：只回答"是谁、是否已认证"；"能做什么"由 permission 包决定。
// 本包只导入 model（以及标准库）。
package identity
