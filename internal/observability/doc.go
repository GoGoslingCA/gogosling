// Package observability 统一日志、追踪与指标。
//
// 职责：
//   - 日志：Setup 同时输出到 stderr（文本）与 <dataDir>/logs/gogoslingd.log（JSON），
//     文件按大小轮转（默认 20 MiB、保留 5 个、可选 gzip 旧文件）；
//     所有输出都经过脱敏 Handler：键名表示凭据（api_key、*_token、secret、authorization、password 等）
//     的字符串属性值被替换为 "[redacted]"，键名统一为 snake_case。
//   - 指标：Registry 提供 Counter / Gauge / Histogram（固定桶）三种进程内指标，
//     Snapshot 导出为可 JSON 序列化的快照，供本地 API 或命令行展示。
//   - 追踪：Tracer / Span 接口；默认实现 LogTracer 把每个跨度结束时的信息写成一条日志。
//     属性键名采用 OpenTelemetry GenAI 语义约定（见 semconv.go），日后接入 OTel SDK 时
//     只需替换 Tracer 实现，调用方不改。
//
// 约定：
//   - 本包不持有任何全局可变状态；Logger、Registry、Tracer 都由组合根创建并注入。
//   - 默认只在本机保存；任何对外上报都必须由用户显式开启（本包不提供上报实现）。
//   - 脱敏只按键名判断，不解析消息文本；调用方仍然不得把凭据放进 msg 或未命名的位置。
//
// 待实现：
//   - OpenTelemetry SDK 适配的 Tracer（导出 OTLP）；
//   - 按日期轮转（当前只按大小）。
//
// 边界：本包是叶子包，只依赖标准库，可被任何包导入。
package observability
