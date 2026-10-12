// Package store 是控制面数据的持久化层：用纯 Go 的 SQLite 驱动（modernc.org/sqlite）
// 实现 model.Store 的全部仓储接口，并负责嵌入式迁移。
//
// 职责：
//   - Open 打开或创建数据库（目录 0700、文件 0600），启用 WAL、外键与 busy_timeout，
//     并在返回前把 migrations/ 下的 SQL 按版本顺序应用到位（可重复启动，幂等）。
//   - 每张表一组仓储方法；错误统一映射为带 model.Code 的错误：
//     无行 → CodeNotFound，UNIQUE / 主键冲突 → CodeConflict，外键失败 → CodeConflict，
//     NOT NULL / CHECK 失败 → CodeInvalid，SQLITE_BUSY → CodeUnavailable，
//     上下文取消 / 超时 → CodeCancelled / CodeTimeout，其余 → CodeInternal。
//   - Tx 在一个 BEGIN IMMEDIATE 事务内执行回调，回调拿到的 Store 读写都走同一事务。
//
// 并发策略（导出类型 *Store 可并发使用）：
//   - 两个连接池。写池 MaxOpenConns=1 且 DSN 带 _txlock=immediate：所有写操作在
//     database/sql 层排队等待唯一的写连接（等待尊重 ctx），因此进程内的写者之间
//     永远不会触发 SQLITE_BUSY，也不会出现"先读后写再升级锁失败"（SQLITE_BUSY_SNAPSHOT）。
//   - 读池默认 4 条连接，走普通 BEGIN（不需要事务时直接单条查询）。WAL 模式下读者
//     看到的是提交快照，不会被写者阻塞，写者也不会被读者阻塞。
//   - busy_timeout=5000 只作为跨进程（例如 CLI 直接打开数据库）或检查点竞争的兜底。
//   - Tx 不可嵌套，回调里必须使用传入的 Store；对外层 Store 再发起写操作会等待写连接
//     直到 ctx 结束（返回 CodeTimeout / CodeCancelled），不会死锁但也不会成功。
//
// 时间与 JSON 的存储格式见 migrations/001_init.sql 顶部的说明。
//
// 待实现：
//   - 升级前自动快照（VACUUM INTO）与数据目录的导出 / 恢复；
//   - 审计与消息的保留期清理。
//
// 边界：只包含 SQL 与数据映射；除 internal/model 外不依赖任何 internal 包，
// 也不被前端直接访问。
package store
