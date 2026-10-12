// Package e2e 是端到端测试：在进程内启动完整的守护进程（internal/app），用真实的 Python 工作进程
// （uv run --project worker gogosling-worker）与一个本地假 OpenAI 兼容服务走通
// "供应商 → 智能体 → 会话 → 发送 → 工具调用审批 → 完成" 的整条链路。
//
// 测试文件带构建标签 e2e，默认的 go test ./... 不会运行它们；运行方式：
//
//	make e2e      # 等价于 go test -tags e2e ./tests/e2e/ -count=1 -v
//
// 本机没有 uv 时测试自动跳过。测试不访问外部网络：模型服务是 httptest 假服务器，
// 工作进程的环境变量按白名单传递（不含代理设置）。
package e2e
