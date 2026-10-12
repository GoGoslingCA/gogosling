// Package scheduler 负责定时任务：表达式校验、下一次触发时间的计算、到点触发与错过补偿。
//
// 桌面设备会休眠、会离线、时钟会跳变，因此本包不用"注册一个定时器等它响"的模型，
// 而是把每个任务的 NextRunAt 以绝对时间（UTC）持久化，由一个固定节拍（默认 15s）的轮询循环
// 比较"现在"与 NextRunAt 决定是否触发。这样进程重启、设备休眠后不需要恢复任何定时器状态，
// 错过的触发也能被准确识别并按任务配置的 MissedPolicy 处理。
//
// 职责：
//   - 任务的创建、更新、启停、删除；校验 cron 表达式（五字段或 @hourly/@daily/@weekly/@monthly/@every）
//     与 IANA 时区；计算并持久化 NextRunAt
//   - 调度循环：每个节拍找出到期任务并触发；错过超过一个周期时按 MissedPolicy 跳过或补跑一次；
//     同一任务不并发（上一轮未结束则记为 skipped）；执行后更新 LastRunAt/LastStatus/LastError
//   - 休眠与时钟跳变检测：相邻节拍间隔超过 3 倍 Tick 或出现倒退，视为唤醒并重算所有 NextRunAt
//   - 手动触发（TriggerNow）与 job.triggered 事件
//   - 智能体被删除后由组合根调用 Forget 移除它的任务（数据库记录已级联删除）
//
// 边界与设计要点：
//   - 只依赖 model、event、config 与 robfig/cron 的 Parser（不使用它的调度器）；执行通过本包定义的
//     Runner 接口交给 session 层（push_text 直接写消息，run_agent 发起运行），时间来源通过 Clock 接口注入
//   - 时区：Job.Timezone 为空时使用 Options.DefaultTimezone，后者为空时使用系统时区；内部时间一律 UTC
//   - 触发即推进：触发一个任务时先把 NextRunAt 推进到下一次并落库，再异步执行，保证崩溃后"至多一次"；
//     落库失败则回滚该次推进且不执行、不发布触发事件，后续节拍或重启按原到期时间重试
//   - 导出类型 Service 并发安全
//
// 待实现（超出本包范围，由其他模块或后续版本提供）：
//   - SessionKey 的格式校验与目标会话的解析由 session 层在执行时完成；本包只透传
//   - DefaultTimezone 在运行期变更后需要重启进程才对未指定时区的任务生效
//   - 执行历史（每次触发的记录表）：当前只保留最近一次的状态
package scheduler
