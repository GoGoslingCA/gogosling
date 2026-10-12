// Package secret 负责凭据的安全存放。
//
// 调用方只以名称引用凭据（例如 "provider:<id>:api_key"、"channel:<id>:bot_token"），
// 数据库与配置里只保存这个名称；凭据本身只在使用点被 Get 出来，用完即丢。
//
// 两个后端：
//   - keyring：操作系统凭据存储（macOS Keychain、Windows Credential Manager、Linux Secret Service），
//     经 github.com/zalando/go-keyring 访问，service 名固定为 ServiceName；
//     打开时写入、读取并删除一条探测记录以确认可用。
//   - file：<dataDir>/secrets.enc，AES-256-GCM 整文件加密，密钥保存在 <dataDir>/secret.key
//     （0600，首次生成；丢失后无法解密）。
//
// 后端选择（OpenWith）只在首次启动时自动进行（keyring 可用则用它，否则回退到 file 并警告），
// 选定结果写入 <dataDir>/secret.backend（0600）。之后每次启动都使用记录的后端：它不可用时返回
// ErrBackendUnavailable，绝不静默切换——否则在桌面会话与 SSH/systemd 环境之间切换时，
// 已存的凭据会"消失"、新凭据又写到另一处。用户可通过 Options.Backend（组合根从
// GOGOSLING_SECRET_BACKEND 读取）显式切换，切换会更新记录，但不迁移原后端里的凭据。
//
// 加密文件格式：JSON 信封 {"version":1,"nonce":…,"ciphertext":…}，明文是 名称 → 值 的 JSON 对象；
// 每次写入都用新的随机 nonce 整体重新加密，并通过"临时文件 + 重命名"原子替换。
//
// 错误：本包是叶子包不能导入 model，因此用哨兵错误（ErrNotFound、ErrInvalidName、ErrTooLarge、
// ErrCorrupt、ErrKeyMissing、ErrBackendUnavailable）表达失败原因。哨兵错误到 model.Code 的映射
// 全仓只有一份：组合根 internal/app 的 secretAdapter（ErrNotFound → not_found，
// ErrInvalidName / ErrTooLarge → invalid，其余 → unavailable）；领域包只接收映射后的错误。
//
// 并发：所有 Store 实现可并发使用；文件后端用进程内互斥锁串行化读改写，
// 跨进程一致性依赖守护进程的单实例锁（daemon.lock）。
//
// 待实现：
//   - 密钥轮换（重新加密 secrets.enc）；
//   - 从文件后端迁移到系统凭据存储的一次性工具。
//
// 边界：本包是叶子包；任何包都不得把凭据明文写入 store、日志或事件。
package secret
