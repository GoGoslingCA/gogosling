package server

import (
	"net/http"
	"time"
)

// SetKeepaliveForTest 调整事件流保活间隔（只在测试中使用）。
func SetKeepaliveForTest(s *Server, d time.Duration) { s.keepalive = d }

// SetSSEBufferForTest 调整事件流订阅缓冲长度（只在测试中使用）。
func SetSSEBufferForTest(s *Server, n int) { s.sseBuffer = n }

// ChainForTest 用服务器的中间件链包装任意处理器，用于验证 panic 恢复等行为。
func ChainForTest(s *Server, h http.Handler) http.Handler { return s.chain(h) }
